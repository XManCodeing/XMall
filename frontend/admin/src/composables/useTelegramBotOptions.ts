import { ref, type ComputedRef } from 'vue'
import { adminAPI } from '@/api/admin'
import type { BotInstance } from '@/composables/useTelegramBotInstances'
import { validRecoveryBotID } from '@/api/telegramRecovery'

/** 读取机器人选项，复用迁移页的无损编号校验，不拉取运行状态。
 * @param canBots 实例列表读取权限。@param message 安全错误文案转换。@returns 选项及加载、清理方法。
 */
export function useTelegramBotOptions(canBots: ComputedRef<boolean>, message: (cause: unknown) => string) {
  const bots = ref<Array<Pick<BotInstance, 'username'> & { id: string }>>([]), botsLoading = ref(false), botsError = ref('')
  let generation = 0
  /** 读取选项，忽略旧上下文响应及已丢失精度的编号。@returns 加载完成。 */
  async function loadBots() {
    if (!canBots.value || botsLoading.value) return
    const current = ++generation
    botsLoading.value = true; botsError.value = ''
    try {
      const response = await adminAPI.getTelegramBotInstances()
      if (current !== generation || !canBots.value) return
      bots.value = (response.data.data.items || []).filter((bot: BotInstance) =>
        (typeof bot.id === 'string' || Number.isSafeInteger(bot.id)) && validRecoveryBotID(String(bot.id)))
        .map((bot: BotInstance) => ({ id: String(bot.id), username: bot.username }))
    } catch (cause) { if (current === generation) botsError.value = message(cause) }
    finally { if (current === generation) botsLoading.value = false }
  }
  /** 清理账号关联选项并使在途请求失效。@returns 无。 */
  function clearBots() { generation++; bots.value = []; botsLoading.value = false; botsError.value = '' }
  return { bots, botsLoading, botsError, loadBots, clearBots }
}
