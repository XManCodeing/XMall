import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import { httpIntegrationMessages } from '@/i18n/httpIntegration'
import type { Config } from '@/api/httpIntegration'

vi.mock('@/api/httpIntegration', async importOriginal => {
  const actual = await importOriginal<typeof import('@/api/httpIntegration')>()
  return { ...actual, httpIntegrationAPI: { getConfig: vi.fn(), saveConfig: vi.fn(), parseCurl: vi.fn(), mapResponse: vi.fn(), test: vi.fn() } }
})
vi.mock('@/utils/confirm', () => ({ confirmAction: vi.fn().mockResolvedValue(true) }))
vi.mock('@/utils/notify', () => {
  const notifyError = vi.fn()
  return { notifySuccess: vi.fn(), notifyError, notifyErrorOnce: vi.fn((error: { __notified?: boolean } | null, message: string) => {
    if (!error?.__notified) notifyError(message)
  }) }
})
import { notifyError, notifyErrorOnce } from '@/utils/notify'
import { httpIntegrationAPI } from '@/api/httpIntegration'
import { confirmAction } from '@/utils/confirm'
import HttpIntegrationEditor from './HttpIntegrationEditor.vue'

let app: App | undefined
let root: HTMLDivElement
const flush = async () => { await Promise.resolve(); await nextTick(); await Promise.resolve(); await nextTick() }
const config: Config = {
  credentials: { token: '********' },
  endpoints: { ping: { method: 'GET', path: '/ping', encoding: 'none', headers: {}, query: {}, body: {}, success: { path: 'ok', equals: true }, fields: {}, list_path: '' } },
}
async function mount() {
  root = document.createElement('div')
  document.body.append(root)
  app = createApp(HttpIntegrationEditor, { connectionId: 7, baseUrl: 'https://upstream.example' })
  app.use(createI18n({ legacy: false, locale: 'en-US', messages: { 'en-US': { httpIntegration: httpIntegrationMessages['en-US'] } } }))
  const instance = app.mount(root) as unknown as { confirmLeave: () => Promise<boolean> }
  await flush()
  return instance
}
const button = (text: string) => Array.from(root.querySelectorAll('button')).find(item => item.textContent?.trim() === text)!
const input = async (element: HTMLInputElement | HTMLTextAreaElement, value: string) => {
  element.value = value
  element.dispatchEvent(new Event('input', { bubbles: true }))
  await flush()
}
afterEach(() => { app?.unmount(); root?.remove(); vi.clearAllMocks() })

