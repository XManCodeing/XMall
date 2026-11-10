<script setup lang="ts">
import { computed, inject, onActivated, onDeactivated, provide, ref, watchEffect } from 'vue'
import { lockPageBackground, pageDialogTargetKey, pageDialogScopeKey, pageDialogOpenKey } from './pageScope'
import type { DialogRootEmits, DialogRootProps } from "reka-ui"
import { DialogRoot } from "reka-ui"

const props = withDefaults(defineProps<DialogRootProps & { scope?: 'auto' | 'page' | 'global' }>(), { open: undefined, modal: true, scope: 'auto' })
const target = inject(pageDialogTargetKey, undefined)
// 后台页面默认复用布局的遮罩区域；无页面容器时回退全局模态，特殊全局流程可显式覆盖。
const scoped = computed(() => props.scope !== 'global' && !!target?.value)
provide(pageDialogScopeKey, scoped)
// 全局确认内部的子弹窗必须留在同一模态体系，避免被父级焦点锁阻挡。
provide(pageDialogTargetKey, computed(() => scoped.value ? target?.value ?? null : null))
const emits = defineEmits<DialogRootEmits>()

// 缓存页面隐藏时仅隐藏浮层，不向父表单发出关闭事件或清空草稿。
const active = ref(true)
const uncontrolledOpen = ref(props.defaultOpen ?? false)
onActivated(() => { active.value = true })
onDeactivated(() => { active.value = false })
const visible = computed(() => active.value && (props.open ?? uncontrolledOpen.value))
provide(pageDialogOpenKey, visible)
watchEffect(onCleanup => {
  if (scoped.value && visible.value && target?.value) onCleanup(lockPageBackground(target.value))
})
const forwarded = computed(() => {
  const { scope: _scope, ...rootProps } = props
  return { ...rootProps, open: visible.value, modal: scoped.value ? false : props.modal }
})
/** 转发用户操作，忽略失活过程的浮层关闭。@param open 新状态 @returns void */
function updateOpen(open: boolean) {
  if (!active.value) return
  uncontrolledOpen.value = open
  emits('update:open', open)
}
</script>

<template>
  <DialogRoot v-bind="forwarded" @update:open="updateOpen">
    <slot />
  </DialogRoot>
</template>
