import clsx from 'clsx'
import { AlertTriangle, CheckCircle2, FileCode2, FolderGit2, FolderOpen, Globe, KeyRound, PlugZap, Sparkles, XCircle } from 'lucide-react'
import { useEffect, useMemo, useState, type FormEvent, type ReactNode } from 'react'
import { DirPicker, RepoPicker } from '@/components/pickers'
import { api, type AuthType, type BuildType, type GitHubRepo, type Inspection, type RepoRequest, type NetlifyPlan, type Project, type ProjectInput, type Template } from '@/lib/api'
import { useAsync } from '@/lib/hooks'
import { Link, navigate } from '@/lib/router'
import { Button, Card, ErrorBox, Field, fromRows, Input, KeyValueEditor, PageHeader, Select, Spinner, Textarea, toRows } from '@/components/ui'

type Form = Omit<ProjectInput, 'env' | 'labels'> & { env: [string, string][]; labels: [string, string][] }

const blank: Form = {
  name: '',
  repoUrl: '',
  branch: 'main',
  authType: 'none',
  authUsername: '',
  authSecret: '',
  buildType: 'dockerfile',
  templateId: '',
  templateVars: {},
  dockerfilePath: 'Dockerfile',
  buildContext: '.',
  containerPort: 0,
  hostPort: 0,
  env: [],
  network: '',
  labels: [],
  replicas: 1,
  placement: [],
}

const fromProject = (p: Project): Form => ({
  name: p.name,
  repoUrl: p.repoUrl,
  branch: p.branch,
  authType: p.authType,
  authUsername: p.authUsername,
  authSecret: '',
  buildType: p.buildType,
  templateId: p.templateId,
  templateVars: p.templateVars ?? {},
  dockerfilePath: p.dockerfilePath,
  buildContext: p.buildContext,
  containerPort: p.containerPort,
  hostPort: p.hostPort,
  env: toRows(p.env),
  network: p.network,
  labels: toRows(p.labels),
  replicas: p.replicas,
  placement: p.placement ?? [],
})

const isSSH = (url: string) => url.startsWith('ssh://') || /^[\w.-]+@[\w.-]+:/.test(url)

