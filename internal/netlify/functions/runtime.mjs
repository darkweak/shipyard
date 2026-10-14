// Shipyard's Netlify Functions runtime.
//
// netlify-server (the Go static server) starts this script with Node.js and
// proxies function requests to it. It loads the functions bundled at build
// time (manifest.json next to this file) and runs them with Netlify's two
// APIs: Lambda-compatible handlers (v1) and Request/Response handlers (v2),
// plus background (-background suffix) and scheduled functions.
//
// No dependencies: it must run on whatever Node.js version the site uses.
import http from 'node:http'
import crypto from 'node:crypto'
import path from 'node:path'
import { AsyncLocalStorage } from 'node:async_hooks'
import { createRequire } from 'node:module'
import { readFileSync, realpathSync } from 'node:fs'
import { fileURLToPath, pathToFileURL } from 'node:url'

const SYNC_TIMEOUT_MS = Number(process.env.SHIPYARD_FUNCTIONS_TIMEOUT_MS || 30000)
const BACKGROUND_TIMEOUT_MS = 15 * 60 * 1000

const als = new AsyncLocalStorage()

// ---------------------------------------------------------------------------
// Netlify global (v2 API): Netlify.env and Netlify.context.
// ---------------------------------------------------------------------------
function installNetlifyGlobal() {
  const env = {
    get: (k) => process.env[k],
    set: (k, v) => {
      process.env[k] = v
    },
    has: (k) => Object.prototype.hasOwnProperty.call(process.env, k),
    delete: (k) => {
      delete process.env[k]
    },
    toObject: () => ({ ...process.env }),
  }
  const netlify = { env }
  Object.defineProperty(netlify, 'context', { get: () => als.getStore() || null, enumerable: true })
  globalThis.Netlify = netlify
}

// ---------------------------------------------------------------------------
// Loading
// ---------------------------------------------------------------------------
export async function loadFunctions(dir) {
  const manifest = JSON.parse(readFileSync(path.join(dir, 'manifest.json'), 'utf8'))
  const functions = new Map()
  for (const entry of manifest.functions) {
    const file = path.join(dir, entry.file)
    try {
      // Bundles are ESM; CommonJS code inside them resolves require() through
      // this global (esbuild's __require helper looks it up).
      globalThis.require = createRequire(file)
      const mod = await import(pathToFileURL(file).href)
      let kind
      let handler
      if (typeof mod.default === 'function') {
        kind = 'v2'
        handler = mod.default
      } else if (typeof mod.handler === 'function') {
        kind = 'v1'
        handler = mod.handler
      } else if (mod.default && typeof mod.default.handler === 'function') {
        kind = 'v1' // CommonJS `exports.handler`, wrapped by the ESM bundle
        handler = mod.default.handler
      } else {
        console.error(`[functions] ${entry.name}: no handler found (export a default function or a "handler")`)
        continue
      }
      const config = (mod.config && typeof mod.config === 'object' && mod.config) || {}
      functions.set(entry.name, {
        name: entry.name,
        kind,
        handler,
        config,
        schedule: entry.schedule || config.schedule || '',
        background: entry.name.endsWith('-background'),
      })
    } catch (err) {
      console.error(`[functions] ${entry.name}: failed to load:`, err)
    }
  }
  return functions
}

const toArray = (v) => (v === undefined || v === null ? [] : Array.isArray(v) ? v : [v])

/** Routes for functions declaring `config.path` (v2), read by netlify-server. */
export function routes(functions) {
  const out = []
  for (const fn of functions.values()) {
    if (fn.kind !== 'v2' || !fn.config.path || fn.schedule) continue
    out.push({
      name: fn.name,
      path: toArray(fn.config.path),
      excludedPath: toArray(fn.config.excludedPath),
      method: toArray(fn.config.method).map((m) => String(m).toUpperCase()),
      preferStatic: !!fn.config.preferStatic,
    })
  }
  return out
}

// ---------------------------------------------------------------------------
// Invocation
// ---------------------------------------------------------------------------
const TEXT_TYPES = /^(text\/|application\/(json|xml|javascript|x-www-form-urlencoded|graphql|ld\+json|[\w.+-]*\+(json|xml)))/i

