import { effectScope, ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { TelegramBroadcastPayload } from '@/api/admin'
import { useTelegramBroadcastPreview } from './useTelegramBroadcastPreview'

const mocks = vi.hoisted(() => ({ preview: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { previewTelegramBroadcast: mocks.preview } }))
const response = (count: unknown) => ({ data: { data: { recipient_count: count } } })
const scopes: ReturnType<typeof effectScope>[] = []
function setup(recipientType = 'all') {
  const payload = ref<TelegramBroadcastPayload>({
    bot_id: 2, title: 'Title', message_html: 'Message', recipient_type: recipientType,
    user_ids: recipientType === 'specific' ? [1, 2, 3] : [], filters: {},
  })
  const eligible = ref(true)
  const audience = ref({ keyword: '', selected: [1, 2, 3] })
  const scope = effectScope()
  scopes.push(scope)
  return { payload, eligible, audience, scope, model: scope.run(() => useTelegramBroadcastPreview(payload, eligible, audience))! }
}
beforeEach(() => { vi.resetAllMocks(); mocks.preview.mockResolvedValue(response(2)) })
afterEach(() => { scopes.splice(0).forEach(scope => scope.stop()) })

describe('broadcast recipient preview', () => {
  it.each(['all', 'specific'])('requires explicit preview and uses server count for %s', async type => {
    const { payload, model } = setup(type)
    payload.value.message_html = 'Edited'
    expect(mocks.preview).not.toHaveBeenCalled()
    expect(model.consume()).toBeNull()
    const pending = model.preview()
    expect(model.loading.value).toBe(true)
    expect(model.canConfirm.value).toBe(false)
    await pending
    expect(mocks.preview).toHaveBeenCalledExactlyOnceWith(payload.value)
    expect(model.count.value).toBe(2)
    expect(model.canConfirm.value).toBe(true)
    expect(model.consume()).toEqual(payload.value)
    expect(model.consume()).toBeNull()
  })
  it('blocks zero effective recipients even when specific users are selected', async () => {
    mocks.preview.mockResolvedValue(response(0))
    const { model } = setup('specific')
    await model.preview()
    expect(model.state.value).toBe('ready')
    expect(model.count.value).toBe(0)
    expect(model.canConfirm.value).toBe(false)
    expect(model.consume()).toBeNull()
  })
  it.each([undefined, null, '3', -1, 1.5, NaN, Infinity])('fails closed for malformed count %s', async count => {
    mocks.preview.mockResolvedValue(response(count))
    const { model } = setup()
    await model.preview()
    expect(model.state.value).toBe('failed')
    expect(model.count.value).toBeNull()
    expect(model.consume()).toBeNull()
  })
  it('clears a previously valid result on failure and permits explicit retry', async () => {
    const { model } = setup()
    await model.preview()
    mocks.preview.mockRejectedValueOnce(new Error('offline'))
    await model.preview()
    expect(model.state.value).toBe('failed')
    expect(model.count.value).toBeNull()
    expect(model.consume()).toBeNull()
    await model.preview()
    expect(model.canConfirm.value).toBe(true)
  })
  it.each(['bot', 'type', 'selection', 'filters', 'candidateFilters', 'candidateSelection', 'message', 'eligibility'])('invalidates immediately after %s changes without fetching', async change => {
    const { payload, eligible, audience, model } = setup('specific')
    await model.preview()
    switch (change) {
      case 'bot': payload.value.bot_id = 3; break
      case 'type': payload.value.recipient_type = 'all'; break
      case 'selection': payload.value.user_ids!.push(4); break
      case 'filters': payload.value.filters!.keyword = 'new'; break
      case 'candidateFilters': audience.value.keyword = 'new'; break
      case 'candidateSelection': audience.value.selected.push(4); break
      case 'message': payload.value.message_html = 'new'; break
      case 'eligibility': eligible.value = false; break
    }
    expect(model.state.value).toBe('stale')
    expect(model.count.value).toBeNull()
    expect(model.consume()).toBeNull()
    expect(mocks.preview).toHaveBeenCalledTimes(1)
  })
  it('discards stale pending responses and does not let them overwrite a newer preview', async () => {
    let resolve!: (value: ReturnType<typeof response>) => void
    mocks.preview.mockReturnValueOnce(new Promise(done => { resolve = done }))
    const { payload, model } = setup()
    const first = model.preview()
    await model.preview()
    expect(mocks.preview).toHaveBeenCalledTimes(1)
    payload.value.bot_id = 3
    await model.preview()
    resolve(response(99))
    await first
    expect(model.count.value).toBe(2)
    expect(model.consume()?.bot_id).toBe(3)
  })
  it.each(['close', 'dispose'])('ignores pending response after %s', async action => {
    let resolve!: (value: ReturnType<typeof response>) => void
    mocks.preview.mockReturnValueOnce(new Promise(done => { resolve = done }))
    const { scope, model } = setup()
    const pending = model.preview()
    if (action === 'close') model.open.value = false
    else scope.stop()
    resolve(response(12))
    await pending
    expect(model.count.value).toBeNull()
    expect(model.consume()).toBeNull()
  })
  it('does not query if bot, permission or form is ineligible', async () => {
    const { eligible, model } = setup()
    eligible.value = false
    await model.preview()
    expect(mocks.preview).not.toHaveBeenCalled()
    expect(model.open.value).toBe(false)
  })
})
