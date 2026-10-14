import clsx from 'clsx'
import { Folder, Lock, Search, X } from 'lucide-react'
import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { api, type GitHubRepo, type RepoDir, type RepoRequest } from '@/lib/api'
import { timeAgo } from '@/lib/hooks'
import { ErrorBox, Input, Spinner } from './ui'

function Modal({ title, subtitle, onClose, children }: { title: ReactNode; subtitle?: ReactNode; onClose: () => void; children: ReactNode }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])
  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/40 px-4 pt-[10vh]" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div role="dialog" aria-modal="true" className="flex max-h-[75vh] w-full max-w-xl flex-col overflow-hidden rounded-xl border border-border bg-panel shadow-2xl">
        <header className="flex items-start justify-between gap-3 border-b border-border px-4 py-3">
          <div className="min-w-0">
            <h2 className="text-sm font-semibold">{title}</h2>
            {subtitle && <p className="mt-0.5 truncate text-xs text-muted">{subtitle}</p>}
          </div>
          <button type="button" onClick={onClose} className="rounded p-1 text-muted hover:bg-subtle hover:text-fg" aria-label="Close">
            <X className="size-4" />
          </button>
        </header>
        {children}
      </div>
    </div>
  )
}

function SearchBox({ value, onChange, placeholder }: { value: string; onChange: (v: string) => void; placeholder: string }) {
  const ref = useRef<HTMLInputElement>(null)
  useEffect(() => ref.current?.focus(), [])
  return (
    <div className="relative border-b border-border p-3">
      <Search className="pointer-events-none absolute top-1/2 left-5.5 size-4 -translate-y-1/2 text-muted" />
      <Input ref={ref} value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder} className="pl-8" />
    </div>
  )
}

