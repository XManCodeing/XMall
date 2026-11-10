import { describe, expect, it } from 'vitest'

const components = import.meta.glob('../**/*.vue', { query: '?raw', import: 'default', eager: true }) as Record<string, string>

describe('admin overlay architecture', () => {
  it('keeps dialog primitives and full-page modal masks inside the shared UI layer', () => {
    const violations: string[] = []
    for (const [path, source] of Object.entries(components)) {
      if (path.includes('/components/ui/')) continue
      if (/class=["'][^"']*\bfixed\s+inset-0\b/.test(source)
        || /(?<![\w.])(?:window\.)?(?:prompt|alert|confirm)\s*\(/.test(source)
        || /<(?:DialogPortal|DialogOverlay|AlertDialogPortal|AlertDialogOverlay|Teleport|teleport)\b/.test(source)) {
        violations.push(path)
      }
    }
    expect(violations).toEqual([])
  })

  it('defaults shared dialogs to automatic page scope and retains only mobile navigation as explicit global', () => {
    expect(components['../components/ui/dialog/Dialog.vue']).toContain("scope: 'auto'")
    const globalComponents = Object.entries(components)
      .filter(([, source]) => /scope=["']global["']/.test(source))
      .map(([path]) => path)
    expect(globalComponents).toEqual(['../layouts/AdminLayout.vue'])
    expect(components['../layouts/AdminLayout.vue']).toContain('<ConfirmDialogHost />')
    expect(components['../App.vue']).not.toContain('<ConfirmDialogHost />')
  })
})
