<script setup lang="ts">
import { onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useTelegramBotSettings } from '@/composables/useTelegramBotSettings'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import MediaPicker from '@/components/admin/MediaPicker.vue'
import { Loader2, Save } from 'lucide-vue-next'

const { t } = useI18n()
const {
  currentLang,
  fetchConfig,
  form,
  instances,
  languages,
  loading,
  saveConfig,
  canSave, canListInstances, loadError, needsReload, loaded, profileLoaded, profileSyncError,
  saving, selectedInstanceId, selectInstance,
} = useTelegramBotSettings()

onMounted(() => {
  fetchConfig()
})
</script>

<template>
  <div class="space-y-6">
    <p v-if="loadError" role="alert" class="text-sm text-destructive">{{ loadError }}</p>
    <div class="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
      <div>
        <h2 class="text-2xl font-bold tracking-tight">{{ t('telegramBot.settings.title') }}</h2>
        <p class="text-muted-foreground">{{ t('telegramBot.settings.subtitle') }}</p>
      </div>
      <div class="flex flex-col gap-3 sm:flex-row sm:items-center">
        <div class="flex max-w-full overflow-x-auto rounded-lg border border-border bg-card p-1">
          <button
            v-for="lang in languages"
            :key="lang.code"
            class="shrink-0 rounded-md px-3 py-1.5 text-xs font-medium transition-colors"
            :class="currentLang === lang.code ? 'bg-secondary text-foreground' : 'text-muted-foreground hover:text-foreground'"
            @click="currentLang = lang.code"
          >
            {{ lang.name }}
          </button>
        </div>
        <Button v-if="needsReload || loadError" variant="outline" :disabled="loading || saving" @click="fetchConfig">{{ t('telegramBot.instances.reload') }}</Button>
        <Button class="w-full sm:w-auto" :disabled="saving || loading || !loaded || !canSave" @click="saveConfig">
          <Loader2 v-if="saving" class="mr-2 h-4 w-4 animate-spin" />
          <Save v-else class="mr-2 h-4 w-4" />
          {{ t('telegramBot.settings.save') }}
        </Button>
      </div>
    </div>

    <Card>
      <CardHeader>
        <CardTitle>{{ t('telegramBot.settings.globalTitle') }}</CardTitle>
        <CardDescription>{{ t('telegramBot.settings.globalDesc') }}</CardDescription>
      </CardHeader>
      <CardContent class="space-y-4">
        <div class="grid grid-cols-1 gap-4 md:grid-cols-2">
          <div class="space-y-2">
            <Label>{{ t('telegramBot.settings.defaultLocale') }}</Label>
            <Select v-model="form.default_locale">
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="zh-CN">简体中文</SelectItem>
                <SelectItem value="zh-TW">繁體中文</SelectItem>
                <SelectItem value="en-US">English</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div class="space-y-2">
            <Label>{{ t('telegramBot.settings.supportUrl') }}</Label>
            <Input v-model="form.basic.support_url" :placeholder="t('telegramBot.settings.supportUrlPlaceholder')" aria-describedby="bot-support-hint" />
            <p id="bot-support-hint" class="space-y-1 text-xs text-muted-foreground">
              <code class="block w-fit max-w-full break-all">{{ t('telegramBot.settings.supportUrlPlaceholder') }}</code>
              <span class="block">{{ t('telegramBot.settings.supportUrlHint') }}</span>
            </p>
          </div>
        </div>
      </CardContent>
    </Card>

    <Card>
      <CardHeader>
        <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <CardTitle>{{ t('telegramBot.settings.basicInfo') }}</CardTitle>
            <CardDescription>{{ t('telegramBot.settings.basicInfoDesc') }}</CardDescription>
          </div>
          <span class="rounded bg-muted px-2 py-1 text-xs text-muted-foreground">{{ currentLang }}</span>
        </div>
      </CardHeader>
      <CardContent class="space-y-4">
        <div class="space-y-2">
          <Label>{{ t('telegramBot.instances.bot') }}</Label>
          <Select
            :model-value="selectedInstanceId"
            :disabled="loading || saving || !canListInstances || !instances.length"
            @update:model-value="(value) => selectInstance(String(value ?? ''))"
          >
            <SelectTrigger>
              <SelectValue :placeholder="t('telegramBot.settings.selectInstance')" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem v-for="item in instances" :key="item.id" :value="String(item.id)">
                {{ item.username ? `@${item.username}` : item.telegram_id }}
              </SelectItem>
            </SelectContent>
          </Select>
          <p v-if="!loading && canListInstances && !instances.length" class="text-sm text-muted-foreground">{{ t('telegramBot.instances.empty') }}</p>
          <p v-if="profileSyncError" class="text-sm text-destructive">{{ t('telegramBot.settings.profileSyncFailed') }}</p>
        </div>
        <fieldset class="space-y-4 border-0 p-0" :disabled="!profileLoaded">
          <div class="space-y-2">
            <Label>{{ t('telegramBot.settings.displayName') }}</Label>
            <Input v-model="form.basic.display_name" :placeholder="t('telegramBot.settings.displayNamePlaceholder')" />
          </div>
          <div class="space-y-2">
            <Label>{{ t('telegramBot.settings.description') }}</Label>
            <Textarea v-model="form.basic.description[currentLang]" :placeholder="t('telegramBot.settings.descriptionPlaceholder')" rows="2" />
            <p class="text-xs text-muted-foreground">
              {{ t('telegramBot.settings.descriptionPictureHint') }}
              <a href="https://t.me/BotFather" target="_blank" rel="noopener noreferrer" class="text-primary underline underline-offset-4 hover:text-primary/80">{{ t('telegramBot.settings.openBotFather') }}</a>
            </p>
          </div>
          <div class="space-y-2">
            <Label>{{ t('telegramBot.settings.coverUrl') }}</Label>
            <MediaPicker v-model="form.basic.cover_url" scene="telegram" accept="image/*,video/mp4" />
            <p class="text-xs text-muted-foreground">{{ t('telegramBot.settings.avatarHint') }}</p>
          </div>
        </fieldset>
      </CardContent>
    </Card>

    <Card>
      <CardHeader>
        <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <CardTitle>{{ t('telegramBot.settings.welcomeTitle') }}</CardTitle>
            <CardDescription>{{ t('telegramBot.settings.welcomeDesc') }}</CardDescription>
          </div>
          <span class="rounded bg-muted px-2 py-1 text-xs text-muted-foreground">{{ currentLang }}</span>
        </div>
      </CardHeader>
      <CardContent class="space-y-4">
        <div class="flex flex-col gap-3 rounded-lg border border-border bg-muted/10 px-4 py-3 sm:flex-row sm:items-center">
          <Switch id="welcome-enabled" v-model="form.welcome.enabled" />
          <Label for="welcome-enabled">{{ t('telegramBot.settings.welcomeEnabled') }}</Label>
        </div>
        <div class="space-y-2">
          <Label>{{ t('telegramBot.settings.welcomeMessage') }}</Label>
          <Textarea v-model="form.welcome.message[currentLang]" :placeholder="t('telegramBot.settings.welcomeMessagePlaceholder')" rows="3" />
        </div>
      </CardContent>
    </Card>
  </div>
</template>
