import assert from 'node:assert/strict'
import test from 'node:test'
import 'vue'

// Exercise the single theme entry point; templates must never own persistence.
test('template defaults preserve explicit preferences and update the root synchronously', async () => {
  const values = new Map<string, string>()
  const classes = new Set<string>()
  const previous = {
    window: Object.getOwnPropertyDescriptor(globalThis, 'window'),
    document: Object.getOwnPropertyDescriptor(globalThis, 'document'),
    localStorage: Object.getOwnPropertyDescriptor(globalThis, 'localStorage'),
  }
  Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => values.set(key, value),
  } })
  Object.defineProperty(globalThis, 'window', { configurable: true, value: {
    matchMedia: () => ({ matches: true }),
  } })
  Object.defineProperty(globalThis, 'document', { configurable: true, value: {
    documentElement: { classList: { toggle: (name: string, on: boolean) => on ? classes.add(name) : classes.delete(name) } },
  } })
  try {
    const { useTheme } = await import('../src/utils/theme.ts')
    const { theme, toggleTheme, setTemplateDefault } = useTheme()
    assert.equal(theme.value, 'dark')
    assert.equal(classes.has('dark'), true)
    assert.equal(values.has('xmall_theme'), false)
    setTemplateDefault('light')
    assert.equal(theme.value, 'light')
    assert.equal(classes.has('dark'), false)
    assert.equal(values.has('xmall_theme'), false)
    setTemplateDefault()
    assert.equal(theme.value, 'dark')
    toggleTheme()
    assert.equal(values.get('xmall_theme'), 'light')
    setTemplateDefault('dark')
    assert.equal(theme.value, 'light')
    toggleTheme()
    assert.equal(classes.has('dark'), true)
    setTemplateDefault('light')
    assert.equal(theme.value, 'dark')
    assert.equal(values.get('xmall_theme'), 'dark')
  } finally {
    for (const [key, descriptor] of Object.entries(previous)) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor)
      else Reflect.deleteProperty(globalThis, key)
    }
  }
})
