<script setup lang="ts">
import { computed, inject, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { pageDialogTargetKey } from '@/components/ui/dialog/pageScope'
import { membershipPageSteps, membershipGuideTarget } from '@/utils/membershipPageGuide'

const emit = defineEmits<{ close: [] }>()
const route = useRoute()
const { t } = useI18n({ useScope: 'global' })
const dialogTarget = inject(pageDialogTargetKey, undefined)
const pageScoped = computed(() => !!dialogTarget?.value)
const steps = membershipPageSteps(route.path)
const index = ref(0)
const current = computed(() => steps[index.value])
const rect = ref<{ left: number; top: number; width: number; height: number }>()
const panel = ref({ left: '16px', top: '96px', width: '360px' })
let target: HTMLElement | undefined
let frame = 0
let observer: MutationObserver | undefined
let resizeObserver: ResizeObserver | undefined
// 在子弹窗自动聚焦前记录入口；页面切换后不抢走标签栏焦点。
const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null

/** 相对公共浮层容器测量可见区域，保持高亮和提示框在页面内。@returns void */
function measure() {
  frame = 0
  const found = current.value ? membershipGuideTarget(current.value.target) : undefined
  if (found !== target) {
    if (target) resizeObserver?.unobserve(target)
    target = found
    if (target) resizeObserver?.observe(target)
  }
  const host = dialogTarget?.value?.getBoundingClientRect()
  const originLeft = host?.left ?? 0, originTop = host?.top ?? 0
  const width = host?.width ?? window.innerWidth, height = host?.height ?? window.innerHeight
  const bounds = target?.getBoundingClientRect()
  rect.value = undefined
  if (bounds) {
    const main = target?.closest('main')?.getBoundingClientRect()
    const left = Math.max(8, bounds.left - originLeft, (main?.left ?? originLeft) - originLeft)
    const top = Math.max(8, bounds.top - originTop, (main?.top ?? originTop) - originTop)
    const right = Math.min(width - 8, bounds.right - originLeft, (main?.right ?? originLeft + width) - originLeft)
    const bottom = Math.min(height - 8, bounds.bottom - originTop, (main?.bottom ?? originTop + height) - originTop)
    if (right > left && bottom > top) rect.value = { left, top, width: right - left, height: bottom - top }
  }
  const panelWidth = Math.min(380, Math.max(0, width - 32))
  // 预留文案及按钮高度；目标较大时放在页面底部，不改变页面布局。
  const panelHeight = Math.min(330, height - 32)
  const area = rect.value
  const top = area && area.top + area.height + panelHeight + 24 <= height
    ? area.top + area.height + 12
    : area && area.top >= panelHeight + 24 ? area.top - panelHeight - 12 : height - panelHeight - 16
  panel.value = {
    left: `${Math.max(16, Math.min(area?.left ?? (width - panelWidth) / 2, width - panelWidth - 16))}px`,
    top: `${Math.max(16, top)}px`, width: `${panelWidth}px`,
  }
}

/** 合并滚动、窗口缩放和异步内容更新，避免频繁测量。@returns void */
function schedule() {
  if (!frame) frame = requestAnimationFrame(measure)
}

/** 切换说明步骤，滚动到当前控件；绝不点击页面按钮。@returns 渲染完成 */
async function locate() {
  await nextTick()
  const element = current.value ? membershipGuideTarget(current.value.target) : undefined
  element?.scrollIntoView({ block: 'nearest', inline: 'nearest', behavior: 'instant' })
  measure()
}
watch(index, locate)
watch(() => route.fullPath, () => emit('close'))
onMounted(() => {
  resizeObserver = new ResizeObserver(schedule)
  if (dialogTarget?.value) resizeObserver.observe(dialogTarget.value)
  observer = new MutationObserver(schedule)
  const main = document.querySelector('main')
  if (main) observer.observe(main, { childList: true, subtree: true, attributes: true, attributeFilter: ['class', 'style', 'hidden'] })
  window.addEventListener('resize', schedule)
  document.addEventListener('scroll', schedule, true)
  void locate()
})
onBeforeUnmount(() => {
  observer?.disconnect()
  resizeObserver?.disconnect()
  cancelAnimationFrame(frame)
  window.removeEventListener('resize', schedule)
  document.removeEventListener('scroll', schedule, true)
  const focused = document.activeElement
  if (previousFocus?.isConnected && (focused === document.body || focused?.closest('.membership-page-guide'))) {
    // 等待公共弹窗解除背景 inert 和焦点锁后再还原入口。
    void nextTick(() => {
      if (previousFocus.isConnected) previousFocus.focus({ preventScroll: true })
    })
  }
})
</script>

<template>
  <Dialog :open="true" @update:open="value => { if (!value) emit('close') }">
    <DialogContent overlay-class="bg-transparent" class="membership-page-guide max-h-[calc(100vh-2rem)] translate-x-0 translate-y-0 gap-3 overflow-y-auto p-4 duration-0 data-[state=open]:animate-none data-[state=closed]:animate-none" :content-style="panel" @close-auto-focus.prevent>
      <template #decoration>
        <div v-if="rect" data-guide-highlight aria-hidden="true" class="pointer-events-none z-50 rounded-md border-2 border-primary ring-4 ring-primary/20"
          :class="pageScoped ? 'absolute' : 'fixed'"
          :style="{ left: `${rect.left - 3}px`, top: `${rect.top - 3}px`, width: `${rect.width + 6}px`, height: `${rect.height + 6}px` }" />
      </template>
      <DialogHeader>
        <DialogTitle class="pr-6 text-base">{{ t('membershipPageGuide.title') }} · {{ index + 1 }} / {{ steps.length }}</DialogTitle>
        <DialogDescription>{{ t('membershipPageGuide.safe') }}</DialogDescription>
      </DialogHeader>
      <p class="text-sm leading-6" aria-live="polite">{{ current ? t(`membershipPageGuide.${current.key}`) : t('membershipPageGuide.missing') }}</p>
      <p v-if="!rect" role="status" class="text-xs text-muted-foreground">{{ t('membershipPageGuide.missing') }}</p>
      <div class="flex items-center justify-between gap-2">
        <Button size="sm" variant="ghost" @click="emit('close')">{{ t('membershipPageGuide.close') }}</Button>
        <div class="flex gap-2">
          <Button size="sm" variant="outline" :disabled="index === 0" @click="index--">{{ t('membershipPageGuide.previous') }}</Button>
          <Button v-if="index < steps.length - 1" size="sm" @click="index++">{{ t('membershipPageGuide.next') }}</Button>
          <Button v-else size="sm" @click="emit('close')">{{ t('membershipPageGuide.done') }}</Button>
        </div>
      </div>
    </DialogContent>
  </Dialog>
</template>
