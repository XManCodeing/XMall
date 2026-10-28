import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const source = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')

test('storefront default branding is localized in all supported languages', () => {
  for (const [locale, expected] of [['zh-CN', 'X商城'], ['zh-TW', 'X商城'], ['en-US', 'XMall']]) {
    const messages = JSON.parse(source(`../src/i18n/locales/${locale}.json`))
    assert.equal(messages.common.siteName, expected)
  }
})

test('both clients ship the same woven XMall mark and retain historical icons', () => {
  for (const name of ['xmall.svg', 'xmall-dark.svg']) {
    assert.equal(source(`../../user/public/${name}`), source(`../../admin/public/${name}`))
  }
  for (const client of ['user', 'admin']) {
    const icon = source(`../../${client}/public/xmall.svg`)
    assert.ok(icon.includes('<title id="title">XMall</title>'))
    assert.ok(icon.includes('Two mirrored M ribbons'))
    assert.ok(icon.includes('viewBox="0 0 224 224"'))
    assert.ok(!icon.includes('<script'))
    assert.notEqual(icon, source(`../../${client}/public/dj.svg`))
    assert.ok(source(`../../${client}/public/dj.svg`).includes('<svg'))
    const component = source(`../../${client}/src/components/BrandMark.vue`)
    assert.ok(component.includes('dark:hidden'))
    assert.ok(component.includes('dark:block'))
    assert.ok(!component.includes('matchMedia'))
    assert.ok(!component.includes('localStorage'))
  }
  assert.ok(source('../../admin/src/components/BrandMark.vue').includes("adminUrl('/xmall.svg')"))
})

test('monochrome exports preserve transparent weave separation', () => {
  for (const name of ['xmall-black.svg', 'xmall-white.svg']) {
    const icon = source(`../public/${name}`)
    assert.ok(icon.includes('<mask id="weave"'))
    assert.ok(icon.includes('mask="url(#weave)"'))
  }
})

test('Telegram callback registration and cleanup share the renamed callback identifiers', () => {
  for (const [path, name] of [
    ['../src/composables/useLogin.ts', '__xmallUserTelegramLogin'],
    ['../src/views/personal/SecurityPanel.vue', '__xmallSecurityTelegramBind'],
  ]) {
    const text = source(path!)
    assert.ok(text.includes(name!))
    assert.ok(text.includes('telegramCallbackName'))
    assert.ok(!text.includes('__dujiao'))
  }
})
