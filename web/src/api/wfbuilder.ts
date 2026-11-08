// The workflow builder's own reads and its manual run (internal/dashboard/
// wfbuilder.go, api/workflowbuilder.go): what each configured connection can
// do (its plugin's declared verbs and events), where a workflow lives in the
// config file, the past events a test can use, and Run now. Kept apart from
// client.ts/types.ts so the builder's additions stay in one place.
import { authState } from './auth'
import { readResponse } from './errors'
import type { ActionResponse, CatalogField } from './types'

export interface CatalogVerb {
  name: string
  desc?: string
  ask?: boolean
  open?: boolean
  options?: CatalogField[]
  outputs?: CatalogField[]
}
export interface CatalogEvent {
  name: string
  desc?: string
  dynamic?: boolean
  filters?: string[]
  fields?: CatalogField[]
}
export interface CatalogConnection {
  name: string
  use: string
  desc?: string
  unavailable?: string
  verbs: CatalogVerb[]
  events: CatalogEvent[]
}
export interface WorkflowCatalogResponse {
  connections: CatalogConnection[]
}
export interface WorkflowEvent {
  id: string
  on: string
  at: string
  label: string
  fields?: Record<string, unknown>
}
export interface WorkflowBuilderInfo {
  kind: 'trigger' | 'workflow'
  path?: string[]
  file?: string
  events: WorkflowEvent[]
}

function get<T>(path: string): Promise<T> {
  return fetch(path, { headers: { Accept: 'application/json' }, credentials: 'same-origin' }).then((r) => readResponse<T>(r))
}

export const wfApi = {
  catalog: () => get<WorkflowCatalogResponse>('/api/v1/workflows/catalog'),
  builder: (name: string) => get<WorkflowBuilderInfo>('/api/v1/workflows/' + encodeURIComponent(name) + '/builder'),
  run: (name: string, inputs?: Record<string, unknown>) =>
    fetch('/api/v1/workflows/' + encodeURIComponent(name) + '/run', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...(authState.value.csrf ? { 'X-Conductor-CSRF': authState.value.csrf } : {}) },
      body: JSON.stringify({ inputs: inputs || {} }),
    }).then((r) => readResponse<ActionResponse>(r)),
}
