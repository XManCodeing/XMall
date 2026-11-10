export const membershipGroupStates = ['processing', 'available', 'recovering', 'unknown', 'expired', 'revoked', 'identity_mismatch', 'unavailable', 'member'] as const
export type MembershipGroupState = typeof membershipGroupStates[number]

export interface MembershipGroup {
  target_id: string
  title: string
  beneficiary_telegram_id: string
  permanent: boolean
  expires_at?: string
  state: MembershipGroupState
  member_status?: string
  can_request: boolean
}
export interface MembershipGroupPage { items: MembershipGroup[]; next_cursor?: string }
export interface MembershipInvitation { state: MembershipGroupState; invite_url?: string; expires_at?: string }

/** 将服务端未知状态保守降级。@param value 原始状态。@returns 已知状态或 unknown。 */
export function membershipState(value: unknown): MembershipGroupState {
  return membershipGroupStates.includes(value as MembershipGroupState) ? value as MembershipGroupState : 'unknown'
}

/** 校验字符串 ID，避免大整数精度丢失。@param value 原始 ID。@returns 是否为正整数字符串。 */
export function isMembershipId(value: unknown): value is string {
  return typeof value === 'string' && /^[1-9]\d*$/.test(value)
}

/** 只允许 Telegram 私有申请链接，不接受查询、片段、凭据或任意跳转。@param value 链接。@returns 安全链接或空串。 */
export function safeMembershipInvitation(value: unknown): string {
  return typeof value === 'string' && !/\s/.test(value) && /^https:\/\/t\.me\/(?:\+|joinchat\/)[A-Za-z0-9_-]+$/.test(value) ? value : ''
}

/** 只有明确可用且服务端允许时可申请；未知结果不能盲重试。@param group 群权益。@returns 是否允许显式申请。 */
export function canRequestMembership(group: MembershipGroup): boolean {
  return group.state === 'available' && group.can_request
}

/** 校验分页响应；不保留 GET 中意外出现的邀请字段。@param value envelope 内的 data。@returns 规范化分页。 */
export function parseMembershipPage(value: unknown): MembershipGroupPage {
  const page = value as MembershipGroupPage | null
  if (!page || !Array.isArray(page.items) || (page.next_cursor !== undefined && typeof page.next_cursor !== 'string')) {
    throw new Error('Invalid membership group response')
  }
  const items = page.items.map((group) => {
    if (!group || !isMembershipId(group.target_id) || !isMembershipId(group.beneficiary_telegram_id)
      || typeof group.title !== 'string' || typeof group.permanent !== 'boolean' || typeof group.can_request !== 'boolean'
      || (group.expires_at !== undefined && (typeof group.expires_at !== 'string' || !Number.isFinite(Date.parse(group.expires_at))))) {
      throw new Error('Invalid membership group item')
    }
    return {
      target_id: group.target_id, title: group.title, beneficiary_telegram_id: group.beneficiary_telegram_id,
      permanent: group.permanent, expires_at: group.expires_at, state: membershipState(group.state),
      member_status: typeof group.member_status === 'string' ? group.member_status : undefined,
      can_request: group.can_request,
    }
  })
  return { items, next_cursor: page.next_cursor }
}

/** 校验申请结果；未完成不暴露链接或伪报成功。@param value 原始响应。@returns 安全申请结果。 */
export function parseMembershipInvitation(value: unknown): MembershipInvitation {
  const invitation = value as MembershipInvitation | null
  if (!invitation || typeof invitation.state !== 'string') throw new Error('Invalid membership invitation response')
  const state = membershipState(invitation.state)
  if (state !== 'available') return { state }
  const invite_url = safeMembershipInvitation(invitation.invite_url)
  const expires_at = invitation.expires_at
  if (!invite_url || (expires_at !== undefined && (typeof expires_at !== 'string' || !Number.isFinite(Date.parse(expires_at)) || Date.parse(expires_at) <= Date.now()))) {
    return { state: 'unknown' }
  }
  return { state, invite_url, expires_at }
}
