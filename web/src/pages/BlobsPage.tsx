import clsx from 'clsx'
import { ArrowLeft, ChevronRight, Database, Download, File, Folder, RefreshCw, Search, Trash2 } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { api, type Blob as BlobEntry, type BlobStore } from '@/lib/api'
import { timeAgo, useAsync } from '@/lib/hooks'
import { Link } from '@/lib/router'
import { Button, Card, Empty, ErrorBox, Input, PageHeader, Spinner } from '@/components/ui'

/** Largest blob previewed inline; bigger ones are download-only. */
const PREVIEW_MAX = 1 << 20

// @netlify/blobs prefixes store names: "site:<name>" for getStore, "deploy:<id>" for getDeployStore.
function storeLabel(name: string): { label: string; deploy?: string } {
  if (name.startsWith('site:')) return { label: name.slice(5) }
  if (name.startsWith('deploy:')) return { label: `Deploy ${name.slice(7, 15)}`, deploy: name.slice(7) }
  return { label: name }
}

function formatSize(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1 << 20) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / (1 << 20)).toFixed(1)} MB`
}

export function BlobsPage({ id }: { id: string }) {
  const project = useAsync(() => api.project(id), [id])
  const blobs = useAsync(() => api.blobs(id), [id])
  const [storeName, setStoreName] = useState('')
  const [prefix, setPrefix] = useState('')
  const [query, setQuery] = useState('')
  const [selected, setSelected] = useState('')
  const [busy, setBusy] = useState('')
  const [actionError, setActionError] = useState('')

  const stores = blobs.data?.stores ?? []
  const store = stores.find((s) => s.name === storeName) ?? stores[0]

  // Reset the view when the store changes or disappears.
  useEffect(() => {
    setPrefix('')
    setSelected('')
    setQuery('')
  }, [store?.name])

  const act = async (name: string, fn: () => Promise<unknown>) => {
    setBusy(name)
    setActionError('')
    try {
      await fn()
    } catch (e) {
      setActionError((e as Error).message)
    } finally {
      setBusy('')
    }
  }

  const deleteBlob = (key: string) =>
    store &&
    confirm(`Delete blob "${key}"? This can't be undone.`) &&
    act('delete:' + key, async () => {
      await api.deleteBlob(id, store.name, key)
      setSelected('')
      await blobs.reload()
    })

  const deleteStore = () =>
    store &&
    confirm(`Delete store "${storeLabel(store.name).label}" and its ${store.blobs.length} blob(s)? This can't be undone.`) &&
    act('delete-store', async () => {
      await api.deleteBlob(id, store.name)
      setStoreName('')
      await blobs.reload()
    })

  return (
    <>
      <PageHeader
        title={
          <span className="inline-flex items-center gap-2">
            <Database className="size-5 text-muted" /> Blobs
          </span>
        }
        subtitle={
          <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <Link href={`/projects/${id}`} className="inline-flex items-center gap-1 hover:text-fg">
              <ArrowLeft className="size-3.5" /> {project.data?.name ?? 'Project'}
            </Link>
            {blobs.data && <span className="font-mono text-xs">volume {blobs.data.volume}</span>}
          </span>
        }
        actions={
          <Button icon={<RefreshCw className="size-4" />} loading={blobs.loading} onClick={blobs.reload}>
            Refresh
          </Button>
        }
      />
      <div className="mb-4">
        <ErrorBox>{actionError}</ErrorBox>
      </div>

      {blobs.error ? (
        <ErrorBox>{blobs.error}</ErrorBox>
      ) : !blobs.data ? (
        <Spinner />
      ) : !blobs.data.enabled ? (
        <Empty icon={<Database className="size-8" />} title="No blobs to browse">
          {blobs.data.reason}
        </Empty>
      ) : stores.length === 0 || !store ? (
        <Empty icon={<Database className="size-8" />} title="No stores yet">
          Stores appear here once a function writes to one with <span className="font-mono">getStore()</span> or{' '}
          <span className="font-mono">getDeployStore()</span>.
        </Empty>
      ) : (
        <div className="grid gap-5 lg:grid-cols-[240px_1fr]">
          <StoreList stores={stores} current={store.name} onSelect={setStoreName} />
          <div className="min-w-0 space-y-5">
            <Card
              title={<StoreTitle store={store} />}
              actions={
                <Button size="sm" variant="danger" icon={<Trash2 className="size-3.5" />} loading={busy === 'delete-store'} onClick={deleteStore}>
                  Delete store
                </Button>
              }
            >
              <BlobBrowser
                store={store}
                prefix={prefix}
                query={query}
                selected={selected}
                onPrefix={(p) => {
                  setPrefix(p)
                  setSelected('')
                }}
                onQuery={setQuery}
                onSelect={setSelected}
              />
            </Card>
            {selected && store.blobs.some((b) => b.key === selected) && (
              <BlobDetail
                key={store.name + '\n' + selected}
                projectId={id}
                store={store.name}
                blob={store.blobs.find((b) => b.key === selected)!}
                deleting={busy === 'delete:' + selected}
                onDelete={() => deleteBlob(selected)}
              />
            )}
          </div>
        </div>
      )}
      {blobs.data?.enabled && (
        <p className="mt-6 text-xs text-muted">
          Read from the blobs volume on Shipyard's Docker host. On Swarm, replicas running on other nodes have their own copy.
        </p>
      )}
    </>
  )
}

