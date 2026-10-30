import { afterEach, describe, expect, it, vi } from 'vitest'
import { copyText } from './clipboard'

afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })

describe('copy result feedback', () => {
  it('propagates clipboard rejection rather than reporting success', async () => {
    vi.stubGlobal('navigator', { clipboard: { writeText: vi.fn().mockRejectedValue(new Error('denied')) } })
    await expect(copyText('value')).rejects.toThrow('denied')
  })

  it.each([true, false])('returns the actual legacy clipboard outcome (%s) and removes its temporary element', async success => {
    vi.stubGlobal('navigator', {})
    Object.defineProperty(document, 'execCommand', { configurable: true, value: vi.fn().mockReturnValue(success) })
    const result = copyText('value')
    if (success) await expect(result).resolves.toBeUndefined()
    else await expect(result).rejects.toThrow('Clipboard copy failed')
    expect(document.querySelector('textarea')).toBeNull()
  })
})
