import DOMPurify from 'dompurify'

/** Browser preview only: never use this result as the Telegram message payload. */
export function sanitizeBroadcastHtml(raw: string): string {
  if (!raw) return ''

  const fragment = DOMPurify.sanitize(raw, {
    ALLOWED_TAGS: [
      'p', 'br', 'hr', 'b', 'strong', 'i', 'em', 'u', 's', 'strike', 'del',
      'code', 'pre', 'blockquote', 'ul', 'ol', 'li', 'a', 'img', 'span', 'div',
      'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'table', 'thead', 'tbody', 'tr', 'th', 'td',
    ],
    ALLOWED_ATTR: ['href', 'target', 'title', 'src', 'alt', 'width', 'height', 'colspan', 'rowspan'],
    ALLOW_DATA_ATTR: false,
    ALLOW_ARIA_ATTR: false,
    RETURN_DOM_FRAGMENT: true,
  })
  fragment.querySelectorAll('[href], [src]').forEach((element) => {
    for (const attribute of ['href', 'src']) {
      const value = element.getAttribute(attribute)
      if (value === null) continue
      try {
        const protocol = new URL(value, 'https://display.invalid/').protocol
        const allowed = attribute === 'href' ? ['http:', 'https:', 'mailto:', 'tel:'] : ['http:', 'https:']
        if (!allowed.includes(protocol)) element.removeAttribute(attribute)
      } catch {
        element.removeAttribute(attribute)
      }
    }
  })
  fragment.querySelectorAll('a').forEach((anchor) => {
    anchor.setAttribute('rel', 'noopener noreferrer')
  })
  const holder = document.createElement('div')
  holder.appendChild(fragment)
  return holder.innerHTML
}