export function ProjectFormPage({ id }: { id?: string }) {
  const editing = !!id
  const { data: templates } = useAsync(api.templates, [])
  const { data: sys } = useAsync(api.system, [])
  const swarm = sys?.target === 'swarm'
  const { data: project, error: loadError } = useAsync(() => (id ? api.project(id) : Promise.resolve(undefined)), [id])
  const [form, setForm] = useState<Form>(blank)
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  const [inspection, setInspection] = useState<Inspection>()

  const repoRequest = (over: Partial<Form> = {}): RepoRequest => {
    const f = { ...form, ...over }
    return { repoUrl: f.repoUrl, branch: f.branch, authType: f.authType, authUsername: f.authUsername, authSecret: f.authSecret, projectId: id }
  }
  // Looks for netlify.toml / Dockerfile to suggest a build type.
  const inspect = (over: Partial<Form> = {}) => {
    setInspection(undefined)
    api.inspect({ ...repoRequest(over), dir: over.buildContext ?? form.buildContext }).then(setInspection, () => {})
  }

  useEffect(() => {
    if (project) setForm(fromProject(project))
  }, [project])

  // New projects start with the defaults from Settings.
  const { data: settings } = useAsync(() => (editing ? Promise.resolve(undefined) : api.settings()), [editing])
  useEffect(() => {
    if (!settings) return
    // Rows typed before the defaults loaded win over defaults with the same key.
    const merge = (rows: [string, string][], defaults: Record<string, string>): [string, string][] => {
      const typed = new Set(rows.map(([k]) => k.trim()))
      return [...toRows(defaults).filter(([k]) => !typed.has(k)), ...rows.filter(([k, v]) => k.trim() || v)]
    }
    setForm((f) => ({ ...f, env: merge(f.env, settings.defaultEnv), labels: merge(f.labels, settings.defaultLabels) }))
  }, [settings])
  const hasDefaults = !!settings && (Object.keys(settings.defaultEnv).length > 0 || Object.keys(settings.defaultLabels).length > 0)

  // Pre-select a template from ?template=<id> (used by "Use template").
  useEffect(() => {
    const t = new URLSearchParams(location.search).get('template')
    if (!editing && t) setForm((f) => ({ ...f, buildType: 'template', templateId: t }))
  }, [editing])

  const set = <K extends keyof Form>(k: K, v: Form[K]) => setForm((f) => ({ ...f, [k]: v }))
  const template = form.buildType === 'template' ? templates?.find((t) => t.id === form.templateId) : undefined

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setSaving(true)
    setError('')
    const body: ProjectInput = {
      ...form,
      templateVars: Object.fromEntries(Object.entries(form.templateVars).filter(([, v]) => v !== '')),
      env: fromRows(form.env),
      labels: fromRows(form.labels),
      placement: form.placement.map((c) => c.trim()).filter(Boolean),
    }
    try {
      const p = editing ? await api.updateProject(id!, body) : await api.createProject(body)
      navigate(`/projects/${p.id}`)
    } catch (err) {
      setError((err as Error).message)
      setSaving(false)
    }
  }

  if (editing && !project) return loadError ? <ErrorBox>{loadError}</ErrorBox> : <Spinner />

  return (
    <form onSubmit={submit} className="space-y-5">
      <PageHeader
        title={editing ? `Settings · ${project!.name}` : 'New project'}
        subtitle={editing ? <span className="font-mono">container shipyard-{project!.slug}</span> : 'Connect a repository and choose how it is built.'}
      />

      <Card title="General">
        <Field label="Name">
          <Input required value={form.name} onChange={(e) => set('name', e.target.value)} placeholder="Marketing site" />
        </Field>
      </Card>

      <RepositoryCard form={form} set={set} project={project} onAccessible={inspect} />

      <BuildCard form={form} set={set} templates={templates} template={template} inspection={inspection} repo={repoRequest()} onDirPicked={(dir) => inspect({ buildContext: dir })} />

      <Card
        title="Run"
        actions={
          hasDefaults && (
            <Link href="/settings" className="text-xs text-muted hover:text-fg hover:underline">
              Environment and labels pre-filled from Settings
            </Link>
          )
        }
      >
        <div className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-3">
            <Field
              label="Container port"
              hint={
                form.buildType === 'netlify'
                  ? 'Empty = 8080 (the Netlify-compatible server)'
                  : template
                    ? `Empty = template default (${template.defaultPort})`
                    : 'Port the app listens on'
              }
            >
              <Input
                type="number"
                min={0}
                max={65535}
                value={form.containerPort || ''}
                placeholder={form.buildType === 'netlify' ? '8080' : template ? String(template.defaultPort) : '8080'}
                onChange={(e) => set('containerPort', Number(e.target.value))}
              />
            </Field>
            <Field label={swarm ? 'Published port' : 'Host port'} hint={swarm ? 'Published on every node (routing mesh). Empty = not published.' : 'Published on the Docker host. Empty = not published.'}>
              <Input type="number" min={0} max={65535} value={form.hostPort || ''} placeholder="e.g. 3001" onChange={(e) => set('hostPort', Number(e.target.value))} />
            </Field>
            <Field label="Docker network" hint={swarm ? 'Optional existing overlay network, e.g. your reverse proxy\'s.' : "Optional, e.g. your reverse proxy's network."}>
              <Input value={form.network} onChange={(e) => set('network', e.target.value)} placeholder="proxy" />
            </Field>
          </div>
          {swarm && (
            <div className="grid gap-4 sm:grid-cols-[160px_1fr]">
              <Field label="Replicas" hint="Rolling updates, start-first.">
                <Input type="number" min={0} max={100} value={form.replicas} onChange={(e) => set('replicas', Number(e.target.value))} />
              </Field>
              <Field label="Placement constraints" hint="One per line, e.g. node.labels.web == true or node.role == worker.">
                <Textarea
                  rows={2}
                  className="font-mono text-xs"
                  value={form.placement.join('\n')}
                  onChange={(e) => set('placement', e.target.value.split('\n'))}
                  placeholder="node.labels.web == true"
                />
              </Field>
            </div>
          )}
          <div>
            <p className="mb-2 text-sm font-medium">Environment variables</p>
            <KeyValueEditor rows={form.env} onChange={(r) => set('env', r)} keyPlaceholder="DATABASE_URL" />
            <p className="mt-2 text-xs text-muted">
              {form.buildType === 'netlify'
                ? 'Copy your Netlify site variables here (Site configuration → Environment variables): they are available to the build command, like on Netlify.'
                : 'Passed to the container at runtime. Also available while building as a BuildKit secret: RUN --mount=type=secret,id=buildenv sh -c ". /run/secrets/buildenv && …"'}
            </p>
          </div>
          <div>
            <p className="mb-2 text-sm font-medium">Container labels</p>
            <KeyValueEditor rows={form.labels} onChange={(r) => set('labels', r)} keyPlaceholder="traefik.enable" valuePlaceholder="true" />
            <p className="mt-2 text-xs text-muted">
              Handy for reverse proxies that discover {swarm ? 'services' : 'containers'} by labels (Traefik, caddy-docker-proxy…).
              {swarm && ' Applied to the service and its containers.'}
            </p>
          </div>
        </div>
      </Card>

      <ErrorBox>{error}</ErrorBox>
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" onClick={() => history.back()}>
          Cancel
        </Button>
        <Button variant="primary" loading={saving}>
          {editing ? 'Save changes' : 'Create project'}
        </Button>
      </div>
    </form>
  )
}

