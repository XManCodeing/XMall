import { describe, expect, it } from 'vitest'
import { badgeVariants } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

const pages = import.meta.glob<string>('../views/admin/**/*.vue', {
  query: '?raw', import: 'default', eager: true,
})

const tableSource = import.meta.glob<string>('../components/ui/table/{Table,TableCell}.vue', {
  query: '?raw', import: 'default', eager: true,
})

// Check the short state badges, not arbitrary rounded labels, text or buttons.
const stateBindings = /\b(?:(?:statusClass|userStatusClass|statusBadgeClass|cardSecretStatusClass|orderStatusClass|paymentStatusClass|walletDirectionClass|settlementClass|verificationClass|notificationLogStatusClass|buildResellerProductSettingStatusClass)\s*\(|is_active|is_published)/

describe('short status badge layout', () => {
  it('keeps all shared Badge variants on one line without shrinking their icons', () => {
    for (const variant of ['default', 'secondary', 'success', 'destructive', 'outline'] as const) {
      const classes = cn(badgeVariants({ variant }), 'rounded-full').split(/\s+/)
      expect(classes).toContain('whitespace-nowrap')
      expect(classes).toContain('shrink-0')
      expect(classes).toContain('[&>svg]:shrink-0')
    }
  })

  it('protects hand-written state badges across lists and nested tables', () => {
    let checked = 0
    for (const [path, source] of Object.entries(pages)) {
      const tables = [...source.matchAll(/<Table(?:\s[^>]*)?>[\s\S]*?<\/Table>/g)].map(match => match[0]).join('\n')
      for (const match of tables.matchAll(/<span\b([^>]*?)>/gs)) {
        const attrs = match[1]!
        if (!stateBindings.test(attrs)) continue
        const classes = attrs.match(/(?:^|\s)class="([^"]*)"/)?.[1]?.split(/\s+/) || []
        expect(classes, `${path}: ${match[0]}`).toContain('whitespace-nowrap')
        expect(classes, `${path}: ${match[0]}`).toContain('shrink-0')
        checked++
      }
    }
    expect(checked).toBeGreaterThan(20)
  })

  it('preserves long user text wrapping and scrollable table containers', () => {
    const users = pages['../views/admin/Users.vue']!
    expect(users).toMatch(/class="[^"]*break-all[^"]*">\{\{ user.email/)
    expect(users).toMatch(/class="[^"]*break-words[^"]*">\{\{ user.display_name/)
    const products = pages['../views/admin/Products.vue']!
    expect(products).toContain('flex flex-wrap')
    expect(tableSource['../components/ui/table/Table.vue']).toContain('overflow-auto')
    expect(tableSource['../components/ui/table/TableCell.vue']).not.toContain('whitespace-nowrap')
  })
})
