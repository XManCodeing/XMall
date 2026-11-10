<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import MembershipPageGuide from './MembershipPageGuide.vue'
import { membershipPageSteps } from '@/utils/membershipPageGuide'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Users, Package, ReceiptText, MessagesSquare, ArrowRightLeft } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from '@/components/ui/dialog'
import { membershipGuideSteps } from '@/composables/useMembershipGuide'
import { useAdminNavigation } from '@/composables/useAdminNavigation'
import { notifyError } from '@/utils/notify'

const open = defineModel<boolean>('open', { required: true })
const step = defineModel<number>('step', { required: true })
const { t } = useI18n({ useScope: 'global' })
const pageGuide = ref(false)
const router = useRouter()
const canGuideCurrent = computed(() => membershipPageSteps(router.currentRoute.value.path).length > 0)
// 引导归属当前页面，切换标签时关闭，避免把旧页说明带到新页。
watch(() => router.currentRoute.value.fullPath, () => {
  open.value = false
  pageGuide.value = false
})

/** 关闭概览后启动页面定位，避免两个焦点锁同时存在。@returns 渲染完成 */
async function startPageGuide() {
  open.value = false
  await nextTick()
  pageGuide.value = true
}
/** 顶栏入口直接读取当前路由和可见子页；未支持的页面才回退概览。@returns 渲染完成 */
async function showCurrent() {
  if (membershipPageSteps(router.currentRoute.value.path).length) await startPageGuide()
  else open.value = true
}
defineExpose({ showCurrent })

const { navGroups } = useAdminNavigation()
const busy = ref(false)
const icons = [Users, Package, ReceiptText, MessagesSquare, ArrowRightLeft]
const current = computed(() => membershipGuideSteps[step.value] ?? membershipGuideSteps[0])
// 跳转只使用已有权限过滤菜单，不为引导绕过后台权限。
const canVisit = computed(() => navGroups.value.some(group => group.items.some(item => item.to === current.value.path)))

/** 打开当前步骤已有页面，成功后关闭引导。@returns 导航完成 */
async function visit() {
  if (busy.value || !canVisit.value) return
  busy.value = true
  try {
    if (router.currentRoute.value.path === current.value.path || !(await router.push(current.value.path))) await startPageGuide()
    else notifyError(t('membershipGuide.failed'))
  } catch { notifyError(t('membershipGuide.failed')) }
  finally { busy.value = false }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="sm:max-w-xl">
      <DialogHeader>
        <DialogTitle>{{ t('membershipGuide.guide') }}</DialogTitle>
        <DialogDescription>{{ t('membershipGuide.intro') }}</DialogDescription>
      </DialogHeader>
      <Button v-if="canGuideCurrent" size="sm" class="justify-self-start" @click="startPageGuide">{{ t('membershipPageGuide.start') }}</Button>
      <div class="space-y-4 py-2">
        <div class="flex items-center gap-2 text-sm">
          <component :is="icons[step]" class="h-4 w-4 text-muted-foreground" aria-hidden="true" />
          <h3 class="font-medium">{{ t(`membershipGuide.${current.key}`) }}</h3>
          <span class="ml-auto text-xs tabular-nums text-muted-foreground">{{ step + 1 }} / {{ membershipGuideSteps.length }}</span>
        </div>
        <p class="text-sm leading-6 text-muted-foreground">{{ t(`membershipGuide.${current.key}Body`) }}</p>
        <Button v-if="canVisit" size="sm" variant="outline" :disabled="busy" @click="visit">{{ t('membershipGuide.visit') }}</Button>
        <p class="text-xs text-muted-foreground">{{ t('membershipGuide.reopen') }}</p>
      </div>
      <div class="flex items-center justify-between gap-2">
        <Button size="sm" variant="ghost" @click="open = false">{{ t('membershipGuide.skip') }}</Button>
        <div class="flex gap-2">
          <Button size="sm" variant="outline" :disabled="step === 0 || busy" @click="step--">{{ t('membershipGuide.previous') }}</Button>
          <Button v-if="step < membershipGuideSteps.length - 1" size="sm" :disabled="busy" @click="step++">{{ t('membershipGuide.next') }}</Button>
          <Button v-else size="sm" :disabled="busy" @click="open = false">{{ t('membershipGuide.done') }}</Button>
        </div>
      </div>
    </DialogContent>
  </Dialog>
  <MembershipPageGuide v-if="pageGuide" @close="pageGuide = false" />
</template>
