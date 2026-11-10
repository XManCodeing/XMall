<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Moon, Sun, RefreshCw } from 'lucide-vue-next'
import BrandMark from '@/components/BrandMark.vue'
import { Button } from '@/components/ui/button'

const props = defineProps<{ collapsed?: boolean; dark: boolean; canCheck: boolean; hasUpdate: boolean; latestVersion: string }>()
const emit = defineEmits<{ update: []; theme: [] }>()
const { t } = useI18n()
const updateLabel = computed(() => props.hasUpdate ? t('admin.updateCheck.hasUpdate', { version: props.latestVersion }) : t('admin.updateCheck.button'))
</script>

<template>
  <div class="flex items-center py-5" :class="collapsed ? 'flex-col gap-3 px-2' : 'justify-between gap-3 px-4'" data-brand-header>
    <div class="flex shrink-0 items-center gap-2" title="XMall">
      <BrandMark class="h-7 w-7" />
      <span v-if="!collapsed" class="text-xl font-semibold tracking-tight">XMall</span>
    </div>
    <div class="flex shrink-0 gap-2" :class="collapsed ? 'flex-col' : ''">
      <Button v-if="canCheck" size="icon-sm" variant="ghost" class="relative" :title="updateLabel" :aria-label="updateLabel" data-update-check @click="emit('update')">
        <RefreshCw class="h-4 w-4" />
        <span v-if="hasUpdate" class="absolute right-0.5 top-0.5 size-2 rounded-full bg-destructive ring-2 ring-card" aria-hidden="true" data-update-dot />
      </Button>
      <Button size="icon-sm" variant="ghost" :title="t('admin.layout.toggleTheme')" :aria-label="t('admin.layout.toggleTheme')" data-theme-toggle @click="emit('theme')">
        <Sun v-if="dark" class="h-4 w-4" /><Moon v-else class="h-4 w-4" />
      </Button>
    </div>
  </div>
</template>
