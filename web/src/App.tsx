import { useEffect, useState } from 'react'
import { api, setUnauthorizedHandler } from '@/lib/api'
import { match, usePath } from '@/lib/router'
import { Spinner } from '@/components/ui'
import { Layout } from '@/components/Layout'
import { LoginPage } from '@/pages/LoginPage'
import { ProjectsPage } from '@/pages/ProjectsPage'
import { ProjectFormPage } from '@/pages/ProjectFormPage'
import { ProjectPage } from '@/pages/ProjectPage'
import { BlobsPage } from '@/pages/BlobsPage'
import { DeploymentPage } from '@/pages/DeploymentPage'
import { TemplatesPage } from '@/pages/TemplatesPage'
import { TemplateFormPage } from '@/pages/TemplateFormPage'
import { SettingsPage } from '@/pages/SettingsPage'

function Routes() {
  const path = usePath()
  let m: Record<string, string> | null
  if (path === '/' || path === '/projects') return <ProjectsPage />
  if (path === '/projects/new') return <ProjectFormPage />
  if ((m = match('/projects/:id/edit', path))) return <ProjectFormPage id={m.id} />
  if ((m = match('/projects/:id/blobs', path))) return <BlobsPage id={m.id} />
  if ((m = match('/projects/:id', path))) return <ProjectPage id={m.id} />
  if ((m = match('/deployments/:id', path))) return <DeploymentPage id={m.id} />
  if (path === '/templates') return <TemplatesPage />
  if (path === '/templates/new') return <TemplateFormPage />
  if ((m = match('/templates/:id', path))) return <TemplateFormPage id={m.id} />
  if (path === '/settings') return <SettingsPage />
  return <p className="text-muted">Page not found.</p>
}

export default function App() {
  const [authed, setAuthed] = useState<boolean | null>(null)

  useEffect(() => {
    setUnauthorizedHandler(() => setAuthed(false))
    api.session().then(
      () => setAuthed(true),
      () => setAuthed(false),
    )
  }, [])

  if (authed === null) return <Spinner />
  if (!authed) return <LoginPage onLogin={() => setAuthed(true)} />
  return (
    <Layout onLogout={() => api.logout().finally(() => setAuthed(false))}>
      <Routes />
    </Layout>
  )
}
