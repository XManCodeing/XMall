import { afterEach, describe, expect, it, vi } from 'vitest'
import { membershipGuideTarget, membershipPageSteps } from './membershipPageGuide'
import { membershipPageGuideMessages } from '@/i18n/membershipPageGuide'

afterEach(() => { document.body.innerHTML = ''; vi.restoreAllMocks() })

/** 创建可见或隐藏锚点。@param name 名称 @param visible 是否可见 @returns 元素 */
function anchor(name: string, visible = true) {
  const element = document.createElement('div')
  element.dataset.guide = name
  document.body.append(element)
  vi.spyOn(element, 'getClientRects').mockReturnValue((visible ? [{}] : []) as unknown as DOMRectList)
  return element
}

describe('membership page guide', () => {
  it('六个页面均有有序锚点和三语文案', () => {
    for (const path of ['/telegram-bot/groups', '/telegram-bot/membership/products', '/telegram-bot/membership/orders', '/telegram-bot/support/sessions', '/telegram-bot/support/tasks', '/telegram-bot/recovery']) {
      const steps = membershipPageSteps(path)
      expect(steps.length).toBeGreaterThanOrEqual(4)
      expect(new Set(steps.map(step => step.target)).size).toBe(steps.length)
      for (const step of steps) for (const messages of Object.values(membershipPageGuideMessages)) expect(messages).toHaveProperty(step.key)
    }
    expect(membershipPageSteps('/telegram-bot/settings')).toEqual([])
    expect(membershipPageSteps('/orders')).toEqual([])
  })
  it('跳过隐藏缓存元素，缺少锚点不抛错', () => {
    anchor('groups-create', false)
    const visible = anchor('groups-create')
    expect(membershipGuideTarget('groups-create')).toBe(visible)
    expect(membershipGuideTarget('missing')).toBeUndefined()
  })
  it.each(['access', 'reviews', 'lifecycle'])('订单 %s 子页使用当前步骤，不读取隐藏订单列表', tab => {
    anchor(`orders-${tab}`)
    const steps = membershipPageSteps('/telegram-bot/membership/orders')
    expect(steps.map(step => step.target)).toEqual(['orders-tabs', `orders-${tab}`])
    for (const messages of Object.values(membershipPageGuideMessages)) expect(messages).toHaveProperty(steps[1]!.key)
  })
})