function StoreTitle({ store }: { store: BlobStore }) {
  const { label, deploy } = storeLabel(store.name)
  return (
    <span className="flex min-w-0 items-center gap-2">
      <span className="truncate">{label}</span>
      {deploy ? (
        <Link href={`/deployments/${deploy}`} className="rounded bg-violet-500/10 px-1.5 py-px text-xs font-medium text-violet-600 hover:underline dark:text-violet-400">
          deploy store
        </Link>
      ) : (
        <span className="rounded bg-subtle px-1.5 py-px text-xs font-medium text-muted">site store</span>
      )}
    </span>
  )
}

function StoreList({ stores, current, onSelect }: { stores: BlobStore[]; current: string; onSelect: (name: string) => void }) {
  return (
    <nav className="h-fit rounded-xl border border-border bg-panel p-2">
      <p className="px-2 pt-1 pb-2 text-xs font-semibold tracking-wide text-muted uppercase">Stores</p>
      <ul className="space-y-0.5">
        {stores.map((s) => {
          const { label, deploy } = storeLabel(s.name)
          return (
            <li key={s.name}>
              <button
                onClick={() => onSelect(s.name)}
                title={s.name}
                className={clsx(
                  'flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-left text-sm',
                  s.name === current ? 'bg-subtle font-medium' : 'text-muted hover:bg-subtle hover:text-fg',
                )}
              >
                <Database className={clsx('size-3.5 shrink-0', deploy && 'text-violet-500')} />
                <span className="min-w-0 flex-1 truncate">{label}</span>
                <span className="font-mono text-xs text-muted">{s.blobs.length}</span>
              </button>
            </li>
          )
        })}
      </ul>
    </nav>
  )
}