function RepositoryCard({
  form,
  set,
  project,
  onAccessible,
}: {
  form: Form
  set: <K extends keyof Form>(k: K, v: Form[K]) => void
  project?: Project
  onAccessible: (over: Partial<Form>) => void
}) {
  const [check, setCheck] = useState<{ busy?: boolean; ok?: boolean; error?: string; branches?: string[] }>({})
  const [browsing, setBrowsing] = useState(false)
  const storedSecret = project?.hasAuthSecret && project.authType === form.authType
  const canBrowse = form.authType === 'token' && (form.authSecret !== '' || storedSecret)

  // `over` carries values just set (state updates aren't visible yet).
  const runCheck = async (over: Partial<Form> = {}) => {
    const f = { ...form, ...over }
    setCheck({ busy: true })
    try {
      const r = await api.branches({ repoUrl: f.repoUrl, authType: f.authType, authUsername: f.authUsername, authSecret: f.authSecret, projectId: project?.id })
      setCheck(r)
      let branch = f.branch
      if (r.ok && r.branches?.length && !r.branches.includes(branch)) {
        branch = r.branches.includes('main') ? 'main' : r.branches.includes('master') ? 'master' : r.branches[0]
        set('branch', branch)
      }
      if (r.ok && r.branches?.includes(branch)) onAccessible({ ...over, branch })
    } catch (e) {
      setCheck({ ok: false, error: (e as Error).message })
    }
  }

  const pickRepo = (r: GitHubRepo) => {
    setBrowsing(false)
    const over: Partial<Form> = { repoUrl: r.cloneUrl, branch: r.defaultBranch, buildContext: '.' }
    set('repoUrl', r.cloneUrl)
    set('branch', r.defaultBranch)
    set('buildContext', '.')
    if (!form.name) set('name', r.fullName.split('/')[1])
    runCheck(over)
  }

  const authOptions: { value: AuthType; label: string; hint: string }[] = [
    { value: 'none', label: 'Public', hint: 'No credentials' },
    { value: 'token', label: 'Access token', hint: 'HTTPS + personal access / deploy token' },
    { value: 'ssh', label: 'SSH key', hint: 'Deploy key for git@host:… URLs' },
  ]

  return (
    <Card title="Repository">
      <div className="space-y-4">
        <Field label="Repository URL" hint="https://github.com/owner/repo.git or git@github.com:owner/repo.git">
          <Input
            required
            className="font-mono"
            value={form.repoUrl}
            onChange={(e) => {
              const url = e.target.value
              set('repoUrl', url)
              if (form.authType === 'ssh' && !isSSH(url) && url.startsWith('http')) set('authType', 'token')
              if (form.authType === 'token' && isSSH(url)) set('authType', 'ssh')
            }}
            placeholder="https://github.com/acme/website.git"
          />
        </Field>

        <div>
          <p className="mb-1.5 text-sm font-medium">Access</p>
          <div className="grid gap-2 sm:grid-cols-3">
            {authOptions.map((o) => (
              <button
                type="button"
                key={o.value}
                onClick={() => set('authType', o.value)}
                className={clsx(
                  'rounded-lg border px-3 py-2 text-left transition-colors',
                  form.authType === o.value ? 'border-accent bg-accent/5 ring-2 ring-accent/20' : 'border-border hover:bg-subtle',
                )}
              >
                <span className="block text-sm font-medium">{o.label}</span>
                <span className="block text-xs text-muted">{o.hint}</span>
              </button>
            ))}
          </div>
        </div>

        {form.authType === 'token' && (
          <div className="grid gap-4 sm:grid-cols-[1fr_2fr]">
            <Field label="Username" hint="Optional for GitHub. GitLab: oauth2, Bitbucket: x-token-auth.">
              <Input value={form.authUsername} onChange={(e) => set('authUsername', e.target.value)} placeholder="x-access-token" autoComplete="off" />
            </Field>
            <Field
              label="Token"
              hint={
                storedSecret
                  ? 'A token is stored. Leave empty to keep it.'
                  : 'GitHub: a fine-grained token with read access to Contents (and Metadata) on the repositories to deploy.'
              }
            >
              <div className="flex gap-2">
                <Input
                  type="password"
                  autoComplete="new-password"
                  value={form.authSecret}
                  onChange={(e) => set('authSecret', e.target.value)}
                  placeholder={storedSecret ? '•••••••• (stored)' : 'github_pat_… / ghp_…'}
                />
                <Button type="button" className="shrink-0" icon={<FolderGit2 className="size-4" />} disabled={!canBrowse} onClick={() => setBrowsing(true)}>
                  Browse repositories
                </Button>
              </div>
            </Field>
          </div>
        )}
        {browsing && <RepoPicker token={form.authSecret} projectId={project?.id} onPick={pickRepo} onClose={() => setBrowsing(false)} />}
        {form.authType === 'ssh' && (
          <Field
            label={
              <span className="inline-flex items-center gap-1.5">
                <KeyRound className="size-4" /> Private key
              </span>
            }
            hint={
              storedSecret
                ? 'A key is stored. Leave empty to keep it.'
                : 'Add the matching public key as a read-only deploy key on the repository. Generate one with: ssh-keygen -t ed25519 -N "" -f shipyard_key'
            }
          >
            <Textarea
              rows={5}
              className="font-mono text-xs"
              value={form.authSecret}
              onChange={(e) => set('authSecret', e.target.value)}
              placeholder={storedSecret ? '(stored)' : '-----BEGIN OPENSSH PRIVATE KEY-----'}
              spellCheck={false}
            />
          </Field>
        )}

        <div className="grid items-end gap-4 sm:grid-cols-[2fr_auto]">
          <Field label="Branch">
            <Input required list="branches" className="font-mono" value={form.branch} onChange={(e) => set('branch', e.target.value)} />
            <datalist id="branches">{check.branches?.map((b) => <option key={b} value={b} />)}</datalist>
          </Field>
          <Button type="button" icon={<PlugZap className="size-4" />} loading={check.busy} disabled={!form.repoUrl} onClick={() => runCheck()}>
            Check access
          </Button>
        </div>
        {check.ok === true && (
          <p className="flex items-center gap-1.5 text-sm text-emerald-600 dark:text-emerald-400">
            <CheckCircle2 className="size-4" /> Repository reachable · {check.branches?.length ?? 0} branch(es)
          </p>
        )}
        {check.ok === false && (
          <p className="flex items-start gap-1.5 text-sm text-red-600 dark:text-red-400">
            <XCircle className="mt-0.5 size-4 shrink-0" /> {check.error}
          </p>
        )}
      </div>
    </Card>
  )
}

