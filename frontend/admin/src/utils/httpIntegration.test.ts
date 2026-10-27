import { describe, expect, it, vi } from 'vitest'

vi.mock('@/api/client', () => ({ api: { get: vi.fn(), put: vi.fn(), post: vi.fn() } }))
import { api } from '@/api/client'
import { httpIntegrationAPI, isReadOnlyOperation, type RequestSpec } from '@/api/httpIntegration'
import { emptyField, endpointDraft, mergeRequest, parseEnum, rowsToRecord, serializeEndpoint, toRows, validRelativePath } from './httpIntegration'

const request: RequestSpec = { method: 'GET', path: '/products', encoding: 'none', headers: {}, query: {}, body: {} }

describe('HTTP integration configuration', () => {
  it('preserves credential masks when unchanged and replaces only explicit input', () => {
    const rows = toRows({ token: '********' }, true)
    expect(rows[0]?.value).toBe('')
    expect(rowsToRecord(rows)).toEqual({ token: '********' })
    rows[0]!.value = 'replacement'
    expect(rowsToRecord(rows)).toEqual({ token: 'replacement' })
  })
  it('preserves advanced and unknown endpoint fields with typed JSON', () => {
    const field = { path: 'id', required: true, transform: 'string', enum: {}, future: ['keep'] }
    const original = { ...request, success: { path: 'ok', equals: true }, fields: { id: field }, list_path: '', list: true, json_body: { count: 2, enabled: false, items: [null, '001'] }, meta_fields: { total: field }, collections: { skus: { path: 'variants', fields: { id: field } } }, future: { keep: true } }
    const draft = endpointDraft(original)
    draft.path = '/new'
    expect(serializeEndpoint(draft)).toEqual({ ...original, path: '/new', fields: { id: { ...field, value: undefined, default: undefined } } })
    draft.json_body = 'false'
    expect(serializeEndpoint(draft).json_body).toBe(false)
    draft.json_body = '"001"'
    expect(serializeEndpoint(draft).json_body).toBe('001')
  })
  it('rejects legacy body combined with typed JSON including false, zero and null', () => {
    const draft = endpointDraft()
    draft.path = '/api/products'
    draft.body = [{ key: 'legacy', value: 'value' }]
    for (const json of ['false', '0', 'null', '{}', '[]']) {
      draft.json_body = json
      expect(() => serializeEndpoint(draft)).toThrow('conflictingBody')
    }
    draft.body = []
    expect(serializeEndpoint(draft).json_body).toEqual([])
  })
  it('rejects blank or duplicate keys without silently dropping values', () => {
    expect(() => rowsToRecord([{ key: '', value: 'x' }])).toThrow('invalidRows')
    expect(() => rowsToRecord([{ key: 'a', value: '1' }, { key: ' a ', value: '2' }])).toThrow('invalidRows')
  })
  it('round-trips JSON scalar types and validates enums', () => {
    const draft = endpointDraft()
    draft.path = '/products'
    draft.successEquals = 'false'
    draft.fields = [{ ...emptyField('price'), path: 'data.0.price', value: '0', default: 'null', enum: '{"ready":"active"}' }]
    const endpoint = serializeEndpoint(draft)
    expect(endpoint.success.equals).toBe(false)
    expect(endpoint.fields.price?.value).toBe(0)
    expect(endpoint.fields.price?.default).toBeNull()
    expect(serializeEndpoint(endpointDraft(endpoint))).toEqual(endpoint)
    for (const input of ['{', '[]', 'true', '{"x":1}']) expect(() => parseEnum(input)).toThrow('invalidJson')
  })
  it('rejects invalid JSON and duplicate mapping fields', () => {
    const draft = endpointDraft()
    draft.path = '/products'
    draft.fields = [{ ...emptyField('price'), default: 'unquoted' }]
    expect(() => serializeEndpoint(draft)).toThrow('invalidJson')
    draft.fields = [emptyField('price'), emptyField('price')]
    expect(() => serializeEndpoint(draft)).toThrow('invalidRows')
  })
  it('imports request data without overwriting mappings', () => {
    const draft = endpointDraft()
    draft.fields = [emptyField('id')]
    draft.successPath = 'success'
    draft.successEquals = 'true'
    draft.list_path = 'data.products'
    const merged = mergeRequest(draft, request)
    expect(merged.path).toBe('/products')
    expect(merged.fields).toEqual(draft.fields)
    expect(merged.list_path).toBe('data.products')
    expect(merged.successEquals).toBe('true')
  })
  it('never imports an absolute host or authority path', () => {
    for (const path of ['https://other.test/api', '//other.test/api', '/\\other.test', '/x\r\n']) {
      expect(validRelativePath(path)).toBe(false)
      expect(() => mergeRequest(endpointDraft(), { ...request, path })).toThrow('invalidPath')
    }
    expect(validRelativePath('/api/{{product_id}}')).toBe(true)
  })
  it('blocks every trading operation before a debug network request', async () => {
    vi.mocked(api.post).mockClear()
    for (const operation of ['create_order', 'get_order', 'lookup_order', 'cancel_order'] as const) {
      expect(isReadOnlyOperation(operation)).toBe(false)
      await expect(httpIntegrationAPI.test(1, operation, {})).rejects.toThrow('tradingOnly')
    }
    expect(api.post).not.toHaveBeenCalled()
  })
  it('uses the saved-endpoint API contract without sending draft configuration', async () => {
    const result = { status_code: 200, duration_ms: 10, body: '{}' }
    vi.mocked(api.post).mockResolvedValue({ data: { data: { result } } })
    expect(await httpIntegrationAPI.test(9, 'products', { page: '1' })).toEqual(result)
    expect(api.post).toHaveBeenLastCalledWith('/admin/site-connections/9/http-debug', { action: 'test', endpoint: 'products', variables: { page: '1' } }, { timeoutMs: 20000 })
    vi.mocked(api.post).mockResolvedValue({ data: { data: { mapped: { order_id: '123' } } } })
    expect(await httpIntegrationAPI.mapResponse(9, 'create_order', '{}')).toEqual({ order_id: '123' })
    expect(api.post).toHaveBeenLastCalledWith('/admin/site-connections/9/http-debug', { action: 'map_response', endpoint: 'create_order', response: '{}' })
  })
  it('unwraps parsed requests and wraps saved configuration as required by the backend', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { data: { request } } })
    expect(await httpIntegrationAPI.parseCurl(9, 'products', 'curl https://example.test/products')).toEqual(request)
    const config = { endpoints: {}, credentials: { token: 'secret' } }
    const saved = { config: { ...config, credentials: { token: '********' } }, configured: true }
    vi.mocked(api.put).mockResolvedValue({ data: { data: saved } })
    const update = { config, revision: 3, delete_credentials: ['old'], delete_endpoints: ['cancel_order'] }
    expect(await httpIntegrationAPI.saveConfig(9, update)).toEqual(saved)
    expect(api.put).toHaveBeenLastCalledWith('/admin/site-connections/9/http-config', update)
  })
})
