// Public URLs of a project, guessed from the labels reverse proxies read:
// caddy-docker-proxy (`caddy`, `caddy_0`… with `handle_path` directives) and
// Traefik (`traefik.http.routers.<name>.rule`). Netlify projects also get the
// path they're mounted on, since a site is often served under a prefix.

/** Turns "/docs/*" into "/docs/"; "" or "/*" into "/". */
function cleanPath(p: string): string {
  let path = p.trim().replace(/\*+$/, '')
  if (!path.startsWith('/')) path = '/' + path
  return path
}

/** A caddy site address ("example.com", "http://example.com:8080") as a URL origin, or null when it isn't a concrete host. */
function caddyOrigin(addr: string): string | null {
  const a = addr.trim()
  // Wildcards, placeholders, bare ports and matchers aren't links.
  if (!a || /[*{}]/.test(a) || a.startsWith(':') || a.startsWith('@')) return null
  const withScheme = /^https?:\/\//.test(a) ? a : `https://${a}`
  try {
    const u = new URL(withScheme)
    return u.origin
  } catch {
    return null
  }
}

function caddyLinks(labels: Record<string, string>, withPath: boolean): string[] {
  const out: string[] = []
  // Sites: "caddy" and "caddy_<n>", each with their own directives.
  for (const [key, value] of Object.entries(labels)) {
    if (!/^caddy(_\d+)?$/.test(key)) continue
    const origins = value
      .split(/[\s,]+/)
      .map(caddyOrigin)
      .filter((o): o is string => o !== null)
    if (origins.length === 0) continue
    // Directives of this site: "<key>.handle_path" or "<key>.<n>_handle_path".
    const paths = withPath
      ? Object.entries(labels)
          .filter(([k]) => new RegExp(`^${key}\\.(\\d+_)?handle_path$`).test(k))
          .sort(([a], [b]) => a.localeCompare(b, undefined, { numeric: true }))
          .map(([, v]) => cleanPath(v.split(/\s+/)[0] ?? ''))
      : []
    for (const o of origins) {
      if (paths.length === 0) out.push(o + '/')
      for (const p of paths) out.push(o + p)
    }
  }
  return out
}

/** Arguments of a Traefik matcher, e.g. Host(`a.com`, `b.com`) → ["a.com", "b.com"]. */
function matcherArgs(rule: string, name: string): string[] {
  const out: string[] = []
  for (const m of rule.matchAll(new RegExp(`\\b${name}\\(([^)]*)\\)`, 'g'))) {
    for (const a of m[1].matchAll(/[`"']([^`"']+)[`"']/g)) out.push(a[1])
  }
  return out
}

function traefikLinks(labels: Record<string, string>, withPath: boolean): string[] {
  const out: string[] = []
  for (const [key, rule] of Object.entries(labels)) {
    const router = key.match(/^traefik\.http\.routers\.([^.]+)\.rule$/)?.[1]
    if (!router) continue
    const entrypoints = (labels[`traefik.http.routers.${router}.entrypoints`] ?? '').split(',').map((s) => s.trim())
    const tls = labels[`traefik.http.routers.${router}.tls`] === 'true' || !!labels[`traefik.http.routers.${router}.tls.certresolver`]
    // Plain HTTP only when the router clearly isn't on HTTPS.
    const scheme = !tls && entrypoints.length > 0 && entrypoints.every((e) => e === 'web' || e === 'http') ? 'http' : 'https'
    const paths = withPath ? [...matcherArgs(rule, 'PathPrefix'), ...matcherArgs(rule, 'Path')].map(cleanPath) : []
    for (const host of matcherArgs(rule, 'Host')) {
      if (/[*{}]/.test(host)) continue
      if (paths.length === 0) out.push(`${scheme}://${host}/`)
      for (const p of paths) out.push(`${scheme}://${host}${p}`)
    }
  }
  return out
}

/** Public URLs found in a project's labels, deduplicated, in label order. */
export function projectLinks(labels: Record<string, string> | undefined, buildType: string): string[] {
  if (!labels) return []
  const withPath = buildType === 'netlify'
  return [...new Set([...caddyLinks(labels, withPath), ...traefikLinks(labels, withPath)])]
}
