/** Session-only operational labels; task translations remain independently owned. */
export const supportSessionMessages = {
  'zh-CN': {
    unreplied: '未回复', replied: '无待回复', blocked: '黑名单', notBlocked: '未拉黑', permanent: '长期有效', expires: '有效至 {time}',
    replyStatus: '回复状态', blockStatus: '黑名单状态', failure: '投递异常', topic: '当前话题', topicYes: '有可信话题', topicNo: '无可信话题',
    any: '任一方向异常', upstream: '上行异常', downstream: '下行异常', none: '无投递异常',
    upstreamSummary: '上行：失败 {failed} / 未知 {unknown}', downstreamSummary: '下行：失败 {failed} / 未知 {unknown}',
    since: '待回复起点', lastCustomer: '最近上行送达', lastReply: '最近回复送达', openTopic: '打开话题',
    semantics: '顾客消息送达客服群后开始待回复，成功送达顾客的回复清除此前待回复；内部备注不计回复。失败与未知分别统计。',
    topicHint: '仅提供当前绑定话题入口；回复、拉黑及关闭仍在群内操作，并校验当前群成员资格。',
  },
  'zh-TW': {
    unreplied: '未回覆', replied: '無待回覆', blocked: '黑名單', notBlocked: '未封鎖', permanent: '長期有效', expires: '有效至 {time}',
    replyStatus: '回覆狀態', blockStatus: '黑名單狀態', failure: '投遞異常', topic: '目前話題', topicYes: '有可信話題', topicNo: '無可信話題',
    any: '任一方向異常', upstream: '上行異常', downstream: '下行異常', none: '無投遞異常',
    upstreamSummary: '上行：失敗 {failed} / 未知 {unknown}', downstreamSummary: '下行：失敗 {failed} / 未知 {unknown}',
    since: '待回覆起點', lastCustomer: '最近上行送達', lastReply: '最近回覆送達', openTopic: '開啟話題',
    semantics: '顧客訊息送達客服群後開始待回覆，成功送達顧客的回覆清除先前待回覆；內部備註不計回覆。失敗與未知分別統計。',
    topicHint: '僅提供目前綁定話題入口；回覆、封鎖及關閉仍在群內操作，並驗證目前群組成員資格。',
  },
  'en-US': {
    unreplied: 'Awaiting reply', replied: 'No reply pending', blocked: 'Blocked', notBlocked: 'Not blocked', permanent: 'No expiry', expires: 'Until {time}',
    replyStatus: 'Reply status', blockStatus: 'Block status', failure: 'Delivery issues', topic: 'Current topic', topicYes: 'Trusted topic', topicNo: 'No trusted topic',
    any: 'Either direction', upstream: 'Upstream issues', downstream: 'Downstream issues', none: 'No delivery issues',
    upstreamSummary: 'Upstream: {failed} failed / {unknown} unknown', downstreamSummary: 'Downstream: {failed} failed / {unknown} unknown',
    since: 'Waiting since', lastCustomer: 'Last customer delivery', lastReply: 'Last reply delivery', openTopic: 'Open topic',
    semantics: 'Waiting starts when a customer message reaches support. A reply delivered to the customer clears earlier waiting messages. Internal notes do not count. Failed and unknown outcomes are counted separately.',
    topicHint: 'Only the current bound topic is linked. Reply, block and close actions remain in the group and require current membership.',
  },
}
