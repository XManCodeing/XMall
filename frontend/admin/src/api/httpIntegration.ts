import { api } from './client'

export const HTTP_OPERATIONS = ['ping', 'balance', 'products', 'product', 'create_order', 'get_order', 'lookup_order', 'cancel_order'] as const
export type HttpOperation = typeof HTTP_OPERATIONS[number]
export type HttpEncoding = 'none' | 'json' | 'form'
export interface RequestSpec {
  method: string
  path: string
  encoding: HttpEncoding
  headers: Record<string, string>
  query: Record<string, string>
  body: Record<string, string>
  json_body?: unknown
  [key: string]: unknown
}
export interface Field {
  path: string
  value?: unknown
  default?: unknown
  required: boolean
  transform: string
  enum: Record<string, string>
  [key: string]: unknown
}
export interface Endpoint extends RequestSpec {
  success: { path: string; equals?: unknown }
  fields: Record<string, Field>
  list_path: string
  list?: boolean
  meta_fields?: Record<string, Field>
  collections?: Record<string, { path: string; fields: Record<string, Field> }>
}
export interface Config {
  version?: number
  endpoints: Record<string, Endpoint>
  credentials: Record<string, string>
  [key: string]: unknown
}
export interface ConfigResponse { config: Config; configured: boolean; revision: number; ready: boolean; validation_errors: string[] }
export interface ConfigUpdate { config: Config; revision: number; delete_credentials: string[]; delete_endpoints: string[] }
export interface Result { status_code: number; duration_ms: number; body: unknown; mapped?: unknown; error?: string }
export interface DebugRequest {
  action: 'parse_curl' | 'map_response' | 'test'
  endpoint: HttpOperation
  curl?: string
  response?: string
  variables?: Record<string, string>
}

export const httpIntegrationAPI = {
  async getConfig(id: number): Promise<ConfigResponse> {
    return (await api.get(`/admin/site-connections/${id}/http-config`)).data.data
  },
  async saveConfig(id: number, update: ConfigUpdate): Promise<ConfigResponse> {
    return (await api.put(`/admin/site-connections/${id}/http-config`, update)).data.data
  },
  async parseCurl(id: number, endpoint: HttpOperation, curl: string): Promise<RequestSpec> {
    return (await api.post(`/admin/site-connections/${id}/http-debug`, { action: 'parse_curl', endpoint, curl } satisfies DebugRequest)).data.data.request
  },
  async mapResponse(id: number, endpoint: HttpOperation, response: string): Promise<unknown> {
    return (await api.post(`/admin/site-connections/${id}/http-debug`, { action: 'map_response', endpoint, response } satisfies DebugRequest)).data.data.mapped
  },
  async test(id: number, endpoint: HttpOperation, variables: Record<string, string>): Promise<Result> {
    // Never send trading operations to the network debugger, even if called outside the UI.
    if (!isReadOnlyOperation(endpoint)) throw new Error('httpIntegration.tradingOnly')
    return (await api.post(`/admin/site-connections/${id}/http-debug`, { action: 'test', endpoint, variables } satisfies DebugRequest, { timeoutMs: 20000 })).data.data.result
  },
}
export const isReadOnlyOperation = (operation: HttpOperation) =>
  (['ping', 'balance', 'products', 'product'] as readonly string[]).includes(operation)