function requestId() {
  return crypto.randomUUID ? crypto.randomUUID() : crypto.randomBytes(16).toString('hex')
}

function headerObject(headers) {
  const single = {}
  const multi = {}
  for (const [k, v] of Object.entries(headers)) {
    if (k.startsWith('x-shipyard-')) continue
    const vals = toArray(v).map(String)
    multi[k] = vals
    single[k] = vals.join(', ')
  }
  return { single, multi }
}

function timeout(ms, name) {
  let timer
  const promise = new Promise((_, reject) => {
    timer = setTimeout(() => reject(Object.assign(new Error(`Function ${name} timed out after ${ms / 1000}s`), { name: 'TimeoutError' })), ms)
    if (timer.unref) timer.unref()
  })
  return { promise, clear: () => clearTimeout(timer) }
}

async function race(p, ms, name) {
  const t = timeout(ms, name)
  try {
    return await Promise.race([p, t.promise])
  } finally {
    t.clear()
  }
}

/**
 * Runs a function. `inc` is a normalized request:
 * { method, url: URL (as the client sent it), path: the (possibly rewritten) path + query
 *   used for v1 events, headers, body: Buffer, ip, params }.
 * Returns { status, headers: [name, value][], body: Buffer | ReadableStream | Node stream }.
 */
export async function invoke(fn, inc, ms = SYNC_TIMEOUT_MS) {
  return fn.kind === 'v2' ? invokeV2(fn, inc, ms) : invokeV1(fn, inc, ms)
}

async function invokeV1(fn, inc, ms) {
  const target = new URL(inc.path || inc.url.pathname + inc.url.search, inc.url)
  const { single, multi } = headerObject(inc.headers)
  const query = {}
  const multiQuery = {}
  for (const [k, v] of target.searchParams) {
    query[k] = v
    ;(multiQuery[k] = multiQuery[k] || []).push(v)
  }
  const ctype = single['content-type'] || ''
  const binary = inc.body.length > 0 && ctype !== '' && !TEXT_TYPES.test(ctype)
  const event = {
    rawUrl: target.href,
    rawQuery: target.search.slice(1),
    path: target.pathname,
    httpMethod: inc.method,
    headers: single,
    multiValueHeaders: multi,
    queryStringParameters: query,
    multiValueQueryStringParameters: multiQuery,
    body: inc.body.length ? (binary ? inc.body.toString('base64') : inc.body.toString('utf8')) : null,
    isBase64Encoded: binary,
  }
  const deadline = Date.now() + ms
  const context = {
    callbackWaitsForEmptyEventLoop: true,
    functionName: fn.name,
    functionVersion: '$LATEST',
    invokedFunctionArn: '',
    memoryLimitInMB: '1024',
    awsRequestId: requestId(),
    logGroupName: '',
    logStreamName: '',
    clientContext: {},
    getRemainingTimeInMillis: () => Math.max(0, deadline - Date.now()),
  }
  const run = new Promise((resolve, reject) => {
    let settled = false
    const done = (err, res) => {
      if (settled) return
      settled = true
      if (err) reject(err)
      else resolve(res)
    }
    try {
      const r = fn.handler(event, context, done)
      if (r && typeof r.then === 'function') r.then((v) => done(null, v), done)
    } catch (err) {
      done(err)
    }
  })
  const result = await race(run, ms, fn.name)
  if (!result || typeof result !== 'object') {
    throw new Error(`Function ${fn.name} returned ${result === undefined ? 'undefined' : typeof result} instead of a response object`)
  }
  const headers = []
  for (const [k, v] of Object.entries(result.headers || {})) headers.push([k, String(v)])
  for (const [k, vals] of Object.entries(result.multiValueHeaders || {})) for (const v of toArray(vals)) headers.push([k, String(v)])
  let body = result.body
  if (body === undefined || body === null) body = Buffer.alloc(0)
  else if (typeof body === 'string') body = result.isBase64Encoded ? Buffer.from(body, 'base64') : Buffer.from(body, 'utf8')
  else if (!(typeof body.getReader === 'function' || typeof body.pipe === 'function' || Buffer.isBuffer(body))) body = Buffer.from(String(body))
  return { status: result.statusCode || 200, headers, body }
}