function TemplateVars({ template, values, onChange }: { template: Template; values: Record<string, string>; onChange: (v: Record<string, string>) => void }) {
  const [preview, setPreview] = useState<{ dockerfile?: string; error?: string }>({})
  const key = useMemo(() => JSON.stringify(values), [values])

  useEffect(() => {
    const t = setTimeout(() => {
      const vals = Object.fromEntries(Object.entries(values).filter(([, v]) => v !== ''))
      api.preview({ templateId: template.id, values: vals }).then(setPreview, (e) => setPreview({ error: e.message }))
    }, 250)
    return () => clearTimeout(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [template.id, key])

  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <div className="space-y-3">
        {template.description && <p className="text-sm text-muted">{template.description}</p>}
        {template.variables.length === 0 && <p className="text-sm text-muted">This template has no variables.</p>}
        {template.variables.map((v) => (
          <Field key={v.name} label={<span className="font-mono text-xs">{v.name}</span>} hint={v.description}>
            <Input
              className="font-mono text-xs"
              value={values[v.name] ?? ''}
              placeholder={v.default || '(empty)'}
              onChange={(e) => onChange({ ...values, [v.name]: e.target.value })}
            />
          </Field>
        ))}
      </div>
      <Preview>{preview.error ? <span className="text-red-400">{preview.error}</span> : (preview.dockerfile ?? '…')}</Preview>
    </div>
  )
}

export function Preview({ children }: { children: ReactNode }) {
  return (
    <div className="min-w-0">
      <p className="mb-1.5 text-sm font-medium">Rendered Dockerfile</p>
      <pre className="max-h-[480px] overflow-auto rounded-lg bg-term p-3 font-mono text-xs leading-relaxed text-term-fg">{children}</pre>
    </div>
  )
}

const buildTypes: { value: BuildType; label: string; hint: string; icon: typeof Globe }[] = [
  { value: 'dockerfile', label: 'Repository Dockerfile', hint: 'The repository builds itself', icon: FileCode2 },
  { value: 'template', label: 'Dockerfile template', hint: 'Next.js, Hugo, Go… or your own', icon: Sparkles },
  { value: 'netlify', label: 'Netlify', hint: 'Build & serve from netlify.toml', icon: Globe },
]

function BuildCard({
  form,
  set,
  templates,
  template,
  inspection,
  repo,
  onDirPicked,
}: {
  form: Form
  set: <K extends keyof Form>(k: K, v: Form[K]) => void
  templates?: Template[]
  template?: Template
  inspection?: Inspection
  repo: RepoRequest
  onDirPicked: (dir: string) => void
}) {
  const [picking, setPicking] = useState(false)
  const netlify = inspection?.netlify
  const netlifyPlan = netlify && !('error' in netlify) ? (netlify as NetlifyPlan) : undefined
  const suggestNetlify = netlify && form.buildType !== 'netlify'

  return (
    <Card title="Build">
      <div className="space-y-4">
        {suggestNetlify && (
          <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-accent/30 bg-accent/5 px-3 py-2.5 text-sm">
            <span className="inline-flex items-center gap-2">
              <Globe className="size-4 text-accent" />
              <span>
                <span className="font-medium">netlify.toml detected</span> on this branch.
              </span>
            </span>
            <Button type="button" size="sm" variant="primary" onClick={() => set('buildType', 'netlify')}>
              Build with Netlify settings
            </Button>
          </div>
        )}
        {inspection?.ok && !netlify && form.buildType === 'dockerfile' && !inspection.hasDockerfile && (
          <p className="flex items-center gap-1.5 text-sm text-amber-600 dark:text-amber-400">
            <AlertTriangle className="size-4" /> No Dockerfile in {form.buildContext === '.' ? 'the repository root' : form.buildContext}: pick a template.
          </p>
        )}

        <div className="grid gap-2 sm:grid-cols-3">
          {buildTypes.map((o) => (
            <button
              type="button"
              key={o.value}
              onClick={() => {
                set('buildType', o.value)
                if (o.value === 'template' && !form.templateId && templates?.length) set('templateId', templates[0].id)
              }}
              className={clsx(
                'flex items-start gap-2.5 rounded-lg border px-3 py-2 text-left transition-colors',
                form.buildType === o.value ? 'border-accent bg-accent/5 ring-2 ring-accent/20' : 'border-border hover:bg-subtle',
              )}
            >
              <o.icon className="mt-0.5 size-4 shrink-0 text-muted" />
              <span>
                <span className="block text-sm font-medium">{o.label}</span>
                <span className="block text-xs text-muted">{o.hint}</span>
              </span>
            </button>
          ))}
        </div>

        {form.buildType === 'template' && (
          <>
            <Field label="Template" hint="Templates are managed in the Templates section.">
              <Select value={form.templateId} onChange={(e) => set('templateId', e.target.value)}>
                {templates?.map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name}
                  </option>
                ))}
              </Select>
            </Field>
            {template && <TemplateVars template={template} values={form.templateVars} onChange={(v) => set('templateVars', v)} />}
          </>
        )}
        {form.buildType === 'dockerfile' && (
          <Field label="Dockerfile path" hint="Relative to the repository root.">
            <Input value={form.dockerfilePath} onChange={(e) => set('dockerfilePath', e.target.value)} className="font-mono" />
          </Field>
        )}
        {form.buildType === 'netlify' && (
          <div className="space-y-3 rounded-lg bg-subtle px-3 py-3 text-sm">
            <p className="text-muted">
              Each deploy reads <code className="font-mono">netlify.toml</code> (production context), installs dependencies, runs the build command with the
              right Node.js / Hugo / Go versions, and serves the publish directory with a Netlify-compatible server: <code className="font-mono">_redirects</code>,{' '}
              <code className="font-mono">[[redirects]]</code> (redirects, rewrites, proxies, SPA fallbacks), headers, pretty URLs and 404.html. Netlify Functions in{' '}
              <code className="font-mono">netlify/functions</code> (or <code className="font-mono">[functions] directory</code>) are bundled with esbuild and run with
              Node.js: Lambda-style and Request/Response handlers, <code className="font-mono">config.path</code>, background and scheduled functions.
            </p>
            {netlifyPlan && <NetlifySummary plan={netlifyPlan} />}
            {netlify && 'error' in netlify && <ErrorBox>{netlify.error}</ErrorBox>}
          </div>
        )}

        <Field
          label={form.buildType === 'netlify' ? 'Base directory' : 'Build context'}
          hint={
            form.buildType === 'netlify'
              ? "Where netlify.toml lives, relative to the repository root (Netlify's “Base directory”). A base set inside netlify.toml takes precedence."
              : 'Directory sent to docker build, relative to the repository root (useful for monorepos).'
          }
        >
          <div className="flex gap-2">
            <Input value={form.buildContext} onChange={(e) => set('buildContext', e.target.value)} className="font-mono" />
            <Button type="button" className="shrink-0" icon={<FolderOpen className="size-4" />} disabled={!form.repoUrl || !form.branch} onClick={() => setPicking(true)}>
              Browse…
            </Button>
          </div>
        </Field>
        {picking && (
          <DirPicker
            repo={repo}
            current={form.buildContext}
            onClose={() => setPicking(false)}
            onPick={(dir) => {
              setPicking(false)
              set('buildContext', dir)
              onDirPicked(dir)
            }}
          />
        )}
      </div>
    </Card>
  )
}