function BlobBrowser({
  store,
  prefix,
  query,
  selected,
  onPrefix,
  onQuery,
  onSelect,
}: {
  store: BlobStore
  prefix: string
  query: string
  selected: string
  onPrefix: (p: string) => void
  onQuery: (q: string) => void
  onSelect: (key: string) => void
}) {
  // With a search, list matching keys flat; otherwise browse "/"-separated folders like Netlify's UI.
  const { dirs, files } = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (q) return { dirs: [], files: store.blobs.filter((b) => b.key.toLowerCase().includes(q)) }
    const dirs = new Map<string, number>()
    const files: BlobEntry[] = []
    for (const b of store.blobs) {
      if (!b.key.startsWith(prefix)) continue
      const rest = b.key.slice(prefix.length)
      const i = rest.indexOf('/')
      if (i >= 0) {
        const d = rest.slice(0, i + 1)
        dirs.set(d, (dirs.get(d) ?? 0) + 1)
      } else files.push(b)
    }
    return { dirs: [...dirs].sort(([a], [b]) => a.localeCompare(b)), files }
  }, [store, prefix, query])

  const crumbs = prefix.split('/').filter(Boolean)
  const total = store.blobs.reduce((n, b) => n + b.size, 0)

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-3">
        <div className="relative min-w-48 flex-1">
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted" />
          <Input className="pl-8" placeholder="Search keys" value={query} onChange={(e) => onQuery(e.target.value)} />
        </div>
        <span className="text-xs text-muted">
          {store.blobs.length} blob{store.blobs.length === 1 ? '' : 's'} · {formatSize(total)}
        </span>
      </div>

      {!query.trim() && (
        <div className="flex flex-wrap items-center gap-1 font-mono text-xs">
          <button className={clsx('hover:text-accent', crumbs.length === 0 ? 'font-semibold' : 'text-muted')} onClick={() => onPrefix('')}>
            /
          </button>
          {crumbs.map((c, i) => (
            <span key={i} className="inline-flex items-center gap-1">
              <ChevronRight className="size-3 text-muted" />
              <button
                className={clsx('hover:text-accent', i === crumbs.length - 1 ? 'font-semibold' : 'text-muted')}
                onClick={() => onPrefix(crumbs.slice(0, i + 1).join('/') + '/')}
              >
                {c}
              </button>
            </span>
          ))}
        </div>
      )}

      {dirs.length === 0 && files.length === 0 ? (
        <p className="py-6 text-center text-sm text-muted">{query.trim() ? 'No key matches.' : 'This store is empty.'}</p>
      ) : (
        <ul className="divide-y divide-border rounded-lg border border-border">
          {dirs.map(([d, n]) => (
            <li key={d}>
              <button className="flex w-full items-center gap-2.5 px-3 py-2 text-left text-sm hover:bg-subtle" onClick={() => onPrefix(prefix + d)}>
                <Folder className="size-4 shrink-0 text-amber-500" />
                <span className="min-w-0 flex-1 truncate font-mono text-xs">{d}</span>
                <span className="text-xs text-muted">
                  {n} blob{n === 1 ? '' : 's'}
                </span>
              </button>
            </li>
          ))}
          {files.map((b) => (
            <li key={b.key}>
              <button
                className={clsx('flex w-full items-center gap-2.5 px-3 py-2 text-left text-sm', b.key === selected ? 'bg-accent/10' : 'hover:bg-subtle')}
                onClick={() => onSelect(b.key === selected ? '' : b.key)}
              >
                <File className="size-4 shrink-0 text-muted" />
                <span className="min-w-0 flex-1 truncate font-mono text-xs" title={b.key}>
                  {query.trim() ? b.key : b.key.slice(prefix.length)}
                </span>
                <span className="w-20 text-right font-mono text-xs text-muted">{formatSize(b.size)}</span>
                <span className="hidden w-20 text-right text-xs text-muted sm:inline" title={new Date(b.modified).toLocaleString()}>
                  {timeAgo(b.modified)}
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

type Preview = { kind: 'text'; text: string; json: boolean } | { kind: 'image'; url: string } | { kind: 'binary'; hex: string }

async function loadPreview(url: string): Promise<Preview> {
  const res = await fetch(url, { credentials: 'same-origin' })
  if (!res.ok) {
    const body = await res.json().catch(() => ({}))
    throw new Error(body.error ?? res.statusText)
  }
  const type = res.headers.get('Content-Type') ?? ''
  const buf = await res.arrayBuffer()
  if (type.startsWith('image/')) return { kind: 'image', url: URL.createObjectURL(new Blob([buf], { type })) }
  try {
    const text = new TextDecoder('utf-8', { fatal: true }).decode(buf)
    try {
      return { kind: 'text', text: JSON.stringify(JSON.parse(text), null, 2), json: true }
    } catch {
      return { kind: 'text', text, json: false }
    }
  } catch {
    const bytes = [...new Uint8Array(buf.slice(0, 512))]
    const lines: string[] = []
    for (let i = 0; i < bytes.length; i += 16) {
      const row = bytes.slice(i, i + 16)
      const hex = row.map((b) => b.toString(16).padStart(2, '0')).join(' ')
      const ascii = row.map((b) => (b >= 32 && b < 127 ? String.fromCharCode(b) : '.')).join('')
      lines.push(`${i.toString(16).padStart(8, '0')}  ${hex.padEnd(47)}  ${ascii}`)
    }
    return { kind: 'binary', hex: lines.join('\n') + (buf.byteLength > 512 ? '\n…' : '') }
  }
}

function BlobDetail({ projectId, store, blob, deleting, onDelete }: { projectId: string; store: string; blob: BlobEntry; deleting: boolean; onDelete: () => void }) {
  const url = api.blobUrl(projectId, store, blob.key)
  const previewable = blob.size <= PREVIEW_MAX
  const preview = useAsync(() => (previewable ? loadPreview(url) : Promise.resolve(undefined)), [url, blob.etag])

  useEffect(() => {
    const p = preview.data
    return () => {
      if (p?.kind === 'image') URL.revokeObjectURL(p.url)
    }
  }, [preview.data])

  return (
    <Card
      title={<span className="font-mono text-xs break-all">{blob.key}</span>}
      actions={
        <>
          <a href={url} download className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-border bg-panel px-2.5 text-xs font-medium hover:bg-subtle">
            <Download className="size-3.5" /> Download
          </a>
          <Button size="sm" variant="danger" icon={<Trash2 className="size-3.5" />} loading={deleting} onClick={onDelete}>
            Delete
          </Button>
        </>
      }
    >
      <dl className="mb-4 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-xs">
        <dt className="text-muted">Size</dt>
        <dd className="font-mono">
          {formatSize(blob.size)} {blob.size >= 1024 && <span className="text-muted">({blob.size.toLocaleString()} bytes)</span>}
        </dd>
        <dt className="text-muted">Last modified</dt>
        <dd>
          {new Date(blob.modified).toLocaleString()} <span className="text-muted">· {timeAgo(blob.modified)}</span>
        </dd>
        <dt className="text-muted">ETag</dt>
        <dd className="truncate font-mono">{blob.etag}</dd>
      </dl>

      {blob.metadata && (
        <div className="mb-4">
          <h3 className="mb-1.5 text-xs font-semibold">Metadata</h3>
          <pre className="max-h-48 overflow-auto rounded-lg bg-subtle p-3 font-mono text-xs leading-relaxed">{JSON.stringify(blob.metadata, null, 2)}</pre>
        </div>
      )}

      <h3 className="mb-1.5 text-xs font-semibold">
        Content
        {preview.data?.kind === 'text' && preview.data.json && <span className="ml-2 font-normal text-muted">JSON</span>}
      </h3>
      {!previewable ? (
        <p className="text-sm text-muted">Too large to preview ({formatSize(blob.size)}). Download it instead.</p>
      ) : preview.error ? (
        <ErrorBox>{preview.error}</ErrorBox>
      ) : !preview.data ? (
        <Spinner />
      ) : preview.data.kind === 'image' ? (
        <img src={preview.data.url} alt={blob.key} className="max-h-96 rounded-lg border border-border bg-subtle object-contain" />
      ) : preview.data.kind === 'text' ? (
        blob.size === 0 ? (
          <p className="text-sm text-muted">Empty blob.</p>
        ) : (
          <pre className="max-h-[32rem] overflow-auto rounded-lg bg-term p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap text-term-fg">{preview.data.text}</pre>
        )
      ) : (
        <>
          <p className="mb-1.5 text-xs text-muted">Binary content (first 512 bytes):</p>
          <pre className="max-h-96 overflow-auto rounded-lg bg-term p-3 font-mono text-xs leading-relaxed text-term-fg">{preview.data.hex}</pre>
        </>
      )}
    </Card>
  )
}
