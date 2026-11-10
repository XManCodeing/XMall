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

const props = defineProps<DialogContentProps & { class?: HTMLAttributes["class"] }>()
const emits = defineEmits<DialogContentEmits>()

const delegatedProps = reactiveOmit(props, "class")

const { scoped, target, open } = usePageDialogScope()
const forwarded = useForwardPropsEmits(delegatedProps, emits)
</script>

<template>
  <DialogPortal :to="scoped ? target ?? undefined : undefined">
    <component
      :is="scoped ? 'div' : DialogOverlay"
      v-if="!scoped || open"
      :class="[scoped ? 'absolute pointer-events-auto' : 'fixed', 'inset-0 z-50 grid place-items-center overflow-y-auto bg-overlay/80 data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0']"
    >
      <DialogContent
        :class="
          cn(
            'relative z-50 grid w-full min-w-0 max-w-lg grid-cols-1 my-8 gap-4 border border-border bg-background p-6 shadow-lg duration-200 [&>*]:min-w-0 sm:rounded-lg md:w-full',
            props.class,
          )
        "
        v-bind="forwarded"
        @interact-outside="event => { if (scoped) event.preventDefault() }"
        @close-auto-focus="event => { if (scoped) event.preventDefault() }"
        @pointer-down-outside="(event) => {
          const originalEvent = event.detail.originalEvent;
          const target = originalEvent.target as HTMLElement;
          if (originalEvent.offsetX > target.clientWidth || originalEvent.offsetY > target.clientHeight) {
            event.preventDefault();
          }
        }"
      >
        <slot />

        <DialogClose
          class="absolute top-4 right-4 p-0.5 transition-colors rounded-md hover:bg-secondary"
        >
          <X class="w-4 h-4" />
          <span class="sr-only">Close</span>
        </DialogClose>
      </DialogContent>
    </component>
  </DialogPortal>
</template>
