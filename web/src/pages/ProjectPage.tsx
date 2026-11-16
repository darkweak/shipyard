import { Copy, Database, ExternalLink, GitBranch, Globe, Lock, Play, RefreshCw, Rocket, RotateCcw, Settings, Square, Trash2, Webhook } from 'lucide-react'
import { useState } from 'react'
import { api, isActive, type ContainerState, type Deployment, type Project } from '@/lib/api'
import { duration, timeAgo, useAsync } from '@/lib/hooks'
import { projectLinks } from '@/lib/links'
import { Link, navigate } from '@/lib/router'
import { Button, Card, Dialog, Dot, ErrorBox, PageHeader, Spinner, StatusBadge } from '@/components/ui'
import { isLockedTo, nodeLock, runningNodes, toInput, withLock } from '@/lib/placement'
import { repoLabel } from './ProjectsPage'

export function ProjectPage({ id }: { id: string }) {
  const { data: project, error, setData: setProject } = useAsync(() => api.project(id), [id])
  const deployments = useAsync(() => api.deployments(id), [id], 3000)
  const container = useAsync(() => api.container(id), [id], 5000)
  const [busy, setBusy] = useState('')
  const [actionError, setActionError] = useState('')
  const [lockNode, setLockNode] = useState('')

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

  if (error) return <ErrorBox>{error}</ErrorBox>
  if (!project) return <Spinner />

  const deploying = deployments.data?.some((d) => isActive(d.status))
  const liveId = deployments.data?.find((d) => d.status === 'succeeded')?.id
  const links = projectLinks(project.labels, project.buildType)

  return (
    <>
      <PageHeader
        title={project.name}
        subtitle={
          <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <span className="font-mono">{repoLabel(project.repoUrl)}</span>
            <span className="inline-flex items-center gap-1">
              <GitBranch className="size-3.5" />
              {project.branch}
            </span>
            {links.map((url) => (
              <a key={url} href={url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-accent hover:underline">
                <Globe className="size-3.5" />
                {url.replace(/^https:\/\//, '').replace(/\/$/, '')}
              </a>
            ))}
          </span>
        }
        actions={
          <>
            <Button icon={<Settings className="size-4" />} onClick={() => navigate(`/projects/${id}/edit`)}>
              Settings
            </Button>
            <Button
              variant="primary"
              icon={<Rocket className="size-4" />}
              loading={busy === 'deploy'}
              onClick={() =>
                act('deploy', async () => {
                  const d = await api.deploy(id)
                  navigate(`/deployments/${d.id}`)
                })
              }
            >
              {deploying ? 'Queue deploy' : 'Deploy'}
            </Button>
          </>
        }
      />
      <div className="mb-4">
        <ErrorBox>{actionError}</ErrorBox>
      </div>

      <Dialog
        open={!!lockNode}
        title={`Lock to ${lockNode}?`}
        icon={<Lock className="size-4 text-muted" />}
        confirmLabel="Lock"
        onCancel={() => setLockNode('')}
        onConfirm={() => {
          const node = lockNode
          setLockNode('')
          act('lock', async () => setProject(await api.updateProject(id, { ...toInput(project), placement: withLock(project.placement, node) })))
        }}
      >
        <p>
          Adds <span className="font-mono text-fg">{nodeLock(lockNode)}</span> to the placement constraints (replacing any other node lock). It
          applies from the next deployment.
        </p>
        <p>
          The node must carry the <span className="font-mono">node_name={lockNode}</span> label, or the service can't be scheduled anywhere.
        </p>
      </Dialog>

      <div className="grid gap-5 lg:grid-cols-[1fr_340px]">
        <div className="min-w-0 space-y-5">
          <Card title="Deployments" actions={<span className="text-xs text-muted">last 50</span>}>
            <DeploymentList
              deployments={deployments.data}
              liveId={liveId}
              onRedeploy={(d) =>
                act('redeploy', async () => {
                  const n = await api.redeploy(d.id)
                  navigate(`/deployments/${n.id}`)
                })
              }
            />
          </Card>
          <RuntimeLogs projectId={id} />
        </div>

        <div className="space-y-5">
          <ContainerCard
            state={container.data}
            error={container.error}
            busy={busy}
            project={project}
            onAction={(a) => act(a, async () => container.setData(await api.containerAction(id, a)))}
            onLock={setLockNode}
          />
          {project.buildType === 'netlify' && (
            <Card
              title={
                <span className="inline-flex items-center gap-1.5">
                  <Database className="size-4" /> Netlify Blobs
                </span>
              }
            >
              <p className="mb-3 text-xs text-muted">
                Browse, download and delete what the site's functions stored with <span className="font-mono">@netlify/blobs</span>.
              </p>
              <Button size="sm" onClick={() => navigate(`/projects/${id}/blobs`)}>
                Browse blobs
              </Button>
            </Card>
          )}
          <WebhookCard project={project} onRotate={() => act('rotate', async () => setProject(await api.rotateWebhook(id)))} />
          <Card title="Danger zone">
            <p className="mb-3 text-sm text-muted">Deletes the project, its container and its images.</p>
            <Button
              variant="danger"
              icon={<Trash2 className="size-4" />}
              loading={busy === 'delete'}
              onClick={() => {
                if (!confirm(`Delete ${project.name}? Its container will be removed.`)) return
                act('delete', async () => {
                  await api.deleteProject(id)
                  navigate('/projects')
                })
              }}
            >
              Delete project
            </Button>
          </Card>
        </div>
      </div>
    </>
  )
}

function DeploymentList({ deployments, liveId, onRedeploy }: { deployments?: Deployment[]; liveId?: string; onRedeploy: (d: Deployment) => void }) {
  if (!deployments) return <Spinner />
  if (deployments.length === 0) return <p className="py-6 text-center text-sm text-muted">No deployments yet. Hit Deploy to build the first one.</p>
  return (
    <ul className="-my-2 divide-y divide-border">
      {deployments.map((d) => (
        <li key={d.id} className="flex flex-wrap items-center gap-x-4 gap-y-1 py-2.5">
          <div className="w-28 shrink-0">
            <StatusBadge status={d.status} />
          </div>
          <Link href={`/deployments/${d.id}`} className="min-w-0 flex-1 hover:text-accent">
            <p className="truncate text-sm">
              {d.commitSha && <span className="mr-2 font-mono text-xs text-muted">{d.commitSha.slice(0, 7)}</span>}
              {d.commitMessage || <span className="text-muted">{d.status === 'queued' ? 'Waiting…' : '—'}</span>}
            </p>
            <p className="truncate text-xs text-muted">
              {d.trigger}
              {d.id === liveId && <span className="ml-2 rounded bg-emerald-500/10 px-1.5 py-px font-medium text-emerald-600 dark:text-emerald-400">live</span>}
              {d.error && d.status === 'failed' && <span className="ml-2 text-red-500">{d.error}</span>}
            </p>
          </Link>
          <span className="w-16 text-right font-mono text-xs text-muted">{duration(d.startedAt, d.finishedAt)}</span>
          <span className="w-20 text-right text-xs text-muted" title={new Date(d.createdAt).toLocaleString()}>
            {timeAgo(d.createdAt)}
          </span>
          <div className="w-8">
            {d.status === 'succeeded' && d.id !== liveId && (
              <Button size="sm" variant="ghost" title="Redeploy this image" onClick={() => onRedeploy(d)}>
                <RotateCcw className="size-3.5" />
              </Button>
            )}
          </div>
        </li>
      ))}
    </ul>
  )
}

function ContainerCard({
  state,
  error,
  busy,
  project,
  onAction,
  onLock,
}: {
  state?: ContainerState
  error?: string
  busy: string
  project: Project
  onAction: (a: 'start' | 'stop' | 'restart') => void
  onLock: (node: string) => void
}) {
  const swarm = state?.mode === 'swarm'
  return (
    <Card title={swarm ? 'Swarm service' : 'Container'}>
      {error ? (
        <ErrorBox>{error}</ErrorBox>
      ) : !state ? (
        <Spinner />
      ) : !state.exists ? (
        <p className="text-sm text-muted">Not deployed yet.</p>
      ) : (
        <div className="space-y-3 text-sm">
          {swarm ? <SwarmSummary state={state} /> : <ContainerSummary state={state} project={project} />}
          {swarm && <NodeLock state={state} project={project} busy={busy === 'lock'} onLock={onLock} />}
          {project.network && (
            <p className="text-xs">
              <span className="text-muted">Network </span>
              <span className="font-mono">{project.network}</span>
            </p>
          )}
          <PortLinks state={state} />
          <div className="flex gap-2 pt-1">
            {state.running ? (
              <Button size="sm" icon={<Square className="size-3.5" />} loading={busy === 'stop'} onClick={() => onAction('stop')}>
                {swarm ? 'Scale to 0' : 'Stop'}
              </Button>
            ) : (
              <Button size="sm" icon={<Play className="size-3.5" />} loading={busy === 'start'} onClick={() => onAction('start')}>
                {swarm ? `Scale to ${Math.max(project.replicas, 1)}` : 'Start'}
              </Button>
            )}
            <Button size="sm" icon={<RefreshCw className="size-3.5" />} loading={busy === 'restart'} onClick={() => onAction('restart')}>
              {swarm ? 'Rolling restart' : 'Restart'}
            </Button>
          </div>
        </div>
      )}
    </Card>
  )
}

function ContainerSummary({ state, project }: { state: ContainerState; project: Project }) {
  const unhealthy = state.running && state.health === 'unhealthy'
  const tone = state.running && !state.restarting && !unhealthy ? 'green' : state.restarting || unhealthy ? 'amber' : 'red'
  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <Dot tone={tone} />
        <span className="font-medium">{state.restarting ? 'restarting' : state.state}</span>
        {state.running && state.health && (
          <span
            title={state.healthLog || undefined}
            className={
              state.health === 'healthy'
                ? 'text-emerald-600 dark:text-emerald-400'
                : state.health === 'unhealthy'
                  ? 'text-red-600 dark:text-red-400'
                  : 'text-muted'
            }
          >
            · {state.health}
          </span>
        )}
        {state.running && <span className="text-muted">· up since {timeAgo(state.startedAt)}</span>}
        {!state.running && state.exitCode !== 0 && <span className="text-muted">· exit {state.exitCode}</span>}
      </div>
      {unhealthy && state.healthLog && <p className="truncate font-mono text-xs text-red-600 dark:text-red-400">{state.healthLog}</p>}
      <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
        <dt className="text-muted">Name</dt>
        <dd className="truncate font-mono">shipyard-{project.slug}</dd>
        <dt className="text-muted">Image</dt>
        <dd className="truncate font-mono" title={state.image}>
          {state.image}
        </dd>
      </dl>
    </>
  )
}

function SwarmSummary({ state }: { state: ContainerState }) {
  const svc = state.service!
  const running = state.runningTasks ?? 0
  const updating = svc.updateState === 'updating' || svc.updateState?.startsWith('rollback_started')
  const tone = running >= svc.replicas && svc.replicas > 0 ? 'green' : running > 0 || updating ? 'amber' : svc.replicas === 0 ? 'gray' : 'red'
  const active = (state.tasks ?? []).filter((t) => t.desiredState === 'Running')
  const recentFailures = (state.tasks ?? []).filter((t) => t.desiredState !== 'Running' && t.error).slice(0, 2)
  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <Dot tone={tone} />
        <span className="font-medium">
          {running}/{svc.replicas} replica{svc.replicas === 1 ? '' : 's'} running
        </span>
        {svc.updateState && svc.updateState !== 'completed' && <span className="text-muted">· update {svc.updateState.replace('_', ' ')}</span>}
      </div>
      {svc.updateMessage && svc.updateState !== 'completed' && <p className="text-xs text-muted">{svc.updateMessage}</p>}
      <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
        <dt className="text-muted">Service</dt>
        <dd className="truncate font-mono">{svc.name}</dd>
        <dt className="text-muted">Image</dt>
        <dd className="truncate font-mono" title={svc.image}>
          {svc.image.split('@')[0]}
        </dd>
      </dl>
      {active.length > 0 && (
        <ul className="divide-y divide-border rounded-lg border border-border text-xs">
          {active.map((t) => (
            <li key={t.id} className="flex items-center gap-2 px-2.5 py-1.5">
              <Dot tone={t.currentState.startsWith('Running') ? 'green' : 'amber'} />
              <span className="font-mono">{t.name.replace(/^.*_app/, 'app')}</span>
              <span className="truncate text-muted">{t.node}</span>
              <span className="ml-auto shrink-0 text-muted">{t.currentState.split(' ')[0].toLowerCase()}</span>
            </li>
          ))}
        </ul>
      )}
      {recentFailures.map((t) => (
        <p key={t.id} className="text-xs text-red-600 dark:text-red-400">
          {t.node}: {t.error}
        </p>
      ))}
    </>
  )
}

/** Shows the node lock, or offers to lock to the node(s) running the service. */
function NodeLock({ state, project, busy, onLock }: { state: ContainerState; project: Project; busy: boolean; onLock: (node: string) => void }) {
  const nodes = runningNodes(state)
  const locked = project.placement.find((c) => /^\s*node\.labels\.node_name\s*==/.test(c))
  const lockedNode = locked?.split('==')[1]?.trim()
  const pending = lockedNode && nodes.length > 0 && !nodes.includes(lockedNode)
  return (
    <div className="flex flex-wrap items-center gap-2 text-xs">
      {lockedNode && (
        <span className="inline-flex items-center gap-1 text-muted" title={locked}>
          <Lock className="size-3.5" /> Locked to <span className="font-mono text-fg">{lockedNode}</span>
          {pending && ' · applies on next deploy'}
        </span>
      )}
      {nodes
        .filter((n) => !isLockedTo(project.placement, n))
        .map((n) => (
          <Button key={n} size="sm" icon={<Lock className="size-3.5" />} loading={busy} onClick={() => onLock(n)}>
            Lock to {n}
          </Button>
        ))}
    </div>
  )
}

function PortLinks({ state }: { state: ContainerState }) {
  const ports =
    state.mode === 'swarm'
      ? (state.service?.ports ?? []).filter((p) => p.publishedPort).map((p) => ({ host: String(p.publishedPort), container: String(p.targetPort) }))
      : state.ports.filter((p) => !p.hostIp.includes(':')).map((p) => ({ host: p.hostPort, container: p.containerPort }))
  if (ports.length === 0) return null
  return (
    <div className="space-y-1">
      {ports.map((p) => (
        <a
          key={p.host + p.container}
          href={`http://${location.hostname}:${p.host}`}
          target="_blank"
          rel="noreferrer"
          className="flex items-center gap-1.5 font-mono text-xs text-accent hover:underline"
        >
          <ExternalLink className="size-3.5" />
          {location.hostname}:{p.host} → {p.container}
        </a>
      ))}
    </div>
  )
}

function WebhookCard({ project, onRotate }: { project: Project; onRotate: () => void }) {
  const url = `${location.origin}/hooks/${project.id}/${project.webhookSecret}`
  const [copied, setCopied] = useState(false)
  return (
    <Card
      title={
        <span className="inline-flex items-center gap-1.5">
          <Webhook className="size-4" /> Deploy on push
        </span>
      }
    >
      <p className="mb-2 text-xs text-muted">
        Add this URL as a push webhook (GitHub, GitLab, Gitea, Bitbucket). Pushes to <span className="font-mono">{project.branch}</span> trigger a deployment.
      </p>
      <div className="flex gap-2">
        <input readOnly value={url} onFocus={(e) => e.target.select()} className="h-8 min-w-0 flex-1 rounded-lg border border-border bg-subtle px-2 font-mono text-xs" />
        <Button
          size="sm"
          title="Copy"
          onClick={() => {
            navigator.clipboard?.writeText(url)
            setCopied(true)
            setTimeout(() => setCopied(false), 1500)
          }}
        >
          <Copy className="size-3.5" />
          {copied ? 'Copied' : ''}
        </Button>
      </div>
      <button
        className="mt-2 text-xs text-muted underline-offset-2 hover:text-fg hover:underline"
        onClick={() => confirm('Generate a new webhook URL? The current one stops working.') && onRotate()}
      >
        Regenerate secret
      </button>
    </Card>
  )
}

function RuntimeLogs({ projectId }: { projectId: string }) {
  const [open, setOpen] = useState(false)
  const logs = useAsync(() => (open ? api.containerLogs(projectId, 500) : Promise.resolve(undefined)), [projectId, open])
  return (
    <Card
      title={'Logs'}
      actions={
        open ? (
          <Button size="sm" variant="ghost" icon={<RefreshCw className="size-3.5" />} onClick={logs.reload}>
            Refresh
          </Button>
        ) : (
          <Button size="sm" onClick={() => setOpen(true)}>
            Show
          </Button>
        )
      }
    >
      {!open ? (
        <p className="text-sm text-muted">Last 500 lines of the running container's output.</p>
      ) : logs.error ? (
        <ErrorBox>{logs.error}</ErrorBox>
      ) : logs.loading ? (
        <Spinner />
      ) : (
        <pre className="max-h-96 overflow-auto rounded-lg bg-term p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap text-term-fg">
          {logs.data?.logs || 'No output.'}
        </pre>
      )}
    </Card>
  )
}