function cookieJar(header) {
  const incoming = {}
  for (const part of String(header || '').split(';')) {
    const i = part.indexOf('=')
    if (i > 0) incoming[part.slice(0, i).trim()] = decodeURIComponent(part.slice(i + 1).trim())
  }
  const outgoing = []
  const serialize = (c) => {
    let s = `${c.name}=${encodeURIComponent(c.value ?? '')}`
    if (c.domain) s += `; Domain=${c.domain}`
    s += `; Path=${c.path || '/'}`
    if (c.expires) s += `; Expires=${new Date(c.expires).toUTCString()}`
    if (c.maxAge !== undefined) s += `; Max-Age=${c.maxAge}`
    if (c.httpOnly) s += '; HttpOnly'
    if (c.secure) s += '; Secure'
    if (c.sameSite) s += `; SameSite=${c.sameSite}`
    return s
  }
  return {
    outgoing,
    api: {
      get: (name) => incoming[name],
      set: (a, b) => outgoing.push(serialize(typeof a === 'string' ? { name: a, value: b } : a)),
      delete: (a) => {
        const c = typeof a === 'string' ? { name: a } : a
        outgoing.push(serialize({ ...c, value: '', expires: 0 }))
      },
    },
  }
}

async function invokeV2(fn, inc, ms) {
  if (typeof Request === 'undefined') throw new Error('Request/Response handlers need Node.js 18 or later (set NODE_VERSION)')
  const headers = new Headers()
  for (const [k, v] of Object.entries(inc.headers)) {
    if (k.startsWith('x-shipyard-')) continue
    for (const x of toArray(v)) headers.append(k, String(x))
  }
  const hasBody = !['GET', 'HEAD'].includes(inc.method) && inc.body.length > 0
  const request = new Request(inc.url.href, { method: inc.method, headers, body: hasBody ? inc.body : undefined })
  const jar = cookieJar(inc.headers.cookie)
  const env = process.env
  const context = {
    account: { id: '' },
    cookies: jar.api,
    deploy: { context: env.CONTEXT || 'production', id: env.DEPLOY_ID || '', published: true },
    geo: {},
    ip: inc.ip,
    params: inc.params || {},
    requestId: requestId(),
    server: { region: 'shipyard' },
    site: { id: env.SITE_ID || '', name: env.SITE_NAME || '', url: env.URL || inc.url.origin },
    url: inc.url,
    json: (data, init = {}) => {
      const h = new Headers(init.headers)
      if (!h.has('content-type')) h.set('content-type', 'application/json')
      return new Response(JSON.stringify(data), { ...init, headers: h })
    },
    log: (...args) => console.log(...args),
    waitUntil: (p) => {
      Promise.resolve(p).catch((err) => console.error(`[functions] ${fn.name} waitUntil:`, err))
    },
    next: () => {
      throw new Error('context.next() is only available in Edge Functions')
    },
  }
  let response = await race(
    als.run(context, () => fn.handler(request, context)),
    ms,
    fn.name,
  )
  if (response === undefined || response === null) response = new Response(null, { status: 204 })
  if (typeof response.status !== 'number' || !response.headers) {
    throw new Error(`Function ${fn.name} must return a Response`)
  }
  const out = []
  response.headers.forEach((v, k) => {
    if (k !== 'set-cookie') out.push([k, v])
  })
  const setCookies = typeof response.headers.getSetCookie === 'function' ? response.headers.getSetCookie() : toArray(response.headers.get('set-cookie'))
  for (const c of [...setCookies, ...jar.outgoing]) out.push(['set-cookie', c])
  return { status: response.status, headers: out, body: response.body || Buffer.alloc(0) }
}

async function writeResult(res, result) {
  const headers = {}
  for (const [k, v] of result.headers) {
    const key = k.toLowerCase()
    if (key === 'set-cookie') (headers[key] = headers[key] || []).push(v)
    else headers[key] = v
  }
  const body = result.body
  if (Buffer.isBuffer(body) || body instanceof Uint8Array) {
    headers['content-length'] = String(body.length)
    res.writeHead(result.status, headers)
    res.end(body)
    return
  }
  delete headers['content-length']
  res.writeHead(result.status, headers)
  if (typeof body.getReader === 'function') {
    const reader = body.getReader()
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      res.write(value)
    }
    res.end()
  } else if (typeof body.pipe === 'function') {
    body.pipe(res)
  } else {
    res.end()
  }
}

