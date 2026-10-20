// Run with: node --test internal/netlify/functions/runtime.test.mjs
import test from 'node:test'
import assert from 'node:assert/strict'
import { parseCron, cronMatches, nextRun, invoke, routes } from './runtime.mjs'

const req = (over = {}) => ({
  method: 'GET',
  url: new URL('https://site.test/api/items/42?x=1&x=2'),
  headers: { host: 'site.test', 'content-type': 'application/json', cookie: 'a=1; b=two' },
  body: Buffer.alloc(0),
  ip: '203.0.113.9',
  params: { id: '42' },
  ...over,
})

test('cron: macros, lists, ranges, steps, names', () => {
  const at = (s) => new Date(s)
  assert.ok(cronMatches(parseCron('@hourly'), at('2026-01-01T05:00:00Z')))
  assert.ok(!cronMatches(parseCron('@hourly'), at('2026-01-01T05:01:00Z')))
  const c = parseCron('*/15 9-17 * * mon-fri')
  assert.ok(cronMatches(c, at('2026-10-14T09:45:00Z'))) // Wednesday
  assert.ok(!cronMatches(c, at('2026-10-18T09:45:00Z'))) // Sunday
  assert.ok(!cronMatches(c, at('2026-10-14T18:00:00Z')))
  assert.equal(nextRun(parseCron('@daily'), at('2026-10-14T10:20:00Z')).toISOString(), '2026-10-15T00:00:00.000Z')
  assert.ok(cronMatches(parseCron('0 0 * * 7'), at('2026-10-18T00:00:00Z'))) // 7 = Sunday
  assert.throws(() => parseCron('* * *'))
  assert.throws(() => parseCron('61 * * * *'))
})

test('v1 handler: event shape and response', async () => {
  let event
  const fn = {
    name: 'hello',
    kind: 'v1',
    handler: async (e) => {
      event = e
      return { statusCode: 201, headers: { 'x-a': '1' }, multiValueHeaders: { 'set-cookie': ['a=1', 'b=2'] }, body: 'ok' }
    },
  }
  const r = await invoke(fn, req({ method: 'POST', body: Buffer.from('{"a":1}'), path: '/.netlify/functions/hello/sub?q=1' }))
  assert.equal(r.status, 201)
  assert.equal(r.body.toString(), 'ok')
  assert.deepEqual(r.headers.filter(([k]) => k === 'set-cookie').map(([, v]) => v), ['a=1', 'b=2'])
  assert.equal(event.path, '/.netlify/functions/hello/sub')
  assert.equal(event.httpMethod, 'POST')
  assert.equal(event.body, '{"a":1}')
  assert.equal(event.isBase64Encoded, false)
  assert.deepEqual(event.queryStringParameters, { q: '1' })
})

test('v1 handler: callback style, binary bodies, base64 responses', async () => {
  let event
  const fn = {
    name: 'cb',
    kind: 'v1',
    handler: (e, ctx, cb) => {
      event = e
      setTimeout(() => cb(null, { statusCode: 200, body: Buffer.from('png').toString('base64'), isBase64Encoded: true }), 5)
    },
  }
  const r = await invoke(fn, req({ method: 'POST', headers: { 'content-type': 'image/png' }, body: Buffer.from([1, 2, 3]) }))
  assert.equal(r.body.toString(), 'png')
  assert.equal(event.isBase64Encoded, true)
  assert.equal(event.body, Buffer.from([1, 2, 3]).toString('base64'))
  assert.deepEqual(event.multiValueQueryStringParameters, { x: ['1', '2'] })
})

test('v1 handler: Netlify Blobs context for connectLambda()', async () => {
  let event
  const fn = { name: 'b', kind: 'v1', handler: async (e) => ((event = e), { statusCode: 200 }) }
  const ctx = { edgeURL: 'http://127.0.0.1:1', siteID: 'shipyard', token: 't', deployID: 'd1', primaryRegion: 'local' }
  process.env.NETLIFY_BLOBS_CONTEXT = Buffer.from(JSON.stringify(ctx)).toString('base64')
  try {
    await invoke(fn, req())
  } finally {
    delete process.env.NETLIFY_BLOBS_CONTEXT
  }
  assert.deepEqual(JSON.parse(Buffer.from(event.blobs, 'base64').toString()), { url: ctx.edgeURL, token: 't', primaryRegion: 'local' })
  assert.equal(event.headers['x-nf-site-id'], 'shipyard')
  assert.equal(event.headers['x-nf-deploy-id'], 'd1')

  await invoke(fn, req())
  assert.equal(event.blobs, undefined)
})

test('v1 handler returning nothing is an error', async () => {
  await assert.rejects(invoke({ name: 'x', kind: 'v1', handler: async () => {} }, req()), /instead of a response object/)
})

test('v2 handler: Request, context, cookies, Netlify global', async () => {
  process.env.MY_VAR = 'from-env'
  const fn = {
    name: 'item',
    kind: 'v2',
    config: { path: '/api/items/:id' },
    handler: async (request, context) => {
      context.cookies.set({ name: 'seen', value: 'yes', httpOnly: true })
      return Response.json({
        url: request.url,
        id: context.params.id,
        ip: context.ip,
        cookie: context.cookies.get('b'),
        env: Netlify.env.get('MY_VAR'),
        same: Netlify.context === context,
      })
    },
  }
  const r = await invoke(fn, req())
  const chunks = []
  const reader = r.body.getReader()
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    chunks.push(value)
  }
  const data = JSON.parse(Buffer.concat(chunks).toString())
  assert.deepEqual(data, { url: 'https://site.test/api/items/42?x=1&x=2', id: '42', ip: '203.0.113.9', cookie: 'two', env: 'from-env', same: true })
  assert.ok(r.headers.some(([k, v]) => k === 'set-cookie' && v.startsWith('seen=yes') && v.includes('HttpOnly')))
  assert.deepEqual(routes(new Map([['item', fn]])), [{ name: 'item', path: ['/api/items/:id'], excludedPath: [], method: [], preferStatic: false }])
})

test('timeouts', async () => {
  const fn = { name: 'slow', kind: 'v2', config: {}, handler: () => new Promise(() => {}) }
  await assert.rejects(invoke(fn, req(), 50), /timed out/)
})
