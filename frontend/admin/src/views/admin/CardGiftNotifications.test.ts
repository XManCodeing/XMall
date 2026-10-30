import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App, type Component } from 'vue'
import { createI18n } from 'vue-i18n'
import { createMemoryHistory, createRouter } from 'vue-router'
import { readFileSync } from 'node:fs'

vi.mock('@/api/admin', () => ({ adminAPI: {
  getProducts: vi.fn(), getProduct: vi.fn(), getCardSecretStats: vi.fn(), getCardSecretBatches: vi.fn(),
  getCardSecrets: vi.fn(), batchUpdateCardSecretStatus: vi.fn(), batchDeleteCardSecrets: vi.fn(),
  exportCardSecrets: vi.fn(), exportAvailableCardSecrets: vi.fn(), createCardSecretBatch: vi.fn(),
  importCardSecretCSV: vi.fn(), updateCardSecret: vi.fn(), getGiftCards: vi.fn(),
  batchUpdateGiftCardStatus: vi.fn(), exportGiftCards: vi.fn(), generateGiftCards: vi.fn(),
  updateGiftCard: vi.fn(), deleteGiftCard: vi.fn(),
} }))
vi.mock('@/utils/notify', () => ({ notifySuccess: vi.fn(), notifyErrorOnce: vi.fn() }))
vi.mock('@/utils/confirm', () => ({ confirmAction: vi.fn().mockResolvedValue(true) }))

import { adminAPI } from '@/api/admin'
import { notifyErrorOnce, notifySuccess } from '@/utils/notify'
import { confirmAction } from '@/utils/confirm'
import CardSecrets from './CardSecrets.vue'
import CardSecretExports from './CardSecretExports.vue'
import GiftCards from './GiftCards.vue'
import CardSecretBatchCreateModal from './components/CardSecretBatchCreateModal.vue'
import CardSecretEditModal from './components/CardSecretEditModal.vue'

let app: App | undefined
let root: HTMLDivElement | undefined
const flush = async () => { for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() } }
const mount = async (component: Component, props: Record<string, unknown> = {}) => {
  root = document.createElement('div')
  document.body.append(root)
  app = createApp(component, props)
  app.use(createI18n({ legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false, messages: { en: {} } }))
  app.use(createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { template: '<div />' } }] }))
  const instance = app.mount(root)
  await flush()
  return (instance.$ as unknown as { setupState: Record<string, any> }).setupState
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(confirmAction).mockResolvedValue(true)
  for (const method of Object.values(adminAPI)) {
    vi.mocked(method).mockResolvedValue({ data: { data: [], pagination: { page: 1, page_size: 20, total: 1, total_page: 1 } } } as never)
  }
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
  vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: vi.fn(() => 'blob:test'), revokeObjectURL: vi.fn() }))
})
afterEach(() => { app?.unmount(); root?.remove(); vi.restoreAllMocks(); vi.unstubAllGlobals() })

