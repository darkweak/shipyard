import { FileCode2, Plus } from 'lucide-react'
import { api } from '@/lib/api'
import { timeAgo, useAsync } from '@/lib/hooks'
import { Link, navigate } from '@/lib/router'
import { Button, Empty, ErrorBox, PageHeader, Spinner } from '@/components/ui'

export function TemplatesPage() {
  const { data, error, loading } = useAsync(api.templates, [])

  return (
    <>
      <PageHeader
        title="Dockerfile templates"
        subtitle={
          <>
            Reusable Dockerfiles with variables, written as Go templates: <code className="font-mono text-xs">{'{{ .NODE_IMAGE }}'}</code>
          </>
        }
        actions={
          <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => navigate('/templates/new')}>
            New template
          </Button>
        }
      />
      <ErrorBox>{error}</ErrorBox>
      {loading && !data ? (
        <Spinner />
      ) : data?.length === 0 ? (
        <Empty icon={<FileCode2 className="size-8" />} title="No templates" />
      ) : (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {data?.map((t) => (
            <Link key={t.id} href={`/templates/${t.id}`} className="group flex flex-col rounded-xl border border-border bg-panel p-4 transition-colors hover:border-accent/50">
              <div className="flex items-start justify-between gap-2">
                <h3 className="font-semibold group-hover:text-accent">{t.name}</h3>
                {t.builtin && <span className="rounded bg-subtle px-1.5 py-0.5 text-[11px] text-muted">built-in</span>}
              </div>
              <p className="mt-1 line-clamp-2 flex-1 text-sm text-muted">{t.description || 'No description.'}</p>
              <div className="mt-4 flex flex-wrap items-center gap-1.5">
                {t.variables.slice(0, 4).map((v) => (
                  <span key={v.name} className="rounded bg-subtle px-1.5 py-0.5 font-mono text-[11px] text-muted">
                    {v.name}
                  </span>
                ))}
                {t.variables.length > 4 && <span className="text-[11px] text-muted">+{t.variables.length - 4}</span>}
                <span className="ml-auto text-xs text-muted">
                  {t.defaultPort > 0 && <span className="mr-2 font-mono">:{t.defaultPort}</span>}
                  {timeAgo(t.updatedAt)}
                </span>
              </div>
            </Link>
          ))}
        </div>
      )}
    </>
  )
}
