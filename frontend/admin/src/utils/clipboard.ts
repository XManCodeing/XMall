/** Copy text using the browser API, falling back when permissions or context reject it.
 * @param value Plain text to copy. @returns Completion only after a successful copy.
 */
export const copyText = async (value: string): Promise<void> => {
  let clipboardError: unknown
  if (navigator?.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(value)
      return
    } catch (cause) {
      clipboardError = cause
    }
  }
  const previousFocus = document.activeElement
  const textarea = document.createElement('textarea')
  textarea.value = value
  textarea.style.position = 'fixed'
  textarea.style.opacity = '0'
  document.body.appendChild(textarea)
  try {
    textarea.focus()
    textarea.select()
    if (typeof document.execCommand !== 'function' || !document.execCommand('copy')) {
      throw clipboardError || new Error('Clipboard copy failed')
    }
  } finally {
    textarea.remove()
    if (previousFocus instanceof HTMLElement && previousFocus.isConnected) previousFocus.focus()
  }
}
