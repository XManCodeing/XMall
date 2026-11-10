export const recoveryBotRouteMessages = {
  'zh-CN': {
    title: '会员目标群映射（可选）', hint: '未映射目标保持原群；仅可选择源 Bot 当前会员目标，客服群不能在此换群。最多 100 个目标，预检不执行迁移。',
    load: '读取会员目标', target: '会员目标', old: '原群 Chat ID', destination: '目标群 Chat ID', add: '选择目标', remove: '移除',
    invalid: '映射无效：请选择已读取目标，填写不重复的负 int64 群 ID；最多 100 项。', loadFailed: '读取会员目标失败，请重新读取。',
    forbidden: '填写映射需要群列表读取权限；不填映射仍可按原群预检。', loading: '加载中…', empty: '当前页暂无可选会员目标',
    previous: '上一页', next: '下一页', page: '第 {page} 页', snapshot: '已保存的 Bot 目标路由', purpose: '用途', membership: '会员群', support: '客服群', unknown: '未知用途',
    version: '配置版本', routeVersion: '路由版本', invalidated: '变更源 Bot 会清除全部映射。目标 ID 与版本以服务端预检为准。',
  },
  'zh-TW': {
    title: '會員目標群組對應（選填）', hint: '未對應目標保持原群組；僅可選擇來源 Bot 目前會員目標，客服群組不能在此換群組。最多 100 個目標，預檢不執行遷移。',
    load: '讀取會員目標', target: '會員目標', old: '原群組 Chat ID', destination: '目標群組 Chat ID', add: '選擇目標', remove: '移除',
    invalid: '對應無效：請選擇已讀取目標，填寫不重複的負 int64 群組 ID；最多 100 項。', loadFailed: '讀取會員目標失敗，請重新讀取。',
    forbidden: '填寫對應需要群組列表讀取權限；不填對應仍可按原群組預檢。', loading: '載入中…', empty: '目前頁面暫無可選會員目標',
    previous: '上一頁', next: '下一頁', page: '第 {page} 頁', snapshot: '已儲存的 Bot 目標路由', purpose: '用途', membership: '會員群組', support: '客服群組', unknown: '未知用途',
    version: '設定版本', routeVersion: '路由版本', invalidated: '變更來源 Bot 會清除全部對應。目標 ID 與版本以伺服器預檢為準。',
  },
  'en-US': {
    title: 'Membership destination mapping (optional)', hint: 'Unmapped targets keep their original chat. Select only current membership targets of the source Bot; support chats cannot change here. Maximum 100 targets. Preflight does not execute migration.',
    load: 'Read membership targets', target: 'Membership target', old: 'Original Chat ID', destination: 'Destination Chat ID', add: 'Select target', remove: 'Remove',
    invalid: 'Invalid mapping: select a loaded target and enter unique negative int64 chat IDs, up to 100 entries.', loadFailed: 'Could not read membership targets. Try again.',
    forbidden: 'Mapping requires group-list read permission. You can still preflight original chats without mappings.', loading: 'Loading…', empty: 'No eligible membership targets on this page',
    previous: 'Previous', next: 'Next', page: 'Page {page}', snapshot: 'Saved Bot target routes', purpose: 'Purpose', membership: 'Membership', support: 'Support', unknown: 'Unknown purpose',
    version: 'Config version', routeVersion: 'Route version', invalidated: 'Changing the source Bot clears all mappings. The server derives target identity and versions during preflight.',
  },
}
