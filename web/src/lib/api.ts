export type Variable = { name: string; default: string; description: string }

export type Template = {
  id: string
  name: string
  description: string
  dockerfile: string
  dockerignore: string
  variables: Variable[]
  defaultPort: number
  builtin: boolean
  createdAt: string
  updatedAt: string
}

export type TemplateInput = Pick<Template, 'name' | 'description' | 'dockerfile' | 'dockerignore' | 'variables' | 'defaultPort'>

export type AuthType = 'none' | 'token' | 'ssh'
export type BuildType = 'dockerfile' | 'template' | 'netlify'

export type NetlifyPlan = {
  base: string
  publish: string
  command: string
  environment: Record<string, string>
  redirects: { from: string; to: string; status: number; force: boolean }[]
  headers: { for: string; values: Record<string, string> }[]
  functionsDir?: string
  warnings: string[]
}

export type Inspection = {
  ok: boolean
  error?: string
  hasDockerfile?: boolean
  netlify?: NetlifyPlan | { error: string }
}

export type Project = {
  id: string
  name: string
  slug: string
  repoUrl: string
  branch: string
  authType: AuthType
  authUsername: string
  hasAuthSecret: boolean
  buildType: BuildType
  templateId: string
  templateVars: Record<string, string>
  dockerfilePath: string
  buildContext: string
  containerPort: number
  hostPort: number
  env: Record<string, string>
  network: string
  labels: Record<string, string>
  replicas: number
  placement: string[]
  webhookSecret: string
  createdAt: string
  updatedAt: string
  latestDeployment?: Deployment | null
}

export type ProjectInput = Omit<
  Project,
  'id' | 'slug' | 'hasAuthSecret' | 'webhookSecret' | 'createdAt' | 'updatedAt' | 'latestDeployment'
> & { authSecret: string }

export type DeploymentStatus = 'queued' | 'cloning' | 'building' | 'deploying' | 'succeeded' | 'failed' | 'cancelled'

export type Deployment = {
  id: string
  projectId: string
  status: DeploymentStatus
  trigger: 'manual' | 'webhook' | 'rollback'
  commitSha: string
  commitMessage: string
  image: string
  containerId: string
  error: string
  sourceDeploymentId: string
  createdAt: string
  startedAt: string | null
  finishedAt: string | null
}

export type SwarmTask = { id: string; name: string; image: string; node: string; desiredState: string; currentState: string; error?: string }

export type ContainerState = {
  mode: 'docker' | 'swarm'
  service?: {
    id: string
    name: string
    image: string
    replicas: number
    updateState?: string
    updateMessage?: string
    ports: { targetPort: number; publishedPort: number; publishMode: string }[]
  }
  runningTasks?: number
  tasks?: SwarmTask[]
  exists: boolean
  id?: string
  image?: string
  state?: string
  running: boolean
  restarting: boolean
  exitCode: number
  restartCount: number
  startedAt?: string
  ports: { containerPort: string; hostIp: string; hostPort: string }[]
}

export type SystemInfo = {
  docker: { ok: boolean; version?: string; error?: string }
  target: 'docker' | 'swarm'
  registry: string
  swarm?: { nodeId: string; nodes: number; managers: number }
}

export type GitHubRepo = {
  fullName: string
  cloneUrl: string
  sshUrl: string
  private: boolean
  archived: boolean
  defaultBranch: string
  description: string
  pushedAt: string
}

export type RepoDir = { path: string; markers: string[] }

export type RepoRequest = { repoUrl: string; branch: string; authType: AuthType; authUsername: string; authSecret: string; projectId?: string }

export const isActive = (s: DeploymentStatus) => ['queued', 'cloning', 'building', 'deploying'].includes(s)

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message)
  }
}

let onUnauthorized: () => void = () => {}
export const setUnauthorizedHandler = (fn: () => void) => {
  onUnauthorized = fn
}

async function request<T>(method: string, url: string, body?: unknown): Promise<T> {
  const res = await fetch(url, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: 'same-origin',
  })
  if (res.status === 401 && url !== '/api/login') onUnauthorized()
  if (res.status === 204) return undefined as T
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new ApiError(res.status, data.error ?? res.statusText)
  return data as T
}

export const api = {
  login: (token: string) => request<{ ok: boolean }>('POST', '/api/login', { token }),
  logout: () => request<{ ok: boolean }>('POST', '/api/logout'),
  session: () => request<{ ok: boolean }>('GET', '/api/session'),
  system: () => request<SystemInfo>('GET', '/api/system'),

  templates: () => request<Template[]>('GET', '/api/templates'),
  template: (id: string) => request<Template>('GET', `/api/templates/${id}`),
  createTemplate: (t: TemplateInput) => request<Template>('POST', '/api/templates', t),
  updateTemplate: (id: string, t: TemplateInput) => request<Template>('PUT', `/api/templates/${id}`, t),
  deleteTemplate: (id: string) => request<void>('DELETE', `/api/templates/${id}`),
  preview: (body: { templateId?: string; dockerfile?: string; variables?: Variable[]; values: Record<string, string> }) =>
    request<{ dockerfile?: string; error?: string }>('POST', '/api/templates/preview', body),

  branches: (body: { repoUrl: string; authType: AuthType; authUsername: string; authSecret: string; projectId?: string }) =>
    request<{ ok: boolean; branches?: string[]; error?: string }>('POST', '/api/git/branches', body),

  inspect: (body: { repoUrl: string; branch: string; dir: string; authType: AuthType; authUsername: string; authSecret: string; projectId?: string }) =>
    request<Inspection>('POST', '/api/git/inspect', body),

  githubRepos: (body: { token: string; projectId?: string }) =>
    request<{ ok: boolean; error?: string; login?: string; repos?: GitHubRepo[]; truncated?: boolean }>('POST', '/api/github/repos', body),
  dirs: (body: RepoRequest) => request<{ ok: boolean; error?: string; dirs?: RepoDir[]; truncated?: boolean }>('POST', '/api/git/dirs', body),

  projects: () => request<Project[]>('GET', '/api/projects'),
  project: (id: string) => request<Project>('GET', `/api/projects/${id}`),
  createProject: (p: ProjectInput) => request<Project>('POST', '/api/projects', p),
  updateProject: (id: string, p: ProjectInput) => request<Project>('PUT', `/api/projects/${id}`, p),
  deleteProject: (id: string) => request<void>('DELETE', `/api/projects/${id}`),
  deploy: (id: string) => request<Deployment>('POST', `/api/projects/${id}/deploy`),
  rotateWebhook: (id: string) => request<Project>('POST', `/api/projects/${id}/webhook/rotate`),
  deployments: (id: string) => request<Deployment[]>('GET', `/api/projects/${id}/deployments`),
  container: (id: string) => request<ContainerState>('GET', `/api/projects/${id}/container`),
  containerAction: (id: string, action: 'start' | 'stop' | 'restart') =>
    request<ContainerState>('POST', `/api/projects/${id}/container/${action}`),
  containerLogs: (id: string, tail = 300) => request<{ logs: string }>('GET', `/api/projects/${id}/container/logs?tail=${tail}`),

  deployment: (id: string) => request<Deployment>('GET', `/api/deployments/${id}`),
  cancelDeployment: (id: string) => request<{ ok: boolean }>('POST', `/api/deployments/${id}/cancel`),
  redeploy: (id: string) => request<Deployment>('POST', `/api/deployments/${id}/redeploy`),
}
