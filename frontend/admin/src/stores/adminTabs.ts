import { defineStore } from 'pinia'

export interface AdminTab { path: string; fullPath: string }

/** 按路径合并标签，保留顺序和最新查询地址。@param tabs 已访问标签 @returns 去重列表 */
export function normalizeTabs(tabs: AdminTab[]): AdminTab[] {
  const entries = new Map<string, AdminTab>()
  for (const tab of tabs) entries.set(tab.path, tab)
  return [...entries.values()]
}

/** 后台标签仅存内存；缓存名单随关闭同步更新，不落盘业务地址。 */
export const useAdminTabsStore = defineStore('adminTabs', {
  state: () => ({ tabs: [] as AdminTab[], epoch: 0, refreshing: '' }),
  actions: {
    /** 登记成功路由。@param tab 实际访问位置 @returns void */
    add(tab: AdminTab) { this.tabs = normalizeTabs([...this.tabs, tab]) },
    /** 保留指定标签；由调用方先完成导航保护。@param paths 保留路径 @returns void */
    retain(paths: string[]) { this.tabs = this.tabs.filter(tab => paths.includes(tab.path)) },
    /** 清空会话标签和实例。@returns void */
    clear() { this.tabs = []; this.refreshing = ''; this.epoch++ },
  },
})
