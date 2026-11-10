import { onBeforeUnmount, onMounted, ref } from 'vue'
import { membershipGroupsAPI } from '../api/membershipGroups'
import { canRequestMembership, type MembershipGroup, type MembershipInvitation } from '../utils/membershipGroups'

/** 管理只读分页和显式邀请操作；链接仅存当前组件内存。@returns 页面状态及用户操作。 */
export function useMembershipGroups() {
  const items = ref<MembershipGroup[]>([])
  const nextCursor = ref<string>()
  const loading = ref(false)
  const error = ref(false)
  const invitations = ref<Record<string, MembershipInvitation>>({})
  const requesting = ref<string | null>(null)
  let generation = 0
  let disposed = false

  /** 重新查询或加载下一页，任何刷新都销毁旧入口。@param append 是否追加分页。@returns 完成后更新列表。 */
  async function load(append = false) {
    if (loading.value || requesting.value || (append && !nextCursor.value)) return
    const currentGeneration = ++generation
    invitations.value = {}
    loading.value = true
    error.value = false
    try {
      const page = await membershipGroupsAPI.list(append ? nextCursor.value : undefined)
      if (disposed || currentGeneration !== generation) return
      items.value = append ? [...new Map([...items.value, ...page.items].map((item) => [item.target_id, item])).values()] : page.items
      nextCursor.value = page.next_cursor
    } catch {
      // 不输出原始异常，避免邀请或个人身份进入日志；失败时不允许使用陈旧授权。
      error.value = true
      items.value = []
      nextCursor.value = undefined
    } finally {
      if (!disposed && currentGeneration === generation) loading.value = false
    }
  }

  /** 显式申请；网络错误按未知结果处理，不自动重试。@param group 群权益。@returns 更新该群状态和临时链接。 */
  async function requestInvitation(group: MembershipGroup) {
    if (disposed || loading.value || requesting.value || !canRequestMembership(group)) return
    requesting.value = group.target_id
    delete invitations.value[group.target_id]
    try {
      const invitation = await membershipGroupsAPI.requestInvitation(group.target_id)
      if (disposed) return
      group.state = invitation.state
      group.can_request = false
      invitations.value[group.target_id] = invitation
    } catch {
      if (disposed) return
      group.state = 'unknown'
      group.can_request = false
    } finally {
      requesting.value = null
    }
  }

  /** 打开前再检查入口有效期；过期不会静默再发 POST。@param group 群权益。@param event 点击事件。@returns 无。 */
  function checkInvitation(group: MembershipGroup, event: Event) {
    const invitation = invitations.value[group.target_id]
    if (!invitation?.invite_url || (invitation.expires_at && Date.parse(invitation.expires_at) <= Date.now())) {
      event.preventDefault()
      delete invitations.value[group.target_id]
      group.state = 'recovering'
      group.can_request = false
    }
  }

  onMounted(() => { void load() })
  onBeforeUnmount(() => {
    disposed = true
    generation += 1
    invitations.value = {}
  })
  return { items, nextCursor, loading, error, invitations, requesting, load, requestInvitation, checkInvitation }
}
