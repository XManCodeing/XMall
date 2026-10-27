import type { Config, Endpoint, Field, HttpOperation, RequestSpec } from '@/api/httpIntegration'
import { HTTP_OPERATIONS } from '@/api/httpIntegration'

export interface KeyValueRow { key: string; value: string; retained?: string }
export interface FieldRow {
  key: string
  path: string
  required: boolean
  transform: string
  value: string
  default: string
  enum: string
  original?: Field
}
export interface EndpointDraft {
  original?: Endpoint
  json_body: string
  list: boolean
  meta_fields: string
  collections: string
  method: string
  path: string
  encoding: RequestSpec['encoding']
  headers: KeyValueRow[]
  query: KeyValueRow[]
  body: KeyValueRow[]
  successPath: string
  successEquals: string
  list_path: string
  fields: FieldRow[]
}
export const toRows = (record: Record<string, string> = {}, secret = false): KeyValueRow[] =>
  Object.entries(record).map(([key, value]) => secret ? { key, value: '', retained: value } : { key, value })

export function rowsToRecord(rows: KeyValueRow[]): Record<string, string> {
  const entries = rows.map(row => [row.key.trim(), row.value || row.retained || ''] as const)
  if (entries.some(([key]) => !key) || new Set(entries.map(([key]) => key)).size !== entries.length) {
    throw new Error('httpIntegration.invalidRows')
  }
  return Object.fromEntries(entries)
}
export const jsonText = (value: unknown) => value === undefined ? '' : JSON.stringify(value)
export function parseOptionalJson(value: string): unknown {
  if (!value.trim()) return undefined
  try { return JSON.parse(value) } catch { throw new Error('httpIntegration.invalidJson') }
}
export function parseObject(text: string): Record<string, unknown> | undefined {
  const value = parseOptionalJson(text)
  if (value === undefined) return undefined
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('httpIntegration.invalidJson')
  return value as Record<string, unknown>
}
export function parseEnum(value: string): Record<string, string> {
  const parsed = parseOptionalJson(value) ?? {}
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed) || Object.values(parsed).some(item => typeof item !== 'string')) {
    throw new Error('httpIntegration.invalidJson')
  }
  return parsed as Record<string, string>
}
export function emptyField(key = ''): FieldRow {
  return { key, path: '', required: false, transform: '', value: '', default: '', enum: '' }
}
export function endpointDraft(endpoint?: Endpoint): EndpointDraft {
  return {
    original: endpoint,
    json_body: jsonText(endpoint?.json_body), list: endpoint?.list ?? false,
    meta_fields: jsonText(endpoint?.meta_fields), collections: jsonText(endpoint?.collections),
    method: endpoint?.method || 'GET', path: endpoint?.path || '', encoding: endpoint?.encoding || 'none',
    headers: toRows(endpoint?.headers), query: toRows(endpoint?.query), body: toRows(endpoint?.body),
    successPath: endpoint?.success?.path || '', successEquals: jsonText(endpoint?.success?.equals), list_path: endpoint?.list_path || '',
    fields: Object.entries(endpoint?.fields || {}).map(([key, field]) => ({
      original: field, key, path: field.path || '', required: field.required || false, transform: field.transform || '',
      value: jsonText(field.value), default: jsonText(field.default), enum: jsonText(field.enum),
    })),
  }
}
export function validRelativePath(path: string): boolean {
  return path.startsWith('/') && !path.startsWith('//') && !path.includes('\\') && !/[\r\n]/.test(path)
}
export function serializeEndpoint(draft: EndpointDraft): Endpoint {
  if (!validRelativePath(draft.path)) throw new Error('httpIntegration.invalidPath')
  const body = rowsToRecord(draft.body)
  const jsonBody = parseOptionalJson(draft.json_body)
  if (jsonBody !== undefined && Object.keys(body).length) throw new Error('httpIntegration.conflictingBody')
  const fields: [string, Field][] = draft.fields.map(row => [row.key.trim(), {
    ...row.original, path: row.path, required: row.required, transform: row.transform,
    value: parseOptionalJson(row.value), default: parseOptionalJson(row.default), enum: parseEnum(row.enum),
  }])
  if (fields.some(([key]) => !key) || new Set(fields.map(([key]) => key)).size !== fields.length) throw new Error('httpIntegration.invalidRows')
  return {
    ...draft.original,
    json_body: jsonBody, list: draft.list,
    meta_fields: parseObject(draft.meta_fields) as Endpoint['meta_fields'],
    collections: parseObject(draft.collections) as Endpoint['collections'],
    method: draft.method, path: draft.path, encoding: draft.encoding,
    headers: rowsToRecord(draft.headers), query: rowsToRecord(draft.query), body,
    success: { ...draft.original?.success, path: draft.successPath, equals: parseOptionalJson(draft.successEquals) },
    list_path: draft.list_path, fields: Object.fromEntries(fields),
  }
}
export const makeDrafts = (config: Config) => Object.fromEntries(
  HTTP_OPERATIONS.map(operation => [operation, endpointDraft(config.endpoints?.[operation])]),
) as Record<HttpOperation, EndpointDraft>

export function mergeRequest(draft: EndpointDraft, request: RequestSpec): EndpointDraft {
  if (!validRelativePath(request.path)) throw new Error('httpIntegration.invalidPath')
  return {
    ...draft, method: request.method, path: request.path, encoding: request.encoding,
    headers: toRows(request.headers), query: toRows(request.query), body: toRows(request.body),
    json_body: jsonText(request.json_body),
  }
}
export const recommendedFields = (operation: HttpOperation): string[] => {
  if (operation === 'product' || operation === 'products') return ['id', 'title', 'price', 'currency', 'stock_quantity', 'is_active', 'sku_id']
  if (operation === 'ping' || operation === 'balance') return ['balance', 'currency', 'site_name']
  if (operation === 'create_order') return ['order_id', 'order_no', 'status', 'amount', 'currency']
  return ['order_id', 'order_no', 'status', 'fulfillment', 'amount', 'currency']
}
