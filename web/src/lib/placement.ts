import type { ContainerState, Project, ProjectInput } from './api'

const LOCK = /^\s*node\.labels\.node_name\s*==/

/** Placement constraint pinning a service to one node (by its node_name label). */
export const nodeLock = (node: string) => `node.labels.node_name == ${node}`

const squash = (c: string) => c.replace(/\s+/g, '')

export const isLockedTo = (placement: string[], node: string) => placement.some((c) => squash(c) === squash(nodeLock(node)))

/** Replaces any previous node_name lock with one on node. */
export const withLock = (placement: string[], node: string) => [...placement.filter((c) => c.trim() && !LOCK.test(c)), nodeLock(node)]

/** Nodes running the service's tasks right now (Swarm), in task order. */
export const runningNodes = (state?: ContainerState) => [
  ...new Set((state?.tasks ?? []).filter((t) => t.desiredState === 'Running' && t.currentState.startsWith('Running') && t.node).map((t) => t.node)),
]

/** Project as an update payload (an empty authSecret keeps the stored one). */
export const toInput = (p: Project): ProjectInput => ({
  name: p.name,
  repoUrl: p.repoUrl,
  branch: p.branch,
  authType: p.authType,
  authUsername: p.authUsername,
  authSecret: '',
  buildType: p.buildType,
  templateId: p.templateId,
  templateVars: p.templateVars,
  dockerfilePath: p.dockerfilePath,
  buildContext: p.buildContext,
  containerPort: p.containerPort,
  hostPort: p.hostPort,
  env: p.env,
  network: p.network,
  labels: p.labels,
  replicas: p.replicas,
  placement: p.placement,
  healthcheck: p.healthcheck,
  healthcheckPath: p.healthcheckPath,
})
