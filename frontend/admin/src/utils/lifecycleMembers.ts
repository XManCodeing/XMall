import type { MembershipLifecycleTask as Task } from '@/api/telegramMembership'

export interface LifecycleMemberView {
  telegram_id: string
  tasks: Task[]
  latest: Task[]
  memberEvidence: Task[]
  chatIds: string[]
  historyPending: number
}

/** Compare positive decimal IDs without converting to an unsafe JavaScript number.
 * @param left First task ID. @param right Second task ID. @returns Ascending numeric order.
 */
export function compareLifecycleIDs(left: string, right: string): number {
  const first = left.replace(/^0+/, '') || '0', second = right.replace(/^0+/, '') || '0'
  return first.length - second.length || (first < second ? -1 : first > second ? 1 : 0)
}

/** 判断任务是否只是未做新观测的结束扫描，不能据此抹去同周期已确认的移出。
 * @param task 服务端任务。@returns 是否可沿同一周期回溯成员证据。
 */
function isUnobservedNoop(task: Task): boolean {
  return task.state === 'succeeded' && task.phase === 'not_required'
    && task.member_outcome === 'not_required' && !task.review_reason
    && !task.ban_confirmed && !task.ban_outcome && !task.unban_outcome && !task.ban_blocked
    && (!task.observed_at || task.observed_at.startsWith('0001-'))
}

/** 仅在处置周期、权益版本及机器人路由完全相同时保留已经确认的成员执行证据。
 * @param latest 当前群最新任务。@param ordered 按 ID 倒序的成员历史。@returns 原始任务，不合成成功状态。
 */
function memberEvidence(latest: Task, ordered: Task[]): Task {
  if (!isUnobservedNoop(latest)) return latest
  const fences = ['cleanup_cycle_id', 'entitlement_version', 'route_version', 'bot_id', 'target_id'] as const
  if (fences.some(field => !latest[field] || latest[field] === '0')) return latest
  for (const previous of ordered) {
    if (previous.chat_id !== latest.chat_id || compareLifecycleIDs(previous.id, latest.id) >= 0) continue
    // 任何周期、路由或权益变化都是边界；不能跳过后再捡更早的成功记录。
    if (previous.telegram_id !== latest.telegram_id || fences.some(field => previous[field] !== latest[field])) return latest
    if (isUnobservedNoop(previous)) continue
    return previous.ban_confirmed === true ? previous : latest
  }
  return latest
}

/** Project flat tasks into members and retain every historical task, counting each chat only once.
 * @param tasks Server tasks from a member-numbered page. @returns Per-member latest-per-chat summary evidence.
 */
export function lifecycleMemberViews(tasks: Task[]): LifecycleMemberView[] {
  const members = new Map<string, Task[]>()
  for (const task of tasks) {
    const memberTasks = members.get(task.telegram_id) || []
    memberTasks.push(task)
    members.set(task.telegram_id, memberTasks)
  }
  return [...members].map(([telegram_id, memberTasks]) => {
    const ordered = [...memberTasks].sort((left, right) => compareLifecycleIDs(right.id, left.id))
    const chats = new Map<string, Task>()
    for (const task of ordered) if (!chats.has(task.chat_id)) chats.set(task.chat_id, task)
    const latest = [...chats.values()]
    const latestIDs = new Set(latest.map(task => task.id))
    // Historical tasks may still be running or awaiting review; never hide them behind a successful latest result.
    const historyPending = ordered.filter(task => !latestIDs.has(task.id) && !['succeeded', 'not_required'].includes(task.state)).length
    return { telegram_id, tasks: ordered, latest, memberEvidence: latest.map(task => memberEvidence(task, ordered)), chatIds: [...chats.keys()], historyPending }
  })
}
