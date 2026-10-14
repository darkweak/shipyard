import { Boxes, GitBranch, Plus } from 'lucide-react'
import { api, type Project, type Template } from '@/lib/api'
import { timeAgo, useAsync } from '@/lib/hooks'
import { Link, navigate } from '@/lib/router'
import { Button, Empty, ErrorBox, PageHeader, Spinner, StatusBadge } from '@/components/ui'

export function repoLabel(url: string) {
  return url.replace(/^(https?:\/\/|ssh:\/\/)/, '').replace(/^[^@]+@/, '').replace(/\.git$/, '')
}

export function ProjectsPage() {
  const { data: projects, error, loading } = useAsync(api.projects, [], 5000)
  const { data: templates } = useAsync(api.templates, [])
  const buildLabel = (p: Project) =>
    p.buildType === 'netlify' ? 'Netlify' : p.buildType === 'template' ? (templates?.find((t: Template) => t.id === p.templateId)?.name ?? p.templateId) : 'Dockerfile'

  return (
    <>
      <PageHeader
        title="Projects"
        subtitle="Git repositories built and run as containers."
        actions={
          <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => navigate('/projects/new')}>
            New project
          </Button>
        }
      />
      <ErrorBox>{error}</ErrorBox>
      {loading && !projects ? (
        <Spinner />
      ) : projects?.length === 0 ? (
        <Empty icon={<Boxes className="size-8" />} title="No projects yet">
          Connect a repository and pick a Dockerfile template to deploy it.
        </Empty>
      ) : (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {projects?.map((p) => (
            <Link
              key={p.id}
              href={`/projects/${p.id}`}
              className="group flex flex-col rounded-xl border border-border bg-panel p-4 transition-colors hover:border-accent/50"
            >
              <div className="flex items-start justify-between gap-2">
                <h3 className="truncate font-semibold group-hover:text-accent">{p.name}</h3>
                {p.latestDeployment ? <StatusBadge status={p.latestDeployment.status} /> : <span className="text-xs text-muted">never deployed</span>}
              </div>
              <p className="mt-1 truncate font-mono text-xs text-muted">{repoLabel(p.repoUrl)}</p>
              <div className="mt-4 flex items-center gap-3 text-xs text-muted">
                <span className="inline-flex items-center gap-1">
                  <GitBranch className="size-3.5" />
                  {p.branch}
                </span>
                <span className="truncate">{buildLabel(p)}</span>
                {p.hostPort > 0 && <span className="ml-auto font-mono">:{p.hostPort}</span>}
              </div>
              {p.latestDeployment && (
                <p className="mt-3 truncate border-t border-border pt-3 text-xs text-muted">
                  {p.latestDeployment.commitSha && <span className="font-mono">{p.latestDeployment.commitSha.slice(0, 7)} · </span>}
                  {p.latestDeployment.commitMessage || p.latestDeployment.trigger} · {timeAgo(p.latestDeployment.createdAt)}
                </p>
              )}
            </Link>
          ))}
        </div>
      )}
    </>
  )
}
