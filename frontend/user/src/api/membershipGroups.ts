import { userApi } from './client'
import { isMembershipId, parseMembershipPage, parseMembershipInvitation } from '../utils/membershipGroups'

// 邀请属于敏感临时数据：禁止浏览器 HTTP 缓存，错误日志不得包含服务端原文。
const sensitiveOptions = { cache: 'no-store' as const, sensitiveResponse: true, silentBusinessError: true }

export const membershipGroupsAPI = {
  /** 只读查询，不创建邀请。@param cursor 服务端游标。@param limit 页大小。@returns 已校验的群权益分页。 */
  async list(cursor?: string, limit = 20) {
    if ((cursor !== undefined && typeof cursor !== 'string') || !Number.isInteger(limit) || limit < 1 || limit > 100) {
      throw new Error('Invalid membership pagination')
    }
    const response = await userApi.get('/user/membership-groups', { ...sensitiveOptions, params: { cursor, limit } })
    return parseMembershipPage(response.data.data)
  },
  /** 用户显式申请当前群入口。@param targetId 逻辑群字符串 ID。@returns 真实状态及安全临时链接。 */
  async requestInvitation(targetId: string) {
    if (!isMembershipId(targetId)) throw new Error('Invalid membership target ID')
    const response = await userApi.post(`/user/membership-groups/${encodeURIComponent(targetId)}/invitation`, {}, sensitiveOptions)
    return parseMembershipInvitation(response.data.data)
  },
}
