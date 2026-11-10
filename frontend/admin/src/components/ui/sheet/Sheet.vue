<script setup lang="ts">
import type { DialogRootEmits, DialogRootProps } from "reka-ui"
import { useForwardPropsEmits } from "reka-ui"
import DialogRoot from '../dialog/Dialog.vue'

const props = withDefaults(defineProps<DialogRootProps & { scope?: 'auto' | 'page' | 'global' }>(), { open: undefined, modal: true })
const emits = defineEmits<DialogRootEmits>()

const forwarded = useForwardPropsEmits(props, emits)
</script>

<template>
  <DialogRoot v-bind="forwarded">
    <slot />
  </DialogRoot>
</template>
