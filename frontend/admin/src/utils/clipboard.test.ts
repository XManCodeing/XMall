import { afterEach, describe, expect, it, vi } from 'vitest'
import { copyText } from './clipboard'

afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })

describe('copy result feedback', () => {
  it('propagates clipboard rejection rather than reporting success', async () => {
    vi.stubGlobal('navigator', { clipboard: { writeText: vi.fn().mockRejectedValue(new Error('denied')) } })
    Object.defineProperty(document, 'execCommand', { configurable: true, value: vi.fn().mockReturnValue(false) })
    await expect(copyText('value')).rejects.toThrow('denied')
  })

  it('falls back after API rejection and restores focus without false success', async () => {
    vi.stubGlobal('navigator', { clipboard: { writeText: vi.fn().mockRejectedValue(new Error('denied')) } })
    Object.defineProperty(document, 'execCommand', { configurable: true, value: vi.fn().mockReturnValue(true) })
    const button = document.createElement('button')
    document.body.appendChild(button)
    button.focus()
    try {
      await expect(copyText('order-number')).resolves.toBeUndefined()
      expect(document.execCommand).toHaveBeenCalledWith('copy')
      expect(document.activeElement).toBe(button)
      expect(document.querySelector('textarea')).toBeNull()
    } finally { button.remove() }
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
