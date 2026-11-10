<script setup lang="ts">
import { onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useTelegramBotSettingsRegistration } from '@/composables/useTelegramBotSettingsRegistration'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Loader2, Save } from 'lucide-vue-next'

const { t } = useI18n()
const { enabled, loaded, busy, error, canLoad, canSave, load, save } = useTelegramBotSettingsRegistration()
onMounted(load)
</script>

<template>
  <Card>
    <CardHeader>
      <div class="flex items-center justify-between gap-4">
        <div class="space-y-1.5">
          <CardTitle>{{ t('telegramBot.settings.registrationTitle') }}</CardTitle>
          <CardDescription>{{ t('telegramBot.settings.registrationDescription') }}</CardDescription>
        </div>
        <div class="flex items-center gap-2">
          <Button v-if="error" variant="outline" :disabled="busy" @click="load">{{ t('telegramBot.instances.reload') }}</Button>
          <Button :disabled="!canSave" @click="save">
            <Loader2 v-if="busy" class="mr-2 h-4 w-4 animate-spin" />
            <Save v-else class="mr-2 h-4 w-4" />
            {{ t('telegramBot.settings.registrationSave') }}
          </Button>
        </div>
      </div>
    </CardHeader>
    <CardContent class="space-y-3">
      <p v-if="!canLoad" role="alert" class="text-sm text-destructive">{{ t('telegramBot.instances.forbidden') }}</p>
      <div class="flex items-center gap-3">
        <Switch id="bot-registration-enabled" v-model="enabled" :disabled="busy || !loaded || !canSave" />
        <Label for="bot-registration-enabled">{{ t('telegramBot.settings.registrationEnabled') }}</Label>
      </div>
      <p class="text-xs text-muted-foreground">{{ t('telegramBot.settings.registrationHint') }}</p>
    </CardContent>
  </Card>
</template>
