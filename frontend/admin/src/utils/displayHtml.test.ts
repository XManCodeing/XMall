import { describe, expect, it, vi } from 'vitest'
import { sanitizeDisplayHtml } from '../../../user/src/utils/displayHtml'
import { processHtmlForDisplay } from '../../../user/src/utils/content'
import { useLegal } from '../../../user/src/composables/useLegal'
vi.mock('../../../user/src/utils/image', () => ({ getImageUrl: (path: string) => `https://cdn.example.test${path}` }))
vi.mock('../../../user/node_modules/vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('../../../user/src/composables/usePageSeo', () => ({ usePageSeo: () => {} }))
vi.mock('../../../user/src/stores/app', () => ({
  useAppStore: () => ({
    loading: false,
    locale: 'zh-CN',
    config: {
      legal: {
        terms: { 'zh-CN': '<p>terms</p><img src="/uploads/terms.png" onerror="alert(1)">' },
        privacy: { 'zh-CN': '<a href="javascript:alert(1)">privacy</a><iframe src="https://evil.test"></iframe>' },
      },
    },
  }),
}))

// Reuse admin's existing jsdom/Vitest dependencies; the storefront has no DOM test runner.
function render(raw: string, resolveImage?: (path: string) => string) {
  const host = document.createElement('div')
  host.innerHTML = sanitizeDisplayHtml(raw, resolveImage)
  return host
}

describe('storefront rich-text display sanitization', () => {
  it('sanitizes the shared product/article display entry point', () => {
    const host = document.createElement('div')
    host.innerHTML = processHtmlForDisplay('<p>content</p><img src=/uploads/product.png onerror=alert(1)><script>alert(1)</script>')
    expect(host.querySelector('script')).toBeNull()
    expect(host.querySelector('img')?.hasAttribute('onerror')).toBe(false)
    expect(host.querySelector('img')?.getAttribute('src')).toBe('https://cdn.example.test/uploads/product.png')
  })

  it.each(['terms', 'privacy'] as const)('sanitizes the shared %s legal-page entry point', (type) => {
    const host = document.createElement('div')
    host.innerHTML = useLegal(() => type).content.value
    expect(host.textContent).toContain(type)
    expect(host.querySelector('iframe, script, [onerror]')).toBeNull()
    expect(host.querySelector('a')?.hasAttribute('href') ?? false).toBe(false)
    if (type === 'terms') expect(host.querySelector('img')?.getAttribute('src')).toBe('https://cdn.example.test/uploads/terms.png')
  })

  it.each([
    '<script>alert(1)</script><img src=x onerror=alert(1)><p onclick="alert(1)">content</p>',
    '<svg onload="alert(1)"><foreignObject><iframe srcdoc="<script>alert(1)</script>"></iframe></foreignObject></svg>',
    '<math><mtext><table><mglyph><style><!--</style><img title="--><img src=x onerror=alert(1)>">',
    '<form><input name="token"><button formaction="https://evil.test">submit</button></form><object data="https://evil.test"></object><embed src="https://evil.test">',
  ])('removes active content from the resulting DOM: %s', (raw) => {
    const host = render(raw)
    expect(host.querySelector('script, svg, math, iframe, form, input, button, object, embed, style')).toBeNull()
    for (const element of host.querySelectorAll('*')) {
      for (const attribute of element.attributes) expect(attribute.name).not.toMatch(/^on/i)
    }
  })

  it.each(['javascript:alert(1)', 'jav&#x61;script:alert(1)', 'java&#9;script:alert(1)', 'vbscript:alert(1)', 'data:text/html,test', 'file:///etc/passwd', 'custom:alert(1)'])(
    'blocks unsafe protocols: %s', (url) => {
      const host = render(`<a href="${url}">link</a><img src="${url}">`)
      expect(host.querySelector('a')?.hasAttribute('href')).toBe(false)
      expect(host.querySelector('img')?.hasAttribute('src')).toBe(false)
    },
  )

  it('retains formatting, table structure and legal links', () => {
    const host = render('<h2>title</h2><p><strong>bold</strong><em>italic</em><u>underline</u></p><ul><li>item</li></ul><table><tbody><tr><td colspan="2">cell</td></tr></tbody></table><a href="/terms" target="_blank" rel="opener">terms</a><a href="#section">section</a><a href="mailto:help@example.test">email</a><a href="tel:+123">phone</a><a href="https://example.test">https</a>')
    expect(host.querySelectorAll('h2, strong, em, u, li, table')).toHaveLength(6)
    expect(host.querySelector('td')?.getAttribute('colspan')).toBe('2')
    expect(Array.from(host.querySelectorAll('a'), (a) => a.getAttribute('href'))).toEqual(['/terms', '#section', 'mailto:help@example.test', 'tel:+123', 'https://example.test'])
    for (const anchor of host.querySelectorAll('a')) expect(anchor.getAttribute('rel')).toBe('noopener noreferrer')
  })

  it('resolves quoted, entity-encoded and unquoted upload image paths using DOM attributes', () => {
    const host = render('<img src=/uploads/a.png><img src="&#47;uploads/b.png"><img src="/uploads/a&quot; onerror=&quot;alert(1).png">', (path) => `https://cdn.example.test${path}`)
    const images = host.querySelectorAll('img')
    expect(images[0]?.getAttribute('src')).toBe('https://cdn.example.test/uploads/a.png')
    expect(images[1]?.getAttribute('src')).toBe('https://cdn.example.test/uploads/b.png')
    expect(images[2]?.hasAttribute('onerror')).toBe(false)
    expect(images[2]?.getAttribute('src')).toBe('https://cdn.example.test/uploads/a" onerror="alert(1).png')
  })

  it('validates image URLs after rewriting and rejects data images', () => {
    expect(render('<img src="/uploads/a.png">', () => 'javascript:alert(1)').querySelector('img')?.hasAttribute('src')).toBe(false)
    expect(render('<img src="data:image/svg+xml,test">').querySelector('img')?.hasAttribute('src')).toBe(false)
  })

  it('preserves editor alignment and image sizing without arbitrary styles or theme overrides', () => {
    const host = render('<p style="text-align:center;color:red;position:fixed;background:url(https://evil.test)">text</p><img src="/uploads/a.png" style="width:50%;height:120px;position:fixed" id="location" class="dark" data-x="test">')
    const paragraph = host.querySelector('p')!
    expect(paragraph.style.textAlign).toBe('center')
    expect(paragraph.style.color).toBe('')
    expect(paragraph.style.position).toBe('')
    expect(paragraph.style.background).toBe('')
    const image = host.querySelector('img')!
    expect(image.style.width).toBe('50%')
    expect(image.style.height).toBe('120px')
    expect(image.style.position).toBe('')
    expect(image.hasAttribute('id')).toBe(false)
    expect(image.hasAttribute('class')).toBe(false)
    expect(image.hasAttribute('data-x')).toBe(false)
    expect(sanitizeDisplayHtml('')).toBe('')
  })
})
