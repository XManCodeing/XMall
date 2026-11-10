<script setup lang="ts">
import { computed, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAdminAuthStore } from '@/stores/auth'
import type { SupportTopic } from '@/api/telegramSupport'
import { confirmTopicDeletion, topicDeletionPermission, validTopicDeletion } from '@/api/telegramSupportTopic'
import { topicDeletionMessages } from '@/i18n/telegramSupportTopic'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

const props = defineProps<{ task: SupportTopic; busy: boolean; conflict: boolean }>()
const emit = defineEmits<{ recorded: []; conflict: [] }>()
const { t } = useI18n({ useScope: 'local', messages: topicDeletionMessages })
const auth = useAdminAuthStore(), saving = ref(false), error = ref('')
const draft = reactive({ evidence: '', confirmed: false })
const allowed = computed(() => auth.isSuper || auth.permissions.includes(topicDeletionPermission))
const eligible = computed(() => ['succeeded', 'failed'].includes(props.task.state) && !!props.task.mapping && ['open', 'closed'].includes(props.task.mapping.state))
const input = computed(() => ({ expected_version: props.task.operation_version ?? '', mapping_id: props.task.mapping?.id ?? '', mapping_version: props.task.mapping?.version ?? '', chat_id: props.task.mapping?.chat_id ?? '', thread_id: props.task.mapping?.thread_id ?? '', evidence: draft.evidence, confirmed: draft.confirmed }))
const valid = computed(() => allowed.value && eligible.value && !props.conflict && validTopicDeletion(input.value))
let request = 0
/** Invalidate stale bot/task replies and clear all factual confirmation drafts. */
function reset() { request++; draft.evidence = ''; draft.confirmed = false; saving.value = false; error.value = '' }
/** Record one explicit fact; CAS conflict discards drafts and asks the dialog to reload. */
async function submit() {
  if (!valid.value || saving.value || props.busy) return
  const active = ++request
  saving.value = true; error.value = ''
  try {
    await confirmTopicDeletion(props.task.id, input.value)
    if (active !== request) return
    reset(); emit('recorded')
  } catch (failure: unknown) {
    if (active !== request) return
    if ((failure as { response?: { status?: number } })?.response?.status === 409) { reset(); emit('conflict') }
    else error.value = t('failed')
  } finally { if (active === request) saving.value = false }
}
watch(() => [props.task, props.conflict, allowed.value], reset, { flush: 'sync' })
onUnmounted(reset)
</script>

<template>
  <section class="space-y-3 border-t border-border pt-4" data-testid="topic-deletion">
    <h3 class="text-sm font-medium">{{ t('title') }}</h3>
    <p class="text-sm text-muted-foreground">{{ t('hint') }}</p>
    <dl v-if="task.mapping" class="grid grid-cols-2 gap-x-6 gap-y-2 text-sm">
      <div v-for="entry in [['mapping', task.mapping.id], ['version', task.mapping.version], ['chat', task.mapping.chat_id], ['thread', task.mapping.thread_id]]" :key="entry[0]" class="flex justify-between gap-3 border-b border-border py-1"><dt class="text-muted-foreground">{{ t(entry[0]!) }}</dt><dd class="break-all text-right font-mono">{{ entry[1] }}</dd></div>
    </dl>
    <p v-if="!eligible" class="text-sm text-muted-foreground">{{ t('unavailable') }}</p>
    <p v-else-if="!allowed" class="text-sm text-muted-foreground">{{ t('forbidden') }}</p>
    <template v-else>
      <fieldset :disabled="saving || busy || conflict" class="space-y-3">
        <div class="space-y-2"><Label for="topic-delete-evidence">{{ t('evidence') }}</Label><Input id="topic-delete-evidence" v-model="draft.evidence" maxlength="103" autocomplete="off" /><p class="text-xs text-muted-foreground">{{ t('evidenceHint') }}</p></div>
        <div class="flex items-center gap-2"><Checkbox id="topic-delete-confirm" :model-value="draft.confirmed" @update:model-value="draft.confirmed = $event === true" /><Label for="topic-delete-confirm">{{ t('consent') }}</Label></div>
      </fieldset>
      <p v-if="error" role="alert" class="text-sm text-destructive">{{ error }}</p>
      <div class="flex justify-end"><Button :disabled="!valid || saving || busy" @click="submit">{{ t(saving ? 'saving' : 'submit') }}</Button></div>
    </template>
  </section>
</template>
