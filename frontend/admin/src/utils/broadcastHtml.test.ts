import { describe, expect, it } from 'vitest'
import { sanitizeBroadcastHtml } from './broadcastHtml'

function render(raw: string) {
  const host = document.createElement('div')
  host.innerHTML = sanitizeBroadcastHtml(raw)
  return host
}

describe('sanitizeBroadcastHtml', () => {
  it.each([
    '<script>alert(1)</script><img src="/uploads/a.png" onerror="alert(1)">',
    '<svg><g onload="alert(1)"></g><foreignObject><iframe srcdoc="<script>alert(1)</script>"></iframe></foreignObject></svg>',
    '<math><mtext><table><mglyph><style><!--</style><img title="--><img src=x onerror=alert(1)>">',
    '<form action="https://evil.test"><input name="token"><button>submit</button></form><object data="data:text/html,test"></object><embed src="https://evil.test">',
    '<div onclick="alert(1)" style="position:fixed;inset:0" id="location" name="cookie">text</div>',
  ])('removes active content after browser-style parsing: %s', (raw) => {
    const host = render(raw)
    expect(host.querySelector('script, svg, math, iframe, form, input, button, object, embed, style')).toBeNull()
    for (const element of host.querySelectorAll('*')) {
      for (const attribute of element.attributes) {
        expect(attribute.name).not.toMatch(/^on|^(?:style|id|name)$/i)
      }
    }
  })

  it.each(['javascript:alert(1)', 'jav&#x61;script:alert(1)', 'java&#10;script:alert(1)', 'vbscript:alert(1)', 'data:text/html,<script>alert(1)</script>', 'file:///etc/passwd', 'custom:alert(1)'])(
    'removes unsafe link protocols: %s', (href) => {
      expect(render(`<a href="${href}" target="_blank" rel="opener">link</a>`).querySelector('a')?.hasAttribute('href')).toBe(false)
    },
  )

  it('retains Telegram formatting, images and safe links without mutating the payload', () => {
    const payload = { message_html: '<b>bold</b><i>italic</i><u>underline</u><s>strike</s><pre><code>code</code></pre><blockquote>quote</blockquote><img src="/uploads/a.png" alt="photo"><a href="https://example.test" target="_blank" rel="opener">link</a><script>alert(1)</script>' }
    const original = payload.message_html
    const host = render(payload.message_html)
    expect(host.querySelectorAll('b, i, u, s, pre, code, blockquote')).toHaveLength(7)
    expect(host.querySelector('img')?.getAttribute('src')).toBe('/uploads/a.png')
    expect(host.querySelector('a')?.getAttribute('href')).toBe('https://example.test')
    expect(host.querySelector('a')?.getAttribute('rel')).toBe('noopener noreferrer')
    expect(payload.message_html).toBe(original)
  })

  it('rejects data images and retains relative links', () => {
    const host = render('<img src="data:image/svg+xml,test"><a href="/help">help</a><a href="#section">section</a>')
    expect(host.querySelector('img')?.hasAttribute('src')).toBe(false)
    expect(Array.from(host.querySelectorAll('a'), (a) => a.getAttribute('href'))).toEqual(['/help', '#section'])
    expect(sanitizeBroadcastHtml('')).toBe('')
  })
})