// ---------------------------------------------------------------------------
// Scheduled functions (cron expressions, evaluated in UTC like on Netlify)
// ---------------------------------------------------------------------------
const MACROS = {
  '@yearly': '0 0 1 1 *',
  '@annually': '0 0 1 1 *',
  '@monthly': '0 0 1 * *',
  '@weekly': '0 0 * * 0',
  '@daily': '0 0 * * *',
  '@midnight': '0 0 * * *',
  '@hourly': '0 * * * *',
}
const NAMES = {
  month: { jan: 1, feb: 2, mar: 3, apr: 4, may: 5, jun: 6, jul: 7, aug: 8, sep: 9, oct: 10, nov: 11, dec: 12 },
  dow: { sun: 0, mon: 1, tue: 2, wed: 3, thu: 4, fri: 5, sat: 6 },
}

function parseField(field, min, max, names) {
  const values = new Set()
  for (const part of field.split(',')) {
    const [range, stepStr] = part.split('/')
    const step = stepStr === undefined ? 1 : Number(stepStr)
    if (!Number.isInteger(step) || step < 1) throw new Error(`invalid step in "${field}"`)
    const num = (s) => {
      const n = names && names[s.toLowerCase()] !== undefined ? names[s.toLowerCase()] : Number(s)
      if (!Number.isInteger(n) || n < min || n > max + (names === NAMES.dow ? 1 : 0)) throw new Error(`invalid value "${s}"`)
      return n
    }
    let lo
    let hi
    if (range === '*') {
      lo = min
      hi = max
    } else if (range.includes('-')) {
      const [a, b] = range.split('-')
      lo = num(a)
      hi = num(b)
    } else {
      lo = num(range)
      hi = stepStr === undefined ? lo : max
    }
    for (let v = lo; v <= hi; v += step) values.add(names === NAMES.dow && v === 7 ? 0 : v)
  }
  return values
}

export function parseCron(expr) {
  const fields = (MACROS[expr.trim().toLowerCase()] || expr).trim().split(/\s+/)
  if (fields.length !== 5) throw new Error(`invalid cron expression "${expr}" (expected 5 fields)`)
  return {
    minute: parseField(fields[0], 0, 59),
    hour: parseField(fields[1], 0, 23),
    dom: parseField(fields[2], 1, 31),
    month: parseField(fields[3], 1, 12, NAMES.month),
    dow: parseField(fields[4], 0, 6, NAMES.dow),
    domAny: fields[2] === '*',
    dowAny: fields[4] === '*',
  }
}

export function cronMatches(c, d) {
  if (!c.minute.has(d.getUTCMinutes()) || !c.hour.has(d.getUTCHours()) || !c.month.has(d.getUTCMonth() + 1)) return false
  const dom = c.dom.has(d.getUTCDate())
  const dow = c.dow.has(d.getUTCDay())
  // Standard cron: when both day fields are restricted, either may match.
  if (c.domAny) return dow
  if (c.dowAny) return dom
  return dom || dow
}

export function nextRun(c, from) {
  const d = new Date(from)
  d.setUTCSeconds(0, 0)
  for (let i = 0; i < 366 * 24 * 60; i++) {
    d.setUTCMinutes(d.getUTCMinutes() + 1)
    if (cronMatches(c, d)) return new Date(d)
  }
  return null
}

