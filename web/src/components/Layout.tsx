import clsx from 'clsx'
import { Boxes, FileCode2, LogOut, Ship } from 'lucide-react'
import type { ReactNode } from 'react'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/hooks'
import { Link, usePath } from '@/lib/router'
import { Dot } from './ui'

const nav = [
  { href: '/projects', label: 'Projects', icon: Boxes, match: (p: string) => p === '/' || p.startsWith('/projects') || p.startsWith('/deployments') },
  { href: '/templates', label: 'Templates', icon: FileCode2, match: (p: string) => p.startsWith('/templates') },
]

export function Layout({ children, onLogout }: { children: ReactNode; onLogout: () => void }) {
  const path = usePath()
  const { data: sys } = useAsync(api.system, [], 30000)

  return (
    <div className="flex min-h-screen flex-col md:flex-row">
      <aside className="flex shrink-0 items-center gap-1 border-b border-border bg-panel px-3 py-2 md:w-56 md:flex-col md:items-stretch md:border-r md:border-b-0 md:px-3 md:py-4">
        <Link href="/projects" className="mr-3 flex items-center gap-2 px-2 py-1 md:mr-0 md:mb-5">
          <span className="grid size-7 place-items-center rounded-lg bg-accent text-accent-fg">
            <Ship className="size-4" />
          </span>
          <span className="font-semibold tracking-tight">Shipyard</span>
        </Link>
        {nav.map(({ href, label, icon: Icon, match }) => (
          <Link
            key={href}
            href={href}
            className={clsx(
              'flex items-center gap-2.5 rounded-lg px-2.5 py-1.5 text-sm transition-colors',
              match(path) ? 'bg-subtle font-medium text-fg' : 'text-muted hover:bg-subtle hover:text-fg',
            )}
          >
            <Icon className="size-4" />
            {label}
          </Link>
        ))}
        <div className="ml-auto flex items-center gap-1 md:mt-auto md:ml-0 md:flex-col md:items-stretch">
          <div className="flex items-center gap-2 px-2.5 py-1.5 text-xs text-muted" title={sys?.docker.error}>
            <Dot tone={sys === undefined ? 'gray' : sys.docker.ok ? 'green' : 'red'} />
            <span className="hidden sm:inline">
              {sys === undefined
                ? 'Docker…'
                : !sys.docker.ok
                  ? 'Docker unreachable'
                  : sys.target === 'swarm'
                    ? `Swarm · ${sys.swarm?.nodes ?? '?'} node${sys.swarm?.nodes === 1 ? '' : 's'}`
                    : `Docker ${sys.docker.version}`}
            </span>
          </div>
          <button
            onClick={onLogout}
            className="flex items-center gap-2.5 rounded-lg px-2.5 py-1.5 text-sm text-muted transition-colors hover:bg-subtle hover:text-fg"
          >
            <LogOut className="size-4" />
            <span className="hidden sm:inline">Sign out</span>
          </button>
        </div>
      </aside>
      <main className="min-w-0 flex-1 px-4 py-6 md:px-10 md:py-8">
        <div className="mx-auto max-w-6xl">{children}</div>
      </main>
    </div>
  )
}
