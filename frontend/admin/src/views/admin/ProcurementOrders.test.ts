import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import { createI18n } from 'vue-i18n'
import { httpIntegrationMessages } from '@/i18n/httpIntegration'

vi.mock('@/api/admin', () => ({ adminAPI: {
  getSiteConnections: vi.fn(), getProcurementOrders: vi.fn(), getProcurementOrderStats: vi.fn(),
  retryProcurementOrder: vi.fn(), cancelProcurementOrder: vi.fn(),
} }))
vi.mock('@/utils/notify', () => {
  const notifyError = vi.fn()
  return { notifySuccess: vi.fn(), notifyError, notifyErrorOnce: vi.fn((error: { __notified?: boolean } | null, message: string) => {
    if (!error?.__notified) notifyError(message)
  }) }
})
vi.mock('@/utils/confirm', () => ({ confirmAction: vi.fn().mockResolvedValue(true) }))
import { adminAPI } from '@/api/admin'
import { notifyError, notifyErrorOnce } from '@/utils/notify'
import ProcurementOrders from './ProcurementOrders.vue'

let app: App | undefined
let root: HTMLDivElement | undefined
const flush = async () => { for (let i = 0; i < 5; i++) { await Promise.resolve(); await nextTick() } }
async function mount(status: string) {
  vi.mocked(adminAPI.getSiteConnections).mockResolvedValue({ data: { data: [] } } as any)
  vi.mocked(adminAPI.getProcurementOrders).mockResolvedValue({ data: { data: [{ id: 9, status, local_order_no: 'local', created_at: '2026-01-01', retry_count: 0 }] } } as any)
  vi.mocked(adminAPI.getProcurementOrderStats).mockResolvedValue({ data: { data: { total: 1, by_status: { [status]: 1 } } } } as any)
  vi.mocked(adminAPI.retryProcurementOrder).mockResolvedValue({ data: {} } as any)
  root = document.createElement('div')
  document.body.append(root)
  app = createApp(ProcurementOrders)
  app.use(createI18n({ legacy: false, locale: 'en-US', missingWarn: false, fallbackWarn: false, messages: { 'en-US': {
    httpIntegration: httpIntegrationMessages['en-US'], procurement: { actions: { retry: 'Retry order', cancel: 'Cancel order' } },
  } } }))
  app.mount(root)
  await flush()
}
const button = (text: string) => Array.from(root!.querySelectorAll('button')).find(item => item.textContent?.trim() === text)
afterEach(() => { app?.unmount(); root?.remove(); vi.clearAllMocks() })

describe('procurement outcome verification', () => {
  it('only exposes verification for unknown and uses the existing retry API', async () => {
    await mount('unknown')
    expect(button('Retry order')).toBeUndefined()
    expect(button('Cancel order')).toBeUndefined()
    expect(button('Verify upstream outcome')).toBeDefined()
    button('Verify upstream outcome')!.click()
    await flush()
    expect(adminAPI.retryProcurementOrder).toHaveBeenCalledExactlyOnceWith(9)
    expect(adminAPI.cancelProcurementOrder).not.toHaveBeenCalled()
    expect(adminAPI.getProcurementOrders).toHaveBeenCalledTimes(2)
  })
  it('leaves submitting read-only without verification, retry or cancel controls', async () => {
    await mount('submitting')
    expect(button('Verify upstream outcome')).toBeUndefined()
    expect(button('Retry order')).toBeUndefined()
    expect(button('Cancel order')).toBeUndefined()
  })
  it.each([true, false])('keeps verification errors single-notification: %s', async notified => {
    await mount('unknown')
    const error = { __notified: notified }
    vi.mocked(adminAPI.retryProcurementOrder).mockRejectedValueOnce(error)
    button('Verify upstream outcome')!.click()
    await flush()
    expect(notifyErrorOnce).toHaveBeenCalledWith(error, httpIntegrationMessages['en-US'].failed)
    expect(notifyError).toHaveBeenCalledTimes(notified ? 0 : 1)
    expect(button('Verify upstream outcome')?.disabled).toBe(false)
  })
  it('preserves retry entry for a rejected order', async () => {
    await mount('rejected')
    expect(button('Retry order')).toBeDefined()
    expect(button('Verify upstream outcome')).toBeUndefined()
  })
})
