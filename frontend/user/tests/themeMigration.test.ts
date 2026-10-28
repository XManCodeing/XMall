import assert from 'node:assert/strict'
import test from 'node:test'
import 'vue'

for (const [label, initial, expected] of [
  ['legacy dark', { dujiao_theme: 'dark' }, 'dark'],
  ['legacy light', { dujiao_theme: 'light' }, 'light'],
  ['new preference wins', { xmall_theme: 'light', dujiao_theme: 'dark' }, 'light'],
  ['invalid new preference migrates legacy', { xmall_theme: 'invalid', dujiao_theme: 'dark' }, 'dark'],
  ['invalid legacy falls back to system', { dujiao_theme: 'invalid' }, 'light'],
] as const) {
  test(`theme migration: ${label}`, async () => {
    const values = new Map<string, string>(Object.entries(initial))
    const classes = new Set<string>()
    const previous = Object.fromEntries(['window', 'document', 'localStorage'].map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]))
    Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => values.set(key, value),
      removeItem: (key: string) => values.delete(key),
    } })
    Object.defineProperty(globalThis, 'window', { configurable: true, value: {
      matchMedia: () => ({ matches: false }),
    } })
    Object.defineProperty(globalThis, 'document', { configurable: true, value: {
      documentElement: { classList: { toggle: (name: string, on: boolean) => on ? classes.add(name) : classes.delete(name) } },
    } })
    try {
      const { useTheme } = await import(`../src/utils/theme.ts?migration=${encodeURIComponent(label)}`)
      const { theme, toggleTheme, setTemplateDefault } = useTheme()
      assert.equal(theme.value, expected)
      assert.equal(classes.has('dark'), expected === 'dark')
      if (label !== 'invalid legacy falls back to system') {
        assert.equal(values.get('xmall_theme'), expected)
        setTemplateDefault(expected === 'dark' ? 'light' : 'dark')
        assert.equal(theme.value, expected)
        if (label !== 'new preference wins') assert.equal(values.has('dujiao_theme'), false)
      } else {
        assert.equal(values.has('xmall_theme'), false)
      }
      toggleTheme()
      assert.equal(values.get('xmall_theme'), expected === 'dark' ? 'light' : 'dark')
    } finally {
      for (const [key, descriptor] of Object.entries(previous)) {
        if (descriptor) Object.defineProperty(globalThis, key, descriptor)
        else Reflect.deleteProperty(globalThis, key)
      }
    }
  })
}