function NetlifySummary({ plan }: { plan: NetlifyPlan }) {
  const env = Object.keys(plan.environment)
  return (
    <div className="space-y-2">
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-xs">
        <dt className="text-muted">Command</dt>
        <dd className="font-mono break-all">{plan.command || '(none)'}</dd>
        <dt className="text-muted">Publish</dt>
        <dd className="font-mono">{plan.publish || '.'}</dd>
        {plan.base && (
          <>
            <dt className="text-muted">Base</dt>
            <dd className="font-mono">{plan.base}</dd>
          </>
        )}
        {env.length > 0 && (
          <>
            <dt className="text-muted">Environment</dt>
            <dd className="font-mono break-all">{env.join(', ')}</dd>
          </>
        )}
        {plan.functionsDir && (
          <>
            <dt className="text-muted">Functions</dt>
            <dd className="font-mono">{plan.functionsDir}</dd>
          </>
        )}
        <dt className="text-muted">Rules</dt>
        <dd>
          {plan.redirects.length} redirect(s), {plan.headers.length} header block(s)
        </dd>
      </dl>
      {plan.warnings.map((w) => (
        <p key={w} className="flex items-start gap-1.5 text-xs text-amber-600 dark:text-amber-400">
          <AlertTriangle className="mt-px size-3.5 shrink-0" /> {w}
        </p>
      ))}
    </div>
  )
}