/** Lists the repositories a GitHub token can access. */
export function RepoPicker({ token, projectId, onPick, onClose }: { token: string; projectId?: string; onPick: (r: GitHubRepo) => void; onClose: () => void }) {
  const [state, setState] = useState<{ loading: boolean; error?: string; login?: string; repos?: GitHubRepo[]; truncated?: boolean }>({ loading: true })
  const [q, setQ] = useState('')

  useEffect(() => {
    api.githubRepos({ token, projectId }).then(
      (r) => setState({ loading: false, ...r }),
      (e) => setState({ loading: false, error: e.message }),
    )
  }, [token, projectId])

  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase()
    return (state.repos ?? []).filter((r) => !needle || r.fullName.toLowerCase().includes(needle) || r.description?.toLowerCase().includes(needle))
  }, [state.repos, q])

  return (
    <Modal title="GitHub repositories" subtitle={state.login ? `Accessible with ${state.login}'s token${state.truncated ? ' · first 1000 shown' : ''}` : undefined} onClose={onClose}>
      <SearchBox value={q} onChange={setQ} placeholder="Filter repositories…" />
      <div className="min-h-40 overflow-y-auto">
        {state.loading ? (
          <Spinner />
        ) : state.error ? (
          <div className="p-4">
            <ErrorBox>{state.error}</ErrorBox>
          </div>
        ) : shown.length === 0 ? (
          <p className="p-6 text-center text-sm text-muted">No repository matches.</p>
        ) : (
          <ul className="divide-y divide-border">
            {shown.map((r) => (
              <li key={r.fullName}>
                <button type="button" onClick={() => onPick(r)} className="flex w-full items-start gap-3 px-4 py-2.5 text-left hover:bg-subtle">
                  <div className="min-w-0 flex-1">
                    <p className="flex items-center gap-1.5 truncate text-sm font-medium">
                      {r.fullName}
                      {r.private && <Lock className="size-3 shrink-0 text-muted" aria-label="private" />}
                      {r.archived && <span className="rounded bg-subtle px-1 text-[10px] text-muted">archived</span>}
                    </p>
                    {r.description && <p className="truncate text-xs text-muted">{r.description}</p>}
                  </div>
                  <span className="shrink-0 pt-0.5 text-xs text-muted">{timeAgo(r.pushedAt)}</span>
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </Modal>
  )
}

const markerLabel: Record<string, string> = {
  'netlify.toml': 'Netlify',
  'package.json': 'npm',
  Dockerfile: 'Dockerfile',
  'go.mod': 'Go',
  'hugo.toml': 'Hugo',
  'hugo.yaml': 'Hugo',
  'hugo.json': 'Hugo',
  'next.config.js': 'Next.js',
  'next.config.mjs': 'Next.js',
  'next.config.ts': 'Next.js',
  'vite.config.ts': 'Vite',
  'vite.config.js': 'Vite',
  'astro.config.mjs': 'Astro',
  Gemfile: 'Ruby',
  'requirements.txt': 'Python',
  'index.html': 'HTML',
}

const hiddenDir = (p: string) => p.split('/').some((s) => s.startsWith('.') && s !== '.' && s !== '.github') || /(^|\/)node_modules(\/|$)/.test(p)

/** Lists a branch's directories; build-related files are shown as badges. */
export function DirPicker({ repo, current, onPick, onClose }: { repo: RepoRequest; current: string; onPick: (dir: string) => void; onClose: () => void }) {
  const [state, setState] = useState<{ loading: boolean; error?: string; dirs?: RepoDir[]; truncated?: boolean }>({ loading: true })
  const [q, setQ] = useState('')
  const [onlyBuildable, setOnlyBuildable] = useState(false)

  useEffect(() => {
    api.dirs(repo).then(
      (r) => setState({ loading: false, ...r }),
      (e) => setState({ loading: false, error: e.message }),
    )
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [repo.repoUrl, repo.branch, repo.authType])

  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase()
    return (state.dirs ?? []).filter(
      (d) =>
        (d.path === '.' || !hiddenDir(d.path)) &&
        (!needle || d.path.toLowerCase().includes(needle)) &&
        (!onlyBuildable || d.markers.some((m) => ['netlify.toml', 'package.json', 'Dockerfile', 'go.mod', 'hugo.toml', 'hugo.yaml', 'Gemfile'].includes(m))),
    )
  }, [state.dirs, q, onlyBuildable])

  return (
    <Modal title="Choose a directory" subtitle={`${repo.repoUrl} · ${repo.branch}`} onClose={onClose}>
      <SearchBox value={q} onChange={setQ} placeholder="Filter directories…" />
      <label className="flex items-center gap-2 border-b border-border px-4 py-2 text-xs text-muted">
        <input type="checkbox" checked={onlyBuildable} onChange={(e) => setOnlyBuildable(e.target.checked)} />
        Only directories with build files (netlify.toml, package.json, Dockerfile…)
      </label>
      <div className="min-h-40 overflow-y-auto">
        {state.loading ? (
          <Spinner />
        ) : state.error ? (
          <div className="p-4">
            <ErrorBox>{state.error}</ErrorBox>
          </div>
        ) : (
          <ul className="py-1">
            {shown.slice(0, 800).map((d) => {
              const depth = d.path === '.' ? 0 : d.path.split('/').length
              const name = d.path === '.' ? '/ (repository root)' : d.path.split('/').pop()
              return (
                <li key={d.path}>
                  <button
                    type="button"
                    onClick={() => onPick(d.path)}
                    title={d.path}
                    style={{ paddingLeft: q ? 16 : 16 + depth * 16 }}
                    className={clsx('flex w-full items-center gap-2 py-1.5 pr-4 text-left text-sm hover:bg-subtle', d.path === current && 'bg-accent/10')}
                  >
                    <Folder className="size-4 shrink-0 text-muted" />
                    <span className="truncate font-mono text-xs">{q ? d.path : name}</span>
                    <span className="ml-auto flex shrink-0 gap-1">
                      {[...new Set(d.markers.map((m) => markerLabel[m] ?? m))].map((m) => (
                        <span key={m} className={clsx('rounded px-1.5 py-px text-[10px]', m === 'Netlify' ? 'bg-accent/15 text-accent' : 'bg-subtle text-muted')}>
                          {m}
                        </span>
                      ))}
                    </span>
                  </button>
                </li>
              )
            })}
            {(shown.length > 800 || state.truncated) && <li className="px-4 py-2 text-xs text-muted">List truncated: filter to narrow it down.</li>}
          </ul>
        )}
      </div>
    </Modal>
  )
}