function startScheduler(functions, port) {
  const jobs = []
  for (const fn of functions.values()) {
    if (!fn.schedule) continue
    try {
      jobs.push({ fn, cron: parseCron(fn.schedule) })
      console.log(`[functions] ${fn.name}: scheduled "${fn.schedule}" (UTC)`)
    } catch (err) {
      console.error(`[functions] ${fn.name}: ${err.message}`)
    }
  }
  if (jobs.length === 0) return
  const tick = () => {
    const now = new Date()
    for (const job of jobs) {
      if (!cronMatches(job.cron, now)) continue
      const next = nextRun(job.cron, now)
      const body = Buffer.from(JSON.stringify({ next_run: next ? next.toISOString() : null }))
      const url = new URL(`http://127.0.0.1:${port}/.netlify/functions/${job.fn.name}`)
      const inc = { method: 'POST', url, path: url.pathname, headers: { 'content-type': 'application/json' }, body, ip: '127.0.0.1', params: {} }
      console.log(`[functions] ${job.fn.name}: scheduled run`)
      invoke(job.fn, inc, BACKGROUND_TIMEOUT_MS)
        .then((r) => console.log(`[functions] ${job.fn.name}: finished with status ${r.status}`))
        .catch((err) => console.error(`[functions] ${job.fn.name}: scheduled run failed:`, err))
    }
    setTimeout(tick, 60000 - (Date.now() % 60000) + 50)
  }
  setTimeout(tick, 60000 - (Date.now() % 60000) + 50)
}

// ---------------------------------------------------------------------------
// HTTP server
// ---------------------------------------------------------------------------
function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = []
    req.on('data', (c) => chunks.push(c))
    req.on('end', () => resolve(Buffer.concat(chunks)))
    req.on('error', reject)
  })
}

function decodeParams(header) {
  if (!header) return {}
  try {
    return JSON.parse(Buffer.from(String(header), 'base64').toString('utf8'))
  } catch {
    return {}
  }
}

export function createServer(functions) {
  const routeList = routes(functions)
  return http.createServer(async (req, res) => {
    if (req.url === '/__shipyard/routes') {
      res.writeHead(200, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ functions: [...functions.keys()], routes: routeList }))
      return
    }
    const name = String(req.headers['x-shipyard-function'] || '')
    const viaPath = req.headers['x-shipyard-route'] === 'path'
    const fn = functions.get(name)
    // Scheduled functions can't be invoked over HTTP, and functions with a
    // custom path aren't reachable on /.netlify/functions/<name> (as on Netlify).
    if (!fn || fn.schedule || (fn.kind === 'v2' && fn.config.path && !viaPath)) {
      res.writeHead(404, { 'content-type': 'text/plain; charset=utf-8' })
      res.end('Function not found')
      return
    }
    try {
      const proto = String(req.headers['x-forwarded-proto'] || 'http').split(',')[0]
      const host = req.headers['x-forwarded-host'] || req.headers.host || 'localhost'
      const inc = {
        method: req.method,
        url: new URL(req.url, `${proto}://${host}`),
        path: req.headers['x-shipyard-path'] ? String(req.headers['x-shipyard-path']) : undefined,
        headers: req.headers,
        body: await readBody(req),
        ip: String(req.headers['x-nf-client-connection-ip'] || req.socket.remoteAddress || ''),
        params: decodeParams(req.headers['x-shipyard-params']),
      }
      if (fn.background) {
        res.writeHead(202)
        res.end()
        invoke(fn, inc, BACKGROUND_TIMEOUT_MS).catch((err) => console.error(`[functions] ${fn.name}:`, err))
        return
      }
      await writeResult(res, await invoke(fn, inc))
    } catch (err) {
      console.error(`[functions] ${name}:`, err)
      if (!res.headersSent) {
        res.writeHead(err && err.name === 'TimeoutError' ? 504 : 500, { 'content-type': 'application/json' })
        res.end(JSON.stringify({ errorType: (err && err.name) || 'Error', errorMessage: (err && err.message) || String(err) }))
      } else {
        res.end()
      }
    }
  })
}

async function main() {
  const dir = path.dirname(fileURLToPath(import.meta.url))
  const port = Number(process.env.SHIPYARD_FUNCTIONS_PORT || 9999)
  const functions = await loadFunctions(dir)
  createServer(functions).listen(port, '127.0.0.1', () => {
    console.log(`[functions] ${functions.size} function(s) ready: ${[...functions.keys()].join(', ')}`)
  })
  startScheduler(functions, port)
}

installNetlifyGlobal()
const isMain = () => {
  try {
    return !!process.argv[1] && realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url))
  } catch {
    return false
  }
}
if (isMain()) {
  main().catch((err) => {
    console.error('[functions] runtime failed to start:', err)
    process.exit(1)
  })
}
