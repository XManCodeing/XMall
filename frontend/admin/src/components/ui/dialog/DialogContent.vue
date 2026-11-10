<script setup lang="ts">
import type { DialogContentEmits, DialogContentProps } from "reka-ui"
import type { HTMLAttributes } from "vue"
import { reactiveOmit } from "@vueuse/core"
import { X } from "lucide-vue-next"
import {
  DialogClose,
  DialogContent,
  DialogOverlay,
  DialogPortal,
  useForwardPropsEmits,
} from "reka-ui"
import { usePageDialogScope } from './pageScope'
import { cn } from "@/lib/utils"

const props = defineProps<DialogContentProps & { class?: HTMLAttributes["class"]; overlayClass?: HTMLAttributes["class"]; contentStyle?: HTMLAttributes["style"] }>()
const emits = defineEmits<DialogContentEmits>()

const delegatedProps = reactiveOmit(props, "class", "overlayClass", "contentStyle")

const { scoped, target, open } = usePageDialogScope()
const forwarded = useForwardPropsEmits(delegatedProps, emits)
</script>

<template>
  <DialogPortal :to="scoped ? target ?? undefined : undefined">
    <div v-if="scoped && open" :class="cn('absolute inset-0 z-50 pointer-events-auto bg-overlay/80', props.overlayClass)" />
    <DialogOverlay v-if="!scoped"
      :class="cn('fixed inset-0 z-50 bg-overlay/80 data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0', props.overlayClass)"
    />
    <!-- 装饰层与内容共用挂载区域及生命周期，不建立额外全局浮层。 -->
    <slot v-if="open" name="decoration" />
    <DialogContent
      v-bind="forwarded"
      :style="props.contentStyle"
      @interact-outside="event => { if (scoped) event.preventDefault() }"
      @close-auto-focus="event => { if (scoped) event.preventDefault() }"
      :class="
        cn(
          'fixed left-1/2 top-1/2 z-50 grid w-full min-w-0 max-w-lg grid-cols-1 gap-4 border bg-background p-6 shadow-lg duration-200 [&>*]:min-w-0 data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95 data-[state=closed]:slide-out-to-left-1/2 data-[state=closed]:slide-out-to-top-[48%] data-[state=open]:slide-in-from-left-1/2 data-[state=open]:slide-in-from-top-[48%] -translate-x-1/2 -translate-y-1/2 sm:rounded-lg',
          scoped && 'absolute pointer-events-auto max-h-[calc(100%-2rem)] overflow-y-auto',
          props.class,
        )"
    >
      <slot />

      <DialogClose
        class="absolute right-4 top-4 rounded-sm opacity-70 ring-offset-background transition-opacity hover:opacity-100 focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 disabled:pointer-events-none data-[state=open]:bg-accent data-[state=open]:text-muted-foreground"
      >
        <X class="w-4 h-4" />
        <span class="sr-only">Close</span>
      </DialogClose>
    </DialogContent>
  </DialogPortal>
</template>
