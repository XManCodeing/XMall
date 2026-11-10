<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import type { LocalizedText } from '@/api/types'
export interface ManualFormField { key: string; type: string; required: boolean; label: LocalizedText; placeholder: LocalizedText; regex: string; min: string; max: string; max_len: string; options_text: string }
const props = defineProps<{ fields: ManualFormField[]; locked: boolean; currentLang: string; languageName: string; types: { value: string; label: string }[] }>()
const emit = defineEmits<{ add: []; remove: [index: number] }>()
const { t } = useI18n()
/** Determine whether text validation fields apply. @param type Existing manual-field type. @returns True for text-like fields. */
function isTextLike(type: string) { return ['text', 'textarea', 'phone', 'email'].includes(type) }
</script>

<template>
  <div class="col-span-1 md:col-span-2 rounded-xl border border-border bg-muted/20 p-4 space-y-4">
    <div class="flex items-center justify-between"><div><h3 class="text-sm font-semibold text-foreground">{{ t('admin.products.form.manualFormSchemaTitle') }}</h3><p class="text-xs text-muted-foreground mt-1">{{ t('admin.products.form.manualFormSchemaTip') }}</p><p v-if="locked" class="mt-1 text-xs text-muted-foreground">{{ t('admin.products.mappedFormSchemaLocked') }}</p></div><Button v-if="!locked" type="button" size="sm" variant="outline" @click="emit('add')">{{ t('admin.products.form.manualFormAddField') }}</Button></div>
    <div v-if="!fields.length" class="rounded-lg border border-dashed border-border p-4 text-xs text-muted-foreground">{{ t('admin.products.form.manualFormEmpty') }}</div>
    <div v-for="(field, index) in props.fields" :key="index" class="rounded-lg border border-border bg-background p-4 space-y-3">
      <div class="flex items-center justify-between"><div class="text-xs font-medium text-muted-foreground">{{ t('admin.products.form.manualFormFieldTitle', { index: index + 1 }) }}</div><Button v-if="!locked" type="button" variant="destructive" size="sm" @click="emit('remove', index)">{{ t('admin.products.form.manualFormRemoveField') }}</Button></div>
      <div class="grid grid-cols-1 md:grid-cols-3 gap-3">
        <div><label class="block text-xs font-medium text-muted-foreground mb-1.5">{{ t('admin.products.form.manualFormFieldKey') }}</label><Input v-model="field.key" :placeholder="t('admin.products.form.manualFormFieldKeyPlaceholder')" :disabled="locked" /></div>
        <div><label class="block text-xs font-medium text-muted-foreground mb-1.5">{{ t('admin.products.form.manualFormFieldType') }}</label><Select v-model="field.type" :disabled="locked"><SelectTrigger class="h-9"><SelectValue /></SelectTrigger><SelectContent><SelectItem v-for="item in types" :key="item.value" :value="item.value">{{ item.label }}</SelectItem></SelectContent></Select></div>
        <div class="flex items-end"><div class="inline-flex items-center gap-2"><Switch v-model="field.required" :disabled="locked" /><Label class="text-sm text-muted-foreground">{{ t('admin.products.form.manualFormFieldRequired') }}</Label></div></div>
      </div>
      <div class="grid grid-cols-1 md:grid-cols-2 gap-3">
        <div><label class="block text-xs font-medium text-muted-foreground mb-1.5">{{ t('admin.products.form.manualFormFieldLabel', { lang: languageName }) }}</label><Input v-model="field.label[currentLang]" :placeholder="t('admin.products.form.manualFormFieldLabelPlaceholder')" :disabled="locked" /></div>
        <div><label class="block text-xs font-medium text-muted-foreground mb-1.5">{{ t('admin.products.form.manualFormFieldPlaceholder', { lang: languageName }) }}</label><Input v-model="field.placeholder[currentLang]" :placeholder="t('admin.products.form.manualFormFieldPlaceholderPlaceholder')" :disabled="locked" /></div>
      </div>
      <div v-if="isTextLike(field.type)" class="grid grid-cols-1 md:grid-cols-2 gap-3">
        <div><label class="block text-xs font-medium text-muted-foreground mb-1.5">{{ t('admin.products.form.manualFormFieldRegex') }}</label><Input v-model="field.regex" :placeholder="t('admin.products.form.manualFormFieldRegexPlaceholder')" :disabled="locked" /></div>
        <div><label class="block text-xs font-medium text-muted-foreground mb-1.5">{{ t('admin.products.form.manualFormFieldMaxLength') }}</label><Input v-model="field.max_len" type="number" min="1" :placeholder="t('admin.products.form.manualFormFieldMaxLengthPlaceholder')" :disabled="locked" /></div>
      </div>
      <div v-if="field.type === 'number'" class="grid grid-cols-1 md:grid-cols-2 gap-3">
        <div><label class="block text-xs font-medium text-muted-foreground mb-1.5">{{ t('admin.products.form.manualFormFieldMin') }}</label><Input v-model="field.min" type="number" :placeholder="t('admin.products.form.manualFormFieldMinPlaceholder')" :disabled="locked" /></div>
        <div><label class="block text-xs font-medium text-muted-foreground mb-1.5">{{ t('admin.products.form.manualFormFieldMax') }}</label><Input v-model="field.max" type="number" :placeholder="t('admin.products.form.manualFormFieldMaxPlaceholder')" :disabled="locked" /></div>
      </div>
      <div v-if="['select', 'radio', 'checkbox'].includes(field.type)"><label class="block text-xs font-medium text-muted-foreground mb-1.5">{{ t('admin.products.form.manualFormFieldOptions') }}</label><Textarea v-model="field.options_text" rows="4" :placeholder="t('admin.products.form.manualFormFieldOptionsPlaceholder')" :disabled="locked" /><p class="mt-1 text-xs text-muted-foreground">{{ t('admin.products.form.manualFormFieldOptionsTip') }}</p></div>
    </div>
  </div>
</template>
