import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({ show: vi.fn() }))
vi.mock('@/i18n', () => ({ default: { global: { t: (key: string) => key, locale: { value: 'zh-CN' } } } }))
vi.mock('@/stores/notice', () => ({ useNoticeStore: () => ({ show: mocks.show }) }))
vi.mock('@/utils/adminBase', () => ({ adminUrl: (path: string) => `/console${path}` }))

beforeEach(() => {
  vi.resetModules()
  mocks.show.mockClear()
  vi.stubGlobal('fetch', vi.fn())
  vi.stubGlobal('window', { location: { href: '' } })
  localStorage.setItem('admin_token', 'test-token')
})
afterEach(() => { vi.unstubAllGlobals(); localStorage.clear() })

const response = (status: number, body: unknown) => ({
  ok: status >= 200 && status < 300,
  status,
  headers: new Headers({ 'content-type': 'application/json' }),
  json: async () => body,
  blob: async () => ({ text: async () => JSON.stringify(body) }),
})

describe('request-owned operation feedback', () => {
  it.each([
    ['network', () => Promise.reject(new Error('offline'))],
    ['HTTP', () => Promise.resolve(response(403, { msg: 'Forbidden' }))],
    ['business', () => Promise.resolve(response(200, { status_code: 409, msg: 'Conflict' }))],
    ['invalid JSON', () => Promise.resolve({ ...response(200, null), json: async () => { throw new Error('parse') } })],
  ])('notifies once for %s failures even when the caller uses a different fallback', async (_, result) => {
    vi.mocked(fetch).mockImplementation(result as unknown as typeof fetch)
    const { api } = await import('./client')
    const { notifyErrorOnce } = await import('@/utils/notify')
    const error = await api.post('/admin/example', {}).catch(error => error)
    expect(error.__notified).toBe(true)
    notifyErrorOnce(error, 'Different operation-specific message')
    expect(mocks.show).toHaveBeenCalledTimes(1)
  })

  it.each([403, 200])('keeps download error ownership for HTTP %s', async status => {
    vi.mocked(fetch).mockResolvedValue(response(status, { msg: 'Export failed' }) as Response)
    const { api } = await import('./client')
    const { notifyErrorOnce } = await import('@/utils/notify')
    const error = await api.get('/admin/export', { blob: true }).catch(error => error)
    notifyErrorOnce(error, 'Download failed')
    expect(error.__notified).toBe(true)
    expect(mocks.show).toHaveBeenCalledExactlyOnceWith('Export failed', 'error')
  })

  it.each([401, 200])('preserves HTTP/business unauthorized redirects (%s)', async status => {
    vi.mocked(fetch).mockResolvedValue(response(status, { status_code: 401, msg: 'Unauthorized' }) as Response)
    const { api } = await import('./client')
    await expect(api.get('/admin/example')).rejects.toMatchObject({ __notified: true })
    expect(localStorage.getItem('admin_token')).toBeNull()
    expect(window.location.href).toBe('/console/login')
    expect(mocks.show).toHaveBeenCalledTimes(1)
  })

  it('does not redirect failed login requests', async () => {
    vi.mocked(fetch).mockResolvedValue(response(401, { msg: 'Invalid credentials' }) as Response)
    const { api } = await import('./client')
    await expect(api.post('/admin/login', {})).rejects.toMatchObject({ __notified: true })
    expect(window.location.href).toBe('')
    expect(mocks.show).toHaveBeenCalledTimes(1)
  })

  it('does not toast successful requests or successful downloads', async () => {
    vi.mocked(fetch).mockResolvedValue(response(200, { status_code: 0, data: {} }) as Response)
    const { api } = await import('./client')
    await api.get('/admin/example')
    const blob = new Blob(['export'])
    vi.mocked(fetch).mockResolvedValue({ ...response(200, null), headers: new Headers({ 'content-type': 'text/plain' }), blob: async () => blob } as Response)
    expect((await api.get('/admin/export', { blob: true })).data).toBe(blob)
    expect(mocks.show).not.toHaveBeenCalled()
  })
})
