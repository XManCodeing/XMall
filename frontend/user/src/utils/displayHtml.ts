import DOMPurify from 'dompurify'

/** Sanitize rich-text display only; custom script settings use a separate execution path. */
export function sanitizeDisplayHtml(raw: string, resolveImage: (path: string) => string = (path) => path): string {
    if (!raw) return ''

    const fragment = DOMPurify.sanitize(raw, {
        ALLOWED_TAGS: [
            'p', 'br', 'hr', 'b', 'strong', 'i', 'em', 'u', 's', 'strike', 'del',
            'code', 'pre', 'blockquote', 'ul', 'ol', 'li', 'a', 'img', 'span', 'div',
            'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'sub', 'sup',
            'table', 'caption', 'thead', 'tbody', 'tfoot', 'tr', 'th', 'td', 'colgroup', 'col',
        ],
        ALLOWED_ATTR: ['href', 'target', 'title', 'src', 'alt', 'style', 'width', 'height', 'colspan', 'rowspan', 'start'],
        ALLOW_DATA_ATTR: false,
        ALLOW_ARIA_ATTR: false,
        RETURN_DOM_FRAGMENT: true,
    })
    fragment.querySelectorAll<HTMLElement>('[style]').forEach((element) => {
        const alignment = element.style.textAlign
        const width = element.style.width
        const height = element.style.height
        element.removeAttribute('style')
        // Preserve editor alignment/image sizing, not arbitrary CSS or hardcoded theme colors.
        if (['left', 'center', 'right', 'justify', 'start', 'end'].includes(alignment)) {
            element.style.textAlign = alignment
        }
        if (element.tagName === 'IMG') {
            if (/^\d+(?:\.\d+)?(?:px|%)$/.test(width)) element.style.width = width
            if (/^\d+(?:\.\d+)?(?:px|%)$/.test(height)) element.style.height = height
        }
    })
    fragment.querySelectorAll('img[src]').forEach((image) => {
        const src = image.getAttribute('src') || ''
        if (src.startsWith('/uploads/')) image.setAttribute('src', resolveImage(src))
    })
    // Validate after URL resolution as well, and reject data images and unknown schemes.
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
