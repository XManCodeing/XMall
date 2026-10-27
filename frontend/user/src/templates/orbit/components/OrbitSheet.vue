<script setup lang="ts">
import { ref, watch, nextTick, onBeforeUnmount } from 'vue'
import { X } from 'lucide-vue-next'
import { useMediaQuery } from '@vueuse/core'
import { useI18n } from 'vue-i18n'

const props = defineProps<{ open: boolean; title: string }>()
const emit = defineEmits<{ 'update:open': [value: boolean] }>()
const { t } = useI18n()
const dialog = ref<HTMLDialogElement>()
const isMobile = useMediaQuery('(max-width: 767px)')
const reducedMotion = useMediaQuery('(prefers-reduced-motion: reduce)')
const dragOffset = ref(0)
const dragging = ref(false)
let pointerId: number | undefined
let startY = 0
const resetDrag = () => { dragging.value = false; dragOffset.value = 0; pointerId = undefined }
const startDrag = (event: PointerEvent) => {
  if (!isMobile.value || !event.isPrimary || event.button !== 0 || (event.target as HTMLElement).closest('button')) return
  pointerId = event.pointerId
  startY = event.clientY
  dragging.value = true
  ;(event.currentTarget as HTMLElement).setPointerCapture(event.pointerId)
}
const moveDrag = (event: PointerEvent) => {
  if (event.pointerId !== pointerId) return
  dragOffset.value = Math.min(200, Math.max(0, event.clientY - startY))
}
const endDrag = (event: PointerEvent) => {
  if (event.pointerId !== pointerId) return
  const dismiss = dragOffset.value >= 80
  dragging.value = false
  pointerId = undefined
  if (dismiss) close()
  else dragOffset.value = 0
}
const cancelDrag = () => { if (dragging.value) resetDrag() }
let previousFocus: HTMLElement | null = null
let closeTimer: ReturnType<typeof setTimeout> | undefined
let revision = 0
const finishClose = () => {
  clearTimeout(closeTimer)
  dialog.value?.close()
  resetDrag()
  dialog.value?.classList.remove('is-closing')
  if (previousFocus?.isConnected) previousFocus.focus({ preventScroll: true })
}
watch(() => props.open, async (open) => {
  const current = ++revision
  if (open) resetDrag()
  clearTimeout(closeTimer)
  await nextTick()
  if (current !== revision) return
  if (open) {
    dialog.value?.classList.remove('is-closing')
    if (!dialog.value?.open) {
      previousFocus = document.activeElement as HTMLElement | null
      dialog.value?.showModal()
    }
  } else if (dialog.value?.open) {
    if (isMobile.value && !reducedMotion.value) {
      dialog.value.classList.add('is-closing')
      closeTimer = setTimeout(finishClose, 180)
    } else finishClose()
  }
}, { immediate: true })
onBeforeUnmount(() => {
  revision += 1
  clearTimeout(closeTimer)
  dialog.value?.close()
})
const close = () => emit('update:open', false)
const onBackdrop = (event: MouseEvent) => {
  if (event.target !== dialog.value || !dialog.value) return
  const bounds = dialog.value.getBoundingClientRect()
  if (event.clientX < bounds.left || event.clientX > bounds.right || event.clientY < bounds.top || event.clientY > bounds.bottom) close()
}
</script>

<template>
  <dialog ref="dialog" class="orbit-sheet bg-card text-foreground" :class="{ 'is-dragging': dragging }" :style="{ translate: isMobile && !reducedMotion && dragOffset ? `0 ${dragOffset}px` : undefined }" :aria-label="title" @cancel.prevent="close" @click="onBackdrop">
    <div class="orbit-sheet-heading" @pointerdown="startDrag" @pointermove="moveDrag" @pointerup="endDrag" @pointercancel="cancelDrag" @lostpointercapture="cancelDrag">
      <span class="orbit-sheet-handle" aria-hidden="true" />
      <div class="flex items-center justify-between gap-4 border-b border-border px-5 py-3">
      <h2 class="text-lg font-semibold tracking-tight">{{ title }}</h2>
      <button type="button" class="grid h-11 w-11 shrink-0 place-items-center rounded-xl text-muted-foreground hover:bg-secondary" :aria-label="t('orbit.actions.close')" @click="close"><X class="h-5 w-5" /></button>
      </div>
    </div>
    <div class="orbit-sheet-body p-5"><slot /></div>
  </dialog>
</template>

<style scoped>
.orbit-sheet { position: fixed; inset: auto 0 0; margin: 0 auto; width: 100%; max-width: 40rem; max-height: 85dvh; padding: 0; border: 1px solid var(--ui-border); border-radius: 20px 20px 0 0; overflow: hidden; }
.orbit-sheet::backdrop { background: color-mix(in srgb, var(--ui-text-primary) 35%, transparent); backdrop-filter: blur(5px); }
.orbit-sheet-body { max-height: calc(85dvh - 70px); overflow-y: auto; padding-bottom: max(1.25rem, env(safe-area-inset-bottom)); overscroll-behavior: contain; }
.orbit-sheet-handle { display: none; }
@media (max-width: 767px) {
  .orbit-sheet { transition: translate 220ms var(--orbit-ease); }
  .orbit-sheet.is-dragging { transition: none; }
  .orbit-sheet-heading { touch-action: none; user-select: none; }
  .orbit-sheet-handle { display: block; width: 32px; height: 4px; margin: 10px auto 0; border-radius: 999px; background: var(--ui-border-strong); }
  .orbit-sheet-body { max-height: calc(min(85dvh, var(--orbit-visible-height, 100dvh) - 24px) - 84px); }
  .orbit-sheet { border-radius: 26px 26px 0 0; box-shadow: var(--ui-shadow-3); }
  .orbit-sheet[open] { animation: orbit-sheet-in 300ms var(--orbit-ease) both; }
  .orbit-sheet.is-closing { animation: orbit-sheet-out 180ms ease-in both; }
  .orbit-sheet::backdrop { background: var(--orbit-scrim); backdrop-filter: blur(8px); animation: orbit-sheet-scrim 220ms ease both; }
  .orbit-sheet.is-closing::backdrop { animation: orbit-sheet-scrim 180ms ease reverse both; }
  .orbit-sheet-body { scrollbar-width: thin; }
  .orbit-sheet-body :deep(button) { transition: background-color 180ms ease, border-color 180ms ease; }
  .orbit-sheet-body :deep(button[aria-pressed='true']) { background: var(--ui-accent-soft); border-color: var(--ui-accent); }
}
@keyframes orbit-sheet-in { from { opacity: .5; transform: translateY(32px); } to { opacity: 1; transform: none; } }
@keyframes orbit-sheet-out { to { opacity: 0; transform: translateY(24px); } }
@keyframes orbit-sheet-scrim { from { opacity: 0; } to { opacity: 1; } }
@media (prefers-reduced-motion: reduce) {
  .orbit-sheet[open], .orbit-sheet.is-closing, .orbit-sheet::backdrop, .orbit-sheet.is-closing::backdrop { animation: none; }
}
</style>
