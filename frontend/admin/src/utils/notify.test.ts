import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({ show: vi.fn() }))
vi.mock('@/i18n', () => ({ default: { global: { t: (key: string) => key } } }))
vi.mock('@/stores/notice', () => ({ useNoticeStore: () => ({ show: mocks.show }) }))

beforeEach(() => { vi.resetModules(); mocks.show.mockClear() })

describe('operation error notifications', () => {
  it('does not duplicate a request-layer notification with a different fallback', async () => {
    const { notifyErrorOnce } = await import('./notify')
    notifyErrorOnce({ __notified: true }, 'operation failed')
    expect(mocks.show).not.toHaveBeenCalled()
  })

  it('handles the same local error only once, including frozen errors', async () => {
    const { notifyErrorOnce } = await import('./notify')
    const error = Object.freeze(new Error('private details'))
    notifyErrorOnce(error, 'Copy failed')
    notifyErrorOnce(error, 'Something completely different')
    expect(mocks.show).toHaveBeenCalledTimes(1)
    expect(mocks.show).toHaveBeenCalledWith('Copy failed', 'error')
  })

  it('does not confuse separate failures or expose raw errors', async () => {
    const { notifyErrorOnce } = await import('./notify')
    notifyErrorOnce(new Error('sensitive URL'), 'Upload failed')
    notifyErrorOnce(new Error('sensitive token'), 'Save failed')
    expect(mocks.show.mock.calls).toEqual([['Upload failed', 'error'], ['Save failed', 'error']])
  })

  it.each([null, undefined, 'private exception', 42, {}])('accepts unknown thrown values: %j', async error => {
    const { notifyErrorOnce } = await import('./notify')
    notifyErrorOnce(error)
    expect(mocks.show).toHaveBeenCalledWith('common.api.requestFailed', 'error')
  })
})
