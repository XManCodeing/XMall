import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useCrudModal } from './useCrudModal'

const mocks = vi.hoisted(() => ({ show: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/i18n', () => ({ default: { global: { t: (key: string) => key } } }))
vi.mock('@/stores/notice', () => ({ useNoticeStore: () => ({ show: mocks.show }) }))

beforeEach(() => mocks.show.mockClear())

describe('CRUD operation feedback', () => {
  it('does not repeat request-layer errors inline or in a toast', async () => {
    const onSuccess = vi.fn()
    const modal = useCrudModal({ onSuccess })
    modal.openCreate()
    await modal.handleSubmit(async () => { throw { __notified: true, message: 'already reported' } })
    expect(mocks.show).not.toHaveBeenCalled()
    expect(modal.error.value).toBe('')
    expect(modal.showModal.value).toBe(true)
    expect(modal.submitting.value).toBe(false)
    expect(onSuccess).not.toHaveBeenCalled()
  })

  it('provides a translated fallback for local failures and preserves retry', async () => {
    const onSuccess = vi.fn()
    const modal = useCrudModal({ onSuccess })
    modal.openEdit(9)
    await modal.handleSubmit(async () => { throw {} })
    expect(mocks.show).toHaveBeenCalledExactlyOnceWith('common.api.requestFailed', 'error')
    expect(modal.error.value).toBe('')
    expect(modal.editingId.value).toBe(9)
    await modal.handleSubmit(async () => {})
    expect(modal.showModal.value).toBe(false)
    expect(onSuccess).toHaveBeenCalledOnce()
  })
})
