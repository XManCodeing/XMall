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
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); localStorage.clear() })

const response = (status: number, body: unknown) => ({
  ok: status >= 200 && status < 300,
  status,
  headers: new Headers({ 'content-type': 'application/json' }),
  json: async () => body,
  blob: async () => ({ text: async () => JSON.stringify(body) }),
})

describe('request-owned operation feedback', () => {
  it.each(['network', 'http', 'json'])('keeps background GET failures silent: %s', async kind => {
    if (kind === 'network') vi.mocked(fetch).mockRejectedValue(new Error('offline'))
    else if (kind === 'json') vi.mocked(fetch).mockResolvedValue({ ...response(200, null), json: async () => { throw new SyntaxError('invalid') } } as unknown as Response)
    else vi.mocked(fetch).mockResolvedValue(response(503, { msg: 'unavailable' }) as Response)
    const { api } = await import('./client')
    await expect(api.get('/admin/system/version/check', { silent: true })).rejects.toBeInstanceOf(Error)
    expect(mocks.show).not.toHaveBeenCalled()
  })
  it('does not suppress write failures even when silent is passed', async () => {
    vi.mocked(fetch).mockRejectedValue(new Error('offline'))
    const { api } = await import('./client')
    await expect(api.post('/admin/products', {}, { silent: true })).rejects.toBeInstanceOf(Error)
    expect(mocks.show).toHaveBeenCalledOnce()
  })
  it.each(['headers', 'body'])('times out across %s and marks writes unknown', async phase => {
    vi.useFakeTimers()
    vi.mocked(fetch).mockImplementation(async () => phase === 'headers' ? await new Promise<Response>(() => {})
      : { ...response(200, {}), json: () => new Promise(() => {}) } as Response)
    const { api } = await import('./client')
    const pending = api.post('/admin/products', {}).catch(error => error)
    await vi.advanceTimersByTimeAsync(10000)
    expect(await pending).toMatchObject({ kind: 'timeout', resultUnknown: true })
    expect(mocks.show).toHaveBeenCalledTimes(1)
  })
  it('honors the 30-second save deadline without losing body timeout coverage', async () => {
    vi.useFakeTimers()
    vi.mocked(fetch).mockResolvedValue({ ...response(200, {}), json: () => new Promise(() => {}) } as Response)
    const { api } = await import('./client')
    let settled = false
    const pending = api.put('/admin/products/7', {}, { timeoutMs: 30000 }).catch(error => { settled = true; return error })
    await vi.advanceTimersByTimeAsync(10000); expect(settled).toBe(false)
    await vi.advanceTimersByTimeAsync(20000)
    expect(await pending).toMatchObject({ kind: 'timeout', resultUnknown: true })
  })
  it.each(['post', 'put', 'patch', 'delete'] as const)('marks %s network TypeError unknown without replay', async method => {
    vi.mocked(fetch).mockRejectedValue(new TypeError('Failed to fetch'))
    const { api } = await import('./client')
    await expect(api[method]('/admin/products', {})).rejects.toMatchObject({ kind: 'network', resultUnknown: true })
    expect(fetch).toHaveBeenCalledTimes(1)
  })
  it.each([200, 502])('marks invalid JSON with HTTP %s unknown on writes', async status => {
    vi.mocked(fetch).mockResolvedValue({ ...response(status, null), json: async () => { throw new SyntaxError('bad JSON') } } as unknown as Response)
    const { api } = await import('./client')
    await expect(api.post('/admin/products', {})).rejects.toMatchObject({ resultUnknown: true, response: { status } })
  })
  it.each([422, 200])('preserves definite business message for HTTP %s', async status => {
    vi.mocked(fetch).mockResolvedValue(response(status, { status_code: 422, msg: 'SKU编码重复' }) as Response)
    const { api } = await import('./client')
    await expect(api.post('/admin/products', {})).rejects.toMatchObject({ message: 'SKU编码重复', resultUnknown: false, response: { status: 422 } })
  })
  it('marks JSON 5xx unknown while preserving server message', async () => {
    vi.mocked(fetch).mockResolvedValue(response(503, { msg: '保存后连接中断' }) as Response)
    const { api } = await import('./client')
    await expect(api.put('/admin/products/7', {})).rejects.toMatchObject({ message: '保存后连接中断', resultUnknown: true })
  })
  it('keeps GET transport failure read-only and accepts plain JSON', async () => {
    const { api } = await import('./client')
    vi.mocked(fetch).mockRejectedValueOnce(new TypeError('offline'))
    await expect(api.get('/admin/products')).rejects.toMatchObject({ kind: 'network', resultUnknown: false })
    vi.mocked(fetch).mockResolvedValue(response(200, { id: 7 }) as Response)
    expect(await api.get('/admin/products/7')).toEqual({ data: { id: 7 } })
  })
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
