import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { ref } from 'vue'
import * as policy from '../src/utils/membershipGroups.ts'

const group = { target_id: '9007199254740993', title: '会员群', beneficiary_telegram_id: '9007199254740995', permanent: true, state: 'available' as const, can_request: true }

/** 执行真实模块并注入边界依赖。@param path 模块路径。@param dependencies 测试依赖。@returns 模块导出。 */
function loadModule(path: string, dependencies: Record<string, unknown>) {
  const source = readFileSync(new URL(path, import.meta.url), 'utf8')
  const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } })
  const exports: Record<string, any> = {}
  runInNewContext(compiled.outputText, { exports, require: (name: string) => dependencies[name], console })
  return exports
}

/** 建立真实 composable 测试上下文。@param api 测试 API。@returns 状态和生命周期控制。 */
function fixture(api: Record<string, unknown>) {
  let mount = () => {}
  let unmount = () => {}
  const exports = loadModule('../src/composables/useMembershipGroups.ts', {
    vue: { ref, onMounted: (callback: () => void) => { mount = callback }, onBeforeUnmount: (callback: () => void) => { unmount = callback } },
    '../api/membershipGroups': { membershipGroupsAPI: api }, '../utils/membershipGroups': policy,
  })
  return { state: exports.useMembershipGroups(), mount: () => mount(), unmount: () => unmount() }
}

test('only exact HTTPS Telegram private invitation URLs are accepted', () => {
  for (const url of ['https://t.me/+Abc_12-x', 'https://t.me/joinchat/Abc_12-x']) assert.equal(policy.safeMembershipInvitation(url), url)
  for (const url of ['http://t.me/+abc', 'https://t.me.evil/+abc', 'https://evil@t.me/+abc', 'https://t.me/+abc?start=1', 'https://t.me/+abc#fragment', 'https://t.me/%2Babc', 'https://t.me/public', 'javascript:alert(1)', 'https://t.me:443/+abc', '//t.me/+abc', 'https://t.me/+abc\n']) {
    assert.equal(policy.safeMembershipInvitation(url), '')
  }
})

test('IDs remain strings; GET discards invite fields and unknown state fails closed', () => {
  const page = policy.parseMembershipPage({ items: [{ ...group, invite_url: 'https://t.me/+secret' }], next_cursor: 'opaque' })
  assert.equal(page.items[0]?.target_id, group.target_id)
  assert.equal(page.items[0]?.beneficiary_telegram_id, group.beneficiary_telegram_id)
  assert.equal('invite_url' in page.items[0]!, false)
  assert.equal(page.items[0]?.expires_at, undefined)
  assert.throws(() => policy.parseMembershipPage({ items: [{ ...group, target_id: 123 }] }))
  assert.throws(() => policy.parseMembershipPage({ items: [{ ...group, beneficiary_telegram_id: 123 }] }))
  assert.throws(() => policy.parseMembershipPage({ items: [{ ...group, expires_at: 'bad' }] }))
  assert.equal(policy.parseMembershipPage({ items: [{ ...group, state: 'future-state' }] }).items[0]?.state, 'unknown')
  for (const state of policy.membershipGroupStates) assert.equal(policy.canRequestMembership({ ...group, state }), state === 'available')
  assert.equal(policy.canRequestMembership({ ...group, can_request: false }), false)
})

test('unfinished, invalid and expired invitations never expose an entry', () => {
  for (const state of policy.membershipGroupStates.filter((state) => state !== 'available')) {
    assert.equal(policy.parseMembershipInvitation({ state, invite_url: 'https://t.me/+secret' }).invite_url, undefined)
  }
  for (const value of [{ state: 'available' }, { state: 'available', invite_url: 'https://evil.test' },
    { state: 'available', invite_url: 'https://t.me/+abc', expires_at: '2000-01-01T00:00:00Z' }]) {
    assert.equal(policy.parseMembershipInvitation(value).state, 'unknown')
  }
  assert.equal(policy.parseMembershipInvitation({ state: 'available', invite_url: 'https://t.me/+abc' }).invite_url, 'https://t.me/+abc')
})

test('HTTP adapter uses real authenticated contract, cursor/limit, no-store and empty POST', async () => {
  const calls: any[] = []
  const api = loadModule('../src/api/membershipGroups.ts', {
    './client': { userApi: {
      get: async (...args: unknown[]) => { calls.push(['GET', ...args]); return { data: { status_code: 0, data: { items: [group], next_cursor: 'next' } } } },
      post: async (...args: unknown[]) => { calls.push(['POST', ...args]); return { data: { status_code: 0, data: { state: 'processing' } } } },
    } }, '../utils/membershipGroups': policy,
  }).membershipGroupsAPI
  await api.list('cursor', 30)
  await api.requestInvitation(group.target_id)
  assert.equal(calls[0][1], '/user/membership-groups')
  assert.equal(calls[0][2].params.cursor, 'cursor')
  assert.equal(calls[0][2].params.limit, 30)
  assert.equal(calls[1][1], `/user/membership-groups/${group.target_id}/invitation`)
  assert.equal(JSON.stringify(calls[1][2]), '{}')
  assert.equal(calls[1][3].cache, 'no-store')
  assert.equal(calls[1][3].sensitiveResponse, true)
  await assert.rejects(() => api.requestInvitation(123))
  await assert.rejects(() => api.list(undefined, 101))
  assert.equal(calls.length, 2)
})