describe('HTTP integration editor', () => {
  it('blocks saved-endpoint debugging while dirty and preserves masked credentials on save', async () => {
    vi.mocked(httpIntegrationAPI.getConfig).mockResolvedValue({ config, configured: true, revision: 4, ready: true, validation_errors: [] })
    vi.mocked(httpIntegrationAPI.saveConfig).mockImplementation(async (_id, saved) => ({ config: saved.config, configured: true, revision: saved.revision + 1, ready: true, validation_errors: [] }))
    await mount()
    expect(root.textContent).toContain('pass capability validation')
    const secret = root.querySelector('input[type="password"]') as HTMLInputElement
    expect(secret.value).toBe('')
    expect(secret.placeholder).toContain('Configured')
    await input(root.querySelector('textarea[aria-label="Response sample"]')!, '{"ok":true}')
    expect(button('Map offline').disabled).toBe(false)
    const checkbox = Array.from(root.querySelectorAll('label')).find(label => label.textContent?.includes('suitable for a live read-only test'))!.querySelector('[role="checkbox"]') as HTMLButtonElement
    checkbox.click()
    await flush()
    expect(button('Send live test').disabled).toBe(false)
    await input(root.querySelector('input[placeholder="/api/products"]')!, '/probe')
    expect(button('Map offline').disabled).toBe(true)
    expect(button('Send live test').disabled).toBe(true)
    expect(root.textContent).toContain('Unsaved changes')
    button('Save configuration').click()
    await flush()
    expect(httpIntegrationAPI.saveConfig).toHaveBeenCalledWith(7, expect.objectContaining({ config: expect.objectContaining({ credentials: { token: '********' } }), revision: 4, delete_credentials: [], delete_endpoints: [] }))
    expect(button('Map offline').disabled).toBe(false)
    expect(button('Send live test').disabled).toBe(true)
  })
  it('renders mapped response as text, never HTML', async () => {
    vi.mocked(httpIntegrationAPI.getConfig).mockResolvedValue({ config, configured: true, revision: 4, ready: true, validation_errors: [] })
    vi.mocked(httpIntegrationAPI.mapResponse).mockResolvedValue({ value: '<img src=x onerror="alert(1)">' })
    await mount()
    await input(root.querySelector('textarea[aria-label="Response sample"]')!, '{}')
    button('Map offline').click()
    await flush()
    expect(httpIntegrationAPI.mapResponse).toHaveBeenCalledWith(7, 'ping', '{}')
    expect(root.querySelector('pre')?.textContent).toContain('<img')
    expect(root.querySelector('pre img')).toBeNull()
  })
  it('keeps the editor open when unsaved-change confirmation is rejected', async () => {
    vi.mocked(httpIntegrationAPI.getConfig).mockResolvedValue({ config, configured: true, revision: 1, ready: true, validation_errors: [] })
    const instance = await mount()
    expect(await instance.confirmLeave()).toBe(true)
    await input(root.querySelector('input[placeholder="/api/products"]')!, '/changed')
    vi.mocked(confirmAction).mockResolvedValueOnce(false)
    expect(await instance.confirmLeave()).toBe(false)
    expect(root.textContent).toContain('Unsaved changes')
  })
  it('sends explicit credential deletion and keeps config metadata', async () => {
    vi.mocked(httpIntegrationAPI.getConfig).mockResolvedValue({ config: { ...config, version: 2, future: { enabled: true } }, configured: true, revision: 8, ready: false, validation_errors: ['missing product'] })
    vi.mocked(httpIntegrationAPI.saveConfig).mockImplementation(async (_id, saved) => ({ config: saved.config, configured: true, revision: 9, ready: false, validation_errors: [] }))
    await mount()
    expect(root.textContent).toContain('missing product')
    button('Remove row').click()
    await flush()
    button('Save configuration').click()
    await flush()
    expect(httpIntegrationAPI.saveConfig).toHaveBeenCalledWith(7, expect.objectContaining({ revision: 8, delete_credentials: ['token'], config: expect.objectContaining({ version: 2, future: { enabled: true } }) }))
  })
  it('displays sanitized diagnostics returned with a failed result', async () => {
    vi.mocked(httpIntegrationAPI.getConfig).mockResolvedValue({ config, configured: true, revision: 1, ready: false, validation_errors: [] })
    vi.mocked(httpIntegrationAPI.test).mockResolvedValue({ status_code: 502, duration_ms: 5, body: 'redacted', error: 'Upstream unavailable [redacted]' })
    await mount()
    const checkbox = Array.from(root.querySelectorAll('label')).find(label => label.textContent?.includes('suitable for a live read-only test'))!.querySelector('[role="checkbox"]') as HTMLButtonElement
    checkbox.click()
    await flush()
    button('Send live test').click()
    await flush()
    expect(root.textContent).toContain('Upstream unavailable [redacted]')
  })
  it('imports an absolute cURL source through the parser and saves exact typed response at a root-relative path', async () => {
    const typed = { text: '001', count: 2, enabled: false, missing: null, array: [1, '2', true], nested: { value: '9007199254740993' } }
    const source = `curl 'https://upstream.example/api/products?kind=all' --data-raw '${JSON.stringify(typed)}'`
    vi.mocked(httpIntegrationAPI.getConfig).mockResolvedValue({ config, configured: true, revision: 4, ready: true, validation_errors: [] })
    vi.mocked(httpIntegrationAPI.parseCurl).mockResolvedValue({ method: 'POST', path: '/api/products', encoding: 'json', headers: { 'Content-Type': 'application/json' }, query: { kind: 'all' }, body: {}, json_body: typed })
    vi.mocked(httpIntegrationAPI.saveConfig).mockImplementation(async (_id, saved) => ({ config: saved.config, configured: true, revision: 5, ready: true, validation_errors: [] }))
    await mount()
    await input(root.querySelector('textarea[aria-label="Import cURL"]')!, source)
    const confirm = Array.from(root.querySelectorAll('label')).find(label => label.textContent?.includes('cURL domain matches'))!.querySelector('[role="checkbox"]') as HTMLButtonElement
    confirm.click()
    await flush()
    button('Parse and import').click()
    await flush()
    expect(httpIntegrationAPI.parseCurl).toHaveBeenCalledWith(7, 'ping', source)
    expect((root.querySelector('input[placeholder="/api/products"]') as HTMLInputElement).value).toBe('/api/products')
    button('Save configuration').click()
    await flush()
    const update = vi.mocked(httpIntegrationAPI.saveConfig).mock.calls[0]![1]
    expect(update.config.endpoints.ping).toMatchObject({ method: 'POST', path: '/api/products', encoding: 'json', query: { kind: 'all' }, body: {}, json_body: typed, success: config.endpoints.ping!.success })
    expect(update.config.endpoints.ping!.json_body).toStrictEqual(typed)
  })
  it.each([false, true])('keeps save failures toast-only (already notified: %s)', async notified => {
    vi.mocked(httpIntegrationAPI.getConfig).mockResolvedValue({ config, configured: true, revision: 1, ready: true, validation_errors: [] })
    const error = Object.assign(new Error('private API failure'), { __notified: notified })
    vi.mocked(httpIntegrationAPI.saveConfig).mockRejectedValueOnce(error)
    await mount()
    await input(root.querySelector('input[placeholder="/api/products"]')!, '/changed')
    button('Save configuration').click()
    await flush()
    expect(notifyErrorOnce).toHaveBeenCalledWith(error, httpIntegrationMessages['en-US'].failed)
    expect(notifyError).toHaveBeenCalledTimes(notified ? 0 : 1)
    expect(root.textContent).not.toContain('private API failure')
    expect(root.querySelector('[role="alert"]')).toBeNull()
  })
  it('preserves local endpoint validation inline without a toast', async () => {
    vi.mocked(httpIntegrationAPI.getConfig).mockResolvedValue({ config, configured: true, revision: 1, ready: true, validation_errors: [] })
    await mount()
    await input(root.querySelector('input[placeholder="/api/products"]')!, 'https://bad.example')
    button('Save configuration').click()
    await flush()
    expect(root.querySelector('[role="alert"]')?.textContent).toBe(httpIntegrationMessages['en-US'].invalidPath)
    expect(notifyErrorOnce).not.toHaveBeenCalled()
    expect(httpIntegrationAPI.saveConfig).not.toHaveBeenCalled()
  })
  it.each(['parseCurl', 'mapResponse'] as const)('keeps %s transport failures out of the result panel', async action => {
    vi.mocked(httpIntegrationAPI.getConfig).mockResolvedValue({ config, configured: true, revision: 1, ready: true, validation_errors: [] })
    const error = Object.assign(new Error('already shown'), { __notified: true })
    vi.mocked(httpIntegrationAPI[action]).mockRejectedValueOnce(error)
    await mount()
    if (action === 'parseCurl') {
      await input(root.querySelector('textarea[aria-label="Import cURL"]')!, 'curl https://upstream.example/ping')
      const confirm = Array.from(root.querySelectorAll('label')).find(label => label.textContent?.includes('cURL domain matches'))!.querySelector('[role="checkbox"]') as HTMLButtonElement
      confirm.click()
      await flush()
      button('Parse and import').click()
    } else {
      await input(root.querySelector('textarea[aria-label="Response sample"]')!, '{}')
      button('Map offline').click()
    }
    await flush()
    expect(notifyErrorOnce).toHaveBeenCalledWith(error, httpIntegrationMessages['en-US'].failed)
    expect(notifyError).not.toHaveBeenCalled()
    expect(root.querySelector('[role="alert"]')).toBeNull()
  })
  it('shows a retry state after load failure rather than allowing saves', async () => {
    vi.mocked(httpIntegrationAPI.getConfig).mockRejectedValue(new Error('offline'))
    await mount()
    expect(root.textContent).toContain('Could not load configuration')
    expect(button('Retry')).toBeDefined()
    expect(button('Save configuration')).toBeUndefined()
  })
})