describe('card and gift card operation notifications', () => {
  it('removes ordinary inline outcome state from all five components', () => {
    for (const name of ['CardSecrets.vue', 'CardSecretExports.vue', 'GiftCards.vue', 'components/CardSecretBatchCreateModal.vue', 'components/CardSecretEditModal.vue']) {
      const source = readFileSync(new URL(name, import.meta.url), 'utf8')
      expect(source).not.toMatch(/\b(batchActionSuccess|batchActionError|successMessage|resultMessage|batchSuccess|importSuccess)\b/)
      expect(source).toContain('notifyErrorOnce')
      expect(source).toContain('notifySuccess')
    }
  })

  it('keeps batch-create validation inline and preserves drafts on failure', async () => {
    const vm = await mount(CardSecretBatchCreateModal, { modelValue: true, productId: 1, skuId: 2 })
    await vm.handleBatchCreate()
    expect(vm.batchError).toBe('admin.cardSecrets.errors.secretsRequired')
    expect(notifyErrorOnce).not.toHaveBeenCalled()
    vm.batchForm.secrets = 'draft-secret'
    const error = { __notified: true, message: 'server detail' }
    vi.mocked(adminAPI.createCardSecretBatch).mockRejectedValueOnce(error)
    await vm.handleBatchCreate()
    expect(vm.batchError).toBe('')
    expect(vm.batchForm.secrets).toBe('draft-secret')
    expect(notifyErrorOnce).toHaveBeenCalledWith(error, 'admin.cardSecrets.errors.batchFailed')
    await vm.handleBatchCreate()
    expect(notifySuccess).toHaveBeenCalledWith('admin.cardSecrets.success.batchCreated')
    expect(vm.batchForm.secrets).toBe('')
  })

  it('keeps import validation inline but sends import outcomes to toast', async () => {
    const vm = await mount(CardSecretBatchCreateModal, { modelValue: true, productId: 1, skuId: 2 })
    await vm.handleImport()
    expect(vm.importError).toBe('admin.cardSecrets.errors.fileRequired')
    vm.importForm.file = new File(['secret'], 'cards.csv')
    const error = new Error('local failure')
    vi.mocked(adminAPI.importCardSecretCSV).mockRejectedValueOnce(error)
    await vm.handleImport()
    expect(vm.importError).toBe('')
    expect(vm.importForm.file).not.toBeNull()
    expect(notifyErrorOnce).toHaveBeenCalledWith(error, 'admin.cardSecrets.errors.importFailed')
    await vm.handleImport()
    expect(notifySuccess).toHaveBeenCalledWith('admin.cardSecrets.success.imported')
    expect(vm.importForm.file).toBeNull()
  })

  it('notifies card-secret edit outcomes without losing the failed draft', async () => {
    const vm = await mount(CardSecretEditModal, { modelValue: false, cardSecret: null })
    Object.assign(vm.editForm, { id: 1, secret: 'draft', status: 'available' })
    const error = new Error('failure')
    vi.mocked(adminAPI.updateCardSecret).mockRejectedValueOnce(error)
    await vm.submitEdit()
    expect(vm.editForm.secret).toBe('draft')
    expect(notifyErrorOnce).toHaveBeenCalledWith(error, 'admin.cardSecrets.errors.updateFailed')
    await vm.submitEdit()
    expect(notifySuccess).toHaveBeenCalledWith('admin.common.operationSuccess')
    expect(vm.editSubmitting).toBe(false)
  })

  it.each(['applyBatchStatus', 'deleteSecretsInScope', 'exportSecretsInScope'])('toasts card-secret %s and retains selection validation', async (action) => {
    const vm = await mount(CardSecrets)
    await vm[action]('txt')
    expect(vm.scopeValidationError).toBe('admin.cardSecrets.errors.selectRequired')
    vm.selectedSecretIds = [1]
    await vm[action]('txt')
    expect(vm.scopeValidationError).toBe('')
    expect(notifySuccess).toHaveBeenCalledTimes(1)
    expect(notifyErrorOnce).not.toHaveBeenCalled()
  })

  it('retains export content and metadata while copy/download outcomes use toast', async () => {
    const vm = await mount(CardSecretExports)
    await vm.submitExport()
    expect(vm.validationError).toBe('admin.cardSecrets.errors.productRequired')
    vm.selectedProductValue = '1'
    vm.availableCount = 2
    vm.exportCount = 2
    vi.mocked(adminAPI.exportAvailableCardSecrets).mockResolvedValueOnce({ data: { text: async () => 'secret-content' }, headers: { 'content-disposition': 'attachment; filename="cards.txt"' } } as any)
    await vm.submitExport()
    await vm.runConfirmedExport()
    expect(vm.exportResult).toMatchObject({ content: 'secret-content', filename: 'cards.txt', count: 2, format: 'txt' })
    expect(notifySuccess).toHaveBeenCalledWith('admin.cardSecretExports.success.used')
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: vi.fn().mockResolvedValue(undefined) } })
    await vm.copyExportContent()
    expect(notifySuccess).toHaveBeenCalledWith('admin.cardSecretExports.result.copied')
    vm.downloadExportResult()
    expect(notifySuccess).toHaveBeenCalledWith('admin.cardSecrets.success.batchExported')
    const error = new Error('clipboard denied')
    vi.mocked(navigator.clipboard.writeText).mockRejectedValueOnce(error)
    await vm.copyExportContent()
    expect(notifyErrorOnce).toHaveBeenCalledWith(error, 'admin.common.copyFailed')
    expect(vm.exportResult.content).toBe('secret-content')
  })

  it('retains generation count and batch number after automatic list refresh', async () => {
    const vm = await mount(GiftCards)
    await vm.submitGenerate()
    expect(vm.generateError).toBe('admin.giftCards.errors.invalidAmount')
    Object.assign(vm.generateForm, { name: 'Batch', amount: '20', quantity: 3 })
    vi.mocked(adminAPI.generateGiftCards).mockResolvedValueOnce({ data: { data: { created: 3, batch: { batch_no: 'B-123' } } } } as any)
    vi.mocked(adminAPI.getGiftCards).mockRejectedValueOnce(new Error('reload failure'))
    await vm.submitGenerate()
    await flush()
    expect(vm.generationResult).toEqual({ count: 3, batchNo: 'B-123' })
    expect(root!.querySelector('[data-testid="generation-result"]')?.textContent).toContain('B-123')
    expect(notifySuccess).toHaveBeenCalledWith('admin.giftCards.alerts.generated')
    expect(notifyErrorOnce).not.toHaveBeenCalled()
    expect(vm.generateError).toBe('')
  })

  it.each(['applyBatchStatus', 'exportSelected', 'submitEdit', 'handleDelete'])('toasts gift-card %s failures without inline API errors', async (action) => {
    const vm = await mount(GiftCards)
    vm.selectedCardIDs = [1]
    vm.openEditModal({ id: 1, name: 'Card', status: 'active' })
    const method = { applyBatchStatus: 'batchUpdateGiftCardStatus', exportSelected: 'exportGiftCards', submitEdit: 'updateGiftCard', handleDelete: 'deleteGiftCard' }[action]!
    const error = { __notified: true, message: 'private detail' }
    vi.mocked((adminAPI as any)[method]).mockRejectedValueOnce(error)
    await vm[action](action === 'handleDelete' ? { id: 1, code: 'test' } : 'txt')
    expect(notifyErrorOnce).toHaveBeenCalledWith(error, expect.stringMatching(/^admin\.giftCards\.errors\./))
    expect(vm.selectionValidationError).toBe('')
    expect(vm.editError).toBe('')
    expect(notifySuccess).not.toHaveBeenCalled()
    await vm[action](action === 'handleDelete' ? { id: 1, code: 'test' } : 'txt')
    expect(notifySuccess).toHaveBeenCalledTimes(1)
  })
})