test('mount/refresh/pagination never POST; explicit request exposes link only in memory', async () => {
  let postCount = 0
  const cursors: unknown[] = []
  const view = fixture({
    list: async (cursor: unknown) => { cursors.push(cursor); return { items: [{ ...group }], next_cursor: cursor ? undefined : 'next' } },
    requestInvitation: async () => { postCount++; return { state: 'available', invite_url: 'https://t.me/+abc' } },
  })
  view.mount()
  await new Promise((resolve) => setImmediate(resolve))
  assert.equal(postCount, 0)
  await view.state.load(true)
  assert.deepEqual(cursors, [undefined, 'next'])
  assert.equal(view.state.items.value.length, 1)
  await view.state.requestInvitation(view.state.items.value[0])
  assert.equal(postCount, 1)
  assert.equal(view.state.invitations.value[group.target_id].invite_url, 'https://t.me/+abc')
  await view.state.load()
  assert.equal(Object.keys(view.state.invitations.value).length, 0)
  view.unmount()
})

test('network uncertainty cannot be retried and incomplete work never becomes success', async () => {
  let count = 0
  const view = fixture({ list: async () => ({ items: [{ ...group }] }), requestInvitation: async () => { count++; throw new Error('timeout') } })
  await view.state.load()
  const item = view.state.items.value[0]
  await view.state.requestInvitation(item)
  await view.state.requestInvitation(item)
  assert.equal(item.state, 'unknown')
  assert.equal(count, 1)
  assert.equal(Object.keys(view.state.invitations.value).length, 0)
  const processing = fixture({ list: async () => ({ items: [{ ...group }] }), requestInvitation: async () => ({ state: 'processing' }) })
  await processing.state.load()
  await processing.state.requestInvitation(processing.state.items.value[0])
  assert.equal(processing.state.items.value[0].state, 'processing')
  assert.equal(processing.state.invitations.value[group.target_id].invite_url, undefined)
})

test('duplicate clicks, late unmount responses and expired entry clicks are blocked', async () => {
  let finish: (value: unknown) => void = () => {}
  let count = 0
  const view = fixture({ list: async () => ({ items: [{ ...group }] }), requestInvitation: () => { count++; return new Promise((resolve) => { finish = resolve }) } })
  await view.state.load()
  const item = view.state.items.value[0]
  const pending = view.state.requestInvitation(item)
  await view.state.requestInvitation(item)
  assert.equal(count, 1)
  view.unmount()
  finish({ state: 'available', invite_url: 'https://t.me/+abc' })
  await pending
  assert.equal(Object.keys(view.state.invitations.value).length, 0)
  let prevented = false
  view.state.invitations.value[group.target_id] = { state: 'available', invite_url: 'https://t.me/+abc', expires_at: '2000-01-01' }
  view.state.checkInvitation(item, { preventDefault: () => { prevented = true } })
  assert.equal(prevented, true)
  assert.equal(item.state, 'recovering')
})

test('failed refresh clears stale authorization and invites', async () => {
  let fail = false
  const view = fixture({ list: async () => { if (fail) throw new Error('offline'); return { items: [{ ...group }] } } })
  await view.state.load()
  fail = true
  await view.state.load()
  assert.equal(view.state.items.value.length, 0)
  assert.equal(view.state.error.value, true)
})

test('three templates share protected route, navigation, theme tokens and three locales', () => {
  const read = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')
  const router = read('../src/router/index.ts')
  assert.match(router, /path: '\/me\/membership-groups',[\s\S]*?requiresUserAuth: true/)
  for (const path of ['../src/views/PersonalCenter.vue', '../src/templates/vault/PersonalCenter.vue']) assert.match(read(path), /MembershipGroupsPanel/)
  assert.match(read('../src/templates/orbit/PersonalCenter.vue'), /ClassicPersonalCenter/)
  assert.match(read('../src/views/personal/OrdersPanel.vue'), /to="\/me\/membership-groups"/)
  const panel = read('../src/views/personal/MembershipGroupsPanel.vue')
  assert.match(panel, /min-h-11/)
  assert.match(panel, /safe-area-inset-bottom/)
  assert.match(panel, /contact\?\.email/)
  assert.doesNotMatch(panel, /#[0-9a-fA-F]{3,8}\b|localStorage|sessionStorage|console\./)
  for (const language of ['zh-CN', 'zh-TW', 'en-US']) {
    const messages = JSON.parse(read(`../src/i18n/locales/${language}.json`)).membershipGroups
    assert.ok(messages.title)
    for (const state of policy.membershipGroupStates) assert.ok(messages.states[state])
  }
})
