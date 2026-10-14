import clsx from 'clsx'
import { ArrowLeft, ArrowDownToLine, RotateCcw, XCircle } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { api, isActive } from '@/lib/api'
import { duration, useAsync } from '@/lib/hooks'
import { Link, navigate } from '@/lib/router'
import { Button, ErrorBox, PageHeader, Spinner, StatusBadge } from '@/components/ui'

/** Streams the build log over SSE; EventSource reconnects on its own if dropped. */
function useLogStream(id: string) {
  const [lines, setLines] = useState<string[]>([])
  const [ended, setEnded] = useState(false)

  useEffect(() => {
    setLines([])
    setEnded(false)
    const es = new EventSource(`/api/deployments/${id}/logs`)
    let buffer: string[] = []
    let first = true
    // The server replays the full history on every (re)connection.
    es.onopen = () => {
      first = true
    }
    const flush = setInterval(() => {
      if (buffer.length === 0) return
      const chunk = buffer
      buffer = []
      setLines((prev) => [...prev, ...chunk])
    }, 100)
    es.addEventListener('line', (e) => {
      if (first) {
        first = false
        buffer = []
        setLines([])
      }
      buffer.push(JSON.parse((e as MessageEvent).data))
    })
    es.addEventListener('end', () => {
      setEnded(true)
      es.close()
    })
    return () => {
      clearInterval(flush)
      es.close()
    }
  }, [id])

  return { lines, ended }
}

function lineClass(l: string) {
  if (l.startsWith('==> Deployment succeeded')) return 'text-emerald-400 font-semibold'
  if (l.startsWith('==> Deployment failed') || l.startsWith('==> Deployment cancelled')) return 'text-red-400 font-semibold'
  if (l.startsWith('==>')) return 'text-sky-300 font-semibold'
  if (l.startsWith('  │')) return 'text-zinc-400'
  if (/\berror\b|ERROR|failed/i.test(l)) return 'text-red-300'
  return ''
}

export function DeploymentPage({ id }: { id: string }) {
  const dep = useAsync(() => api.deployment(id), [id], 2000)
  const d = dep.data
  const project = useAsync(() => (d ? api.project(d.projectId) : Promise.resolve(undefined)), [d?.projectId])
  const { lines, ended } = useLogStream(id)
  const [follow, setFollow] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const box = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (follow && box.current) box.current.scrollTop = box.current.scrollHeight
  }, [lines, follow])

  if (dep.error) return <ErrorBox>{dep.error}</ErrorBox>
  if (!d) return <Spinner />

  const run = async (fn: () => Promise<void>) => {
    setBusy(true)
    setError('')
    try {
      await fn()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <Link href={`/projects/${d.projectId}`} className="mb-3 inline-flex items-center gap-1 text-sm text-muted hover:text-fg">
        <ArrowLeft className="size-4" /> {project.data?.name ?? 'Project'}
      </Link>
      <PageHeader
        title={
          <span className="flex items-center gap-3">
            Deployment <span className="font-mono text-base text-muted">{d.id}</span>
            <StatusBadge status={d.status} />
          </span>
        }
        subtitle={
          <span className="flex flex-wrap gap-x-4 gap-y-1">
            {d.commitSha && (
              <span>
                <span className="font-mono">{d.commitSha.slice(0, 10)}</span> {d.commitMessage}
              </span>
            )}
            <span>trigger: {d.trigger}</span>
            <span>duration: {duration(d.startedAt, d.finishedAt)}</span>
            {d.image && <span className="font-mono">{d.image}</span>}
          </span>
        }
        actions={
          isActive(d.status) ? (
            <Button variant="danger" icon={<XCircle className="size-4" />} loading={busy} onClick={() => run(async () => void (await api.cancelDeployment(id)))}>
              Cancel
            </Button>
          ) : d.status === 'succeeded' ? (
            <Button
              icon={<RotateCcw className="size-4" />}
              loading={busy}
              onClick={() =>
                run(async () => {
                  const n = await api.redeploy(id)
                  navigate(`/deployments/${n.id}`)
                })
              }
            >
              Redeploy this image
            </Button>
          ) : null
        }
      />
      <div className="mb-3 space-y-2">
        <ErrorBox>{error}</ErrorBox>
        {d.status === 'failed' && d.error && <ErrorBox>{d.error}</ErrorBox>}
      </div>
      <div className="overflow-hidden rounded-xl border border-border bg-term">
        <div className="flex items-center justify-between border-b border-white/10 px-3 py-2 text-xs text-zinc-400">
          <span>{ended ? `${lines.length} lines` : 'Streaming…'}</span>
          <button onClick={() => setFollow((f) => !f)} className={clsx('inline-flex items-center gap-1 rounded px-1.5 py-0.5', follow ? 'bg-white/10 text-zinc-200' : 'hover:text-zinc-200')}>
            <ArrowDownToLine className="size-3.5" /> Follow
          </button>
        </div>
        <div
          ref={box}
          onWheel={(e) => e.deltaY < 0 && setFollow(false)}
          className="h-[65vh] overflow-auto p-3 font-mono text-xs leading-relaxed text-term-fg"
        >
          {lines.map((l, i) => (
            <div key={i} className={clsx('whitespace-pre-wrap break-all', lineClass(l))}>
              {l || ' '}
            </div>
          ))}
          {!ended && lines.length === 0 && <div className="text-zinc-500">Waiting for output…</div>}
        </div>
      </div>
    </>
  )
}
