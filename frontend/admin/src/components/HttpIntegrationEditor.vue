<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref, watch } from 'vue'
import { confirmAction } from '@/utils/confirm'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Checkbox } from '@/components/ui/checkbox'
import { Textarea } from '@/components/ui/textarea'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import HttpKeyValueRows from '@/components/HttpKeyValueRows.vue'
import { HTTP_OPERATIONS, httpIntegrationAPI, isReadOnlyOperation, type ConfigResponse, type Config, type HttpOperation, type Result } from '@/api/httpIntegration'
import { emptyField, endpointDraft, makeDrafts, mergeRequest, recommendedFields, rowsToRecord, serializeEndpoint, toRows, type KeyValueRow } from '@/utils/httpIntegration'
import { notifyErrorOnce, notifySuccess } from '@/utils/notify'

const props = defineProps<{ connectionId: number; baseUrl: string }>()
const emit = defineEmits<{ saved: [data: ConfigResponse] }>()
const revision = ref(0)
const ready = ref(false)
const validationErrors = ref<string[]>([])
const deletedEndpoints = ref<string[]>([])
const credentialTemplate = '{{credential_name}}'
const { t } = useI18n()
const loading = ref(true)
const loadFailed = ref(false)
const busy = ref(false)
const error = ref('')
const operation = ref<HttpOperation>('ping')
const configured = ref(false)
const savedConfig = ref<Config>({ endpoints: {}, credentials: {} })
const drafts = ref(makeDrafts(savedConfig.value))
const credentials = ref<KeyValueRow[]>([])
const variables = ref<KeyValueRow[]>([])
const snapshot = ref('')
const curl = ref('')
const hostConfirmed = ref(false)
const networkConfirmed = ref(false)
const response = ref('')
const result = ref<Result | null>(null)
const mapped = ref<unknown>()
const generation = ref(0)
const endpoint = computed(() => drafts.value[operation.value])
const conflictingBody = computed(() => !!endpoint.value.json_body.trim() && endpoint.value.body.length > 0)
const fingerprint = () => JSON.stringify({ drafts: drafts.value, credentials: credentials.value, deletedEndpoints: deletedEndpoints.value })
const dirty = computed(() => fingerprint() !== snapshot.value)
const canDebug = computed(() => !loading.value && !loadFailed.value && !busy.value && !dirty.value && !!savedConfig.value.endpoints[operation.value])
const readOnlyOperation = computed(() => isReadOnlyOperation(operation.value))
const pretty = (value: unknown) => typeof value === 'string' ? value : JSON.stringify(value, null, 2)
const showError = (cause: unknown) => {
  const message = cause instanceof Error ? cause.message : ''
  if (['httpIntegration.invalidRows', 'httpIntegration.invalidJson', 'httpIntegration.invalidPath', 'httpIntegration.conflictingBody'].includes(message)) {
    error.value = t(message)
    return
  }
  notifyErrorOnce(cause, t('httpIntegration.failed'))
}
function applyConfig(data: ConfigResponse) {
  const { config, configured: isConfigured } = data
  revision.value = data.revision
  ready.value = data.ready === true
  validationErrors.value = data.validation_errors || []
  deletedEndpoints.value = []
  savedConfig.value = config
  configured.value = isConfigured
  drafts.value = makeDrafts(config)
  credentials.value = toRows(config.credentials, true)
  snapshot.value = fingerprint()
}
async function load() {
  const version = ++generation.value
  loading.value = true
  loadFailed.value = false
  error.value = ''
  result.value = null
  mapped.value = undefined
  curl.value = ''
  response.value = ''
  variables.value = []
  try {
    const data = await httpIntegrationAPI.getConfig(props.connectionId)
    if (version === generation.value) applyConfig(data)
  } catch {
    if (version === generation.value) loadFailed.value = true
  } finally {
    if (version === generation.value) loading.value = false
  }
}
async function save() {
  if (busy.value || loading.value || loadFailed.value) return
  error.value = ''
  busy.value = true
  try {
    const endpoints = { ...savedConfig.value.endpoints }
    for (const action of HTTP_OPERATIONS) {
      if (deletedEndpoints.value.includes(action)) { delete endpoints[action]; continue }
      const draft = drafts.value[action]
      // Never silently discard an edited operation with a missing path.
      if (JSON.stringify(draft) !== JSON.stringify(endpointDraft()) || endpoints[action]) endpoints[action] = serializeEndpoint(draft)
    }
    const nextCredentials = rowsToRecord(credentials.value)
    const deleteCredentials = Object.keys(savedConfig.value.credentials || {}).filter(key => !(key in nextCredentials))
    if ((deleteCredentials.length || deletedEndpoints.value.length) && !await confirmAction({ description: t('httpIntegration.confirmDelete'), variant: 'destructive' })) return
    const data = await httpIntegrationAPI.saveConfig(props.connectionId, {
      config: { ...savedConfig.value, endpoints, credentials: nextCredentials }, revision: revision.value,
      delete_credentials: deleteCredentials, delete_endpoints: [...deletedEndpoints.value],
    })
    applyConfig(data)
    emit('saved', data)
    result.value = null
    mapped.value = undefined
    networkConfirmed.value = false
    notifySuccess(t('httpIntegration.saved'))
  } catch (cause) { showError(cause) } finally { busy.value = false }
}
async function importCurl() {
  if (!curl.value.trim() || !hostConfirmed.value || busy.value) return
  busy.value = true
  error.value = ''
  try {
    const request = await httpIntegrationAPI.parseCurl(props.connectionId, operation.value, curl.value)
    drafts.value[operation.value] = mergeRequest(endpoint.value, request)
    curl.value = ''
    hostConfirmed.value = false
    notifySuccess(t('httpIntegration.imported'))
  } catch (cause) { showError(cause) } finally { busy.value = false }
}
async function debug(action: 'map_response' | 'test') {
  if (!canDebug.value || (action === 'test' && (!readOnlyOperation.value || !networkConfirmed.value)) || (action === 'map_response' && !response.value.trim())) return
  busy.value = true
  error.value = ''
  result.value = null
  mapped.value = undefined
  try {
    if (action === 'test') {
      result.value = await httpIntegrationAPI.test(props.connectionId, operation.value, rowsToRecord(variables.value))
      mapped.value = result.value.mapped
    } else {
      mapped.value = await httpIntegrationAPI.mapResponse(props.connectionId, operation.value, response.value)
    }
  } catch (cause) { showError(cause) } finally { busy.value = false }
}
function addRecommended() {
  const existing = new Set(endpoint.value.fields.map(field => field.key))
  endpoint.value.fields.push(...recommendedFields(operation.value).filter(key => !existing.has(key)).map(emptyField))
}
async function confirmLeave() {
  if (busy.value) return false
  return !dirty.value || await confirmAction({ description: t('httpIntegration.confirmLeave'), variant: 'destructive' })
}
const beforeUnload = (event: BeforeUnloadEvent) => {
  if (!dirty.value) return
  event.preventDefault()
  event.returnValue = ''
}
onMounted(() => window.addEventListener('beforeunload', beforeUnload))
onBeforeUnmount(() => window.removeEventListener('beforeunload', beforeUnload))
defineExpose({ confirmLeave, dirty })
watch(operation, () => {
  result.value = null
  mapped.value = undefined
  response.value = ''
  error.value = ''
  networkConfirmed.value = false
})
watch(curl, () => { hostConfirmed.value = false })
watch(() => props.connectionId, load, { immediate: true })
</script>

<template>
  <div class="space-y-4 text-foreground">
    <div class="rounded-lg border border-border bg-muted/30 p-3 text-sm space-y-1">
      <p>{{ t('httpIntegration.scope') }}</p>
      <p class="break-all font-mono text-xs text-muted-foreground">{{ baseUrl }}</p>
    </div>
    <p v-if="loading" role="status" class="text-sm text-muted-foreground">{{ t('httpIntegration.loading') }}</p>
    <div v-else-if="loadFailed" class="flex items-center justify-between gap-3 text-sm">
      <p role="alert" class="text-destructive">{{ t('httpIntegration.loadFailed') }}</p>
      <Button size="sm" variant="outline" @click="load">{{ t('httpIntegration.retry') }}</Button>
    </div>
    <template v-else>
      <p role="status" class="text-sm text-muted-foreground">{{ ready ? t('httpIntegration.ready') : t('httpIntegration.notReady') }}</p>
      <ul v-if="validationErrors.length" class="text-xs text-destructive"><li v-for="message in validationErrors" :key="message">{{ message }}</li></ul>
      <p v-if="!configured" class="text-xs text-muted-foreground">{{ t('httpIntegration.empty') }}</p>
      <fieldset :disabled="busy" class="min-w-0 space-y-4 disabled:opacity-70">
        <div class="flex items-end justify-between gap-3">
          <div class="w-64 space-y-1">
            <label class="text-xs text-muted-foreground">{{ t('httpIntegration.operation') }}</label>
            <Select v-model="operation" :disabled="busy">
              <SelectTrigger class="h-9"><SelectValue /></SelectTrigger>
              <SelectContent><SelectItem v-for="action in HTTP_OPERATIONS" :key="action" :value="action">{{ t(`httpIntegration.actions.${action}`) }}</SelectItem></SelectContent>
            </Select>
          </div>
          <Button size="sm" :disabled="busy || !dirty" @click="save">{{ busy ? t('httpIntegration.working') : t('httpIntegration.save') }}</Button>
        </div>
        <p class="text-xs text-muted-foreground" role="status">{{ dirty ? t('httpIntegration.dirty') : t('httpIntegration.clean') }}</p>
        <label class="flex items-center gap-2 text-xs"><Checkbox :model-value="deletedEndpoints.includes(operation)" @update:model-value="value => deletedEndpoints = value ? [...new Set([...deletedEndpoints, operation])] : deletedEndpoints.filter(item => item !== operation)" />{{ t('httpIntegration.deleteEndpoint') }}</label>
        <div class="grid grid-cols-[130px_1fr_150px] gap-3">
          <div class="space-y-1">
            <label class="text-xs text-muted-foreground">{{ t('httpIntegration.method') }}</label>
            <Select v-model="endpoint.method" :disabled="busy"><SelectTrigger class="h-9"><SelectValue /></SelectTrigger><SelectContent><SelectItem v-for="method in ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS']" :key="method" :value="method">{{ method }}</SelectItem></SelectContent></Select>
          </div>
          <div class="space-y-1"><label class="text-xs text-muted-foreground">{{ t('httpIntegration.path') }}</label><Input v-model="endpoint.path" class="h-9 font-mono" placeholder="/api/products" /></div>
          <div class="space-y-1">
            <label class="text-xs text-muted-foreground">{{ t('httpIntegration.encoding') }}</label>
            <Select v-model="endpoint.encoding" :disabled="busy"><SelectTrigger class="h-9"><SelectValue /></SelectTrigger><SelectContent><SelectItem v-for="encoding in ['none', 'json', 'form']" :key="encoding" :value="encoding">{{ encoding === 'none' ? t('httpIntegration.none') : encoding }}</SelectItem></SelectContent></Select>
          </div>
        </div>
        <div class="grid grid-cols-2 gap-4">
          <HttpKeyValueRows v-model="endpoint.headers" :label="t('httpIntegration.headers')" />
          <HttpKeyValueRows v-model="endpoint.query" :label="t('httpIntegration.query')" />
          <HttpKeyValueRows v-model="endpoint.body" :label="t('httpIntegration.body')" />
          <div class="space-y-2"><HttpKeyValueRows v-model="credentials" secret :label="t('httpIntegration.credentials')" /><p class="text-xs text-muted-foreground">{{ t('httpIntegration.credentialsHint', { template: credentialTemplate }) }}</p></div>
        </div>
        <details class="border-t border-border pt-3">
          <summary class="cursor-pointer text-sm font-medium">{{ t('httpIntegration.advanced') }}</summary>
          <div class="space-y-3 pt-3">
            <p v-if="conflictingBody" role="alert" class="text-xs text-destructive">{{ t('httpIntegration.conflictingBody') }}</p>
            <p class="text-xs text-muted-foreground">{{ t('httpIntegration.advancedHint') }}</p>
            <label class="flex items-center gap-2 text-xs"><Checkbox v-model="endpoint.list" />{{ t('httpIntegration.rootList') }}</label>
            <div v-for="key in ['json_body', 'meta_fields', 'collections'] as const" :key="key" class="space-y-1">
              <label class="text-xs text-muted-foreground">{{ t(`httpIntegration.${key}`) }}</label>
              <Textarea v-model="endpoint[key]" rows="4" class="font-mono text-xs" :aria-label="t(`httpIntegration.${key}`)" spellcheck="false" />
            </div>
          </div>
        </details>
        <details class="border-t border-border pt-3">
          <summary class="cursor-pointer text-sm font-medium">{{ t('httpIntegration.curl') }}</summary>
          <div class="space-y-2 pt-3">
            <p class="text-xs text-muted-foreground">{{ t('httpIntegration.curlHint') }}</p>
            <Textarea v-model="curl" rows="3" class="font-mono text-xs" :aria-label="t('httpIntegration.curl')" autocomplete="off" spellcheck="false" />
            <label class="flex items-center gap-2 text-xs"><Checkbox v-model="hostConfirmed" :disabled="busy" />{{ t('httpIntegration.hostConfirmed') }}</label>
            <Button size="sm" variant="outline" :disabled="busy || !curl.trim() || !hostConfirmed" @click="importCurl">{{ t('httpIntegration.parse') }}</Button>
          </div>
        </details>
        <section class="space-y-3 border-t border-border pt-3">
          <div class="flex items-center justify-between"><h3 class="text-sm font-medium">{{ t('httpIntegration.mapping') }}</h3><div class="flex gap-2"><Button size="sm" variant="outline" @click="addRecommended">{{ t('httpIntegration.recommend') }}</Button><Button size="sm" variant="outline" @click="endpoint.fields.push(emptyField())">{{ t('httpIntegration.add') }}</Button></div></div>
          <p class="text-xs text-muted-foreground">{{ t('httpIntegration.pathHint') }}</p>
          <p class="text-xs text-muted-foreground">{{ t('httpIntegration.scalarHint') }}</p>
          <div class="grid grid-cols-3 gap-3">
            <div><label class="text-xs text-muted-foreground">{{ t('httpIntegration.listPath') }}</label><Input v-model="endpoint.list_path" class="h-9" /></div>
            <div><label class="text-xs text-muted-foreground">{{ t('httpIntegration.successPath') }}</label><Input v-model="endpoint.successPath" class="h-9" /></div>
            <div><label class="text-xs text-muted-foreground">{{ t('httpIntegration.successEquals') }}</label><Input v-model="endpoint.successEquals" class="h-9" /></div>
          </div>
          <div class="overflow-x-auto rounded-lg border border-border">
            <table class="w-full min-w-[1040px] text-xs">
              <thead class="bg-muted/40 text-muted-foreground"><tr><th v-for="key in ['field', 'path', 'required', 'transform', 'constant', 'fallback', 'enum']" :key="key" class="p-2 text-left font-medium">{{ t(`httpIntegration.${key}`) }}</th><th class="p-2"><span class="sr-only">{{ t('httpIntegration.remove') }}</span></th></tr></thead>
              <tbody class="divide-y divide-border">
                <tr v-if="!endpoint.fields.length"><td colspan="8" class="p-3 text-center text-muted-foreground">{{ t('httpIntegration.emptyRows') }}</td></tr>
                <tr v-for="(field, index) in endpoint.fields" :key="index">
                  <td class="p-1.5"><Input v-model="field.key" class="h-8 min-w-28 text-xs" :aria-label="t('httpIntegration.field')" /></td>
                  <td class="p-1.5"><Input v-model="field.path" class="h-8 min-w-28 text-xs" :aria-label="t('httpIntegration.path')" /></td>
                  <td class="p-1.5"><Checkbox v-model="field.required" :disabled="busy" :aria-label="t('httpIntegration.required')" /></td>
                  <td class="p-1.5"><Select :model-value="field.transform || '__none__'" :disabled="busy" @update:model-value="value => field.transform = value === '__none__' ? '' : String(value)"><SelectTrigger class="h-8 min-w-28 text-xs"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="__none__">{{ t('httpIntegration.none') }}</SelectItem><SelectItem v-for="transform in ['decimal', 'cents_to_units', 'string', 'int', 'bool']" :key="transform" :value="transform">{{ transform }}</SelectItem></SelectContent></Select></td>
                  <td class="p-1.5"><Input v-model="field.value" class="h-8 min-w-24 text-xs" :aria-label="t('httpIntegration.constant')" /></td>
                  <td class="p-1.5"><Input v-model="field.default" class="h-8 min-w-24 text-xs" :aria-label="t('httpIntegration.fallback')" /></td>
                  <td class="p-1.5"><Textarea v-model="field.enum" rows="1" class="min-h-8 min-w-32 font-mono text-xs" :aria-label="t('httpIntegration.enum')" /></td>
                  <td class="p-1.5"><Button size="sm" variant="ghost" @click="endpoint.fields.splice(index, 1)">{{ t('httpIntegration.remove') }}</Button></td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>
        <section class="space-y-3 border-t border-border pt-3">
          <HttpKeyValueRows v-model="variables" secret :label="t('httpIntegration.variables')" />
          <p class="text-xs text-muted-foreground">{{ t('httpIntegration.variablesHint') }}</p>
          <label class="block text-sm font-medium">{{ t('httpIntegration.response') }}</label>
          <Textarea v-model="response" rows="5" class="font-mono text-xs" :aria-label="t('httpIntegration.response')" autocomplete="off" spellcheck="false" />
          <p class="text-xs text-muted-foreground">{{ readOnlyOperation ? t('httpIntegration.networkWarning') : t('httpIntegration.tradingOnly') }}</p>
          <label v-if="readOnlyOperation" class="flex items-center gap-2 text-xs"><Checkbox v-model="networkConfirmed" :disabled="busy || dirty" />{{ t('httpIntegration.networkConfirmed') }}</label>
          <p v-if="!savedConfig.endpoints[operation]" class="text-xs text-muted-foreground">{{ t('httpIntegration.endpointMissing') }}</p>
          <div class="flex gap-2">
            <Button size="sm" variant="outline" :disabled="!canDebug || !response.trim()" @click="debug('map_response')">{{ t('httpIntegration.map') }}</Button>
            <Button size="sm" :disabled="!canDebug || !readOnlyOperation || !networkConfirmed" @click="debug('test')">{{ t('httpIntegration.test') }}</Button>
          </div>
        </section>
      </fieldset>
      <p v-if="error" role="alert" class="text-sm text-destructive whitespace-pre-wrap break-all">{{ error }}</p>
      <section class="space-y-2 border-t border-border pt-3">
        <h3 class="text-sm font-medium">{{ t('httpIntegration.result') }}</h3>
        <p v-if="!result && mapped === undefined" class="text-xs text-muted-foreground">{{ t('httpIntegration.noResult') }}</p>
        <template v-if="result">
          <p v-if="result.error" role="alert" class="text-sm text-destructive whitespace-pre-wrap">{{ result.error }}</p>
          <p class="text-xs text-muted-foreground">{{ t('httpIntegration.status') }}: {{ result.status_code }} · {{ t('httpIntegration.duration') }}: {{ result.duration_ms }}</p>
          <pre class="max-h-72 overflow-auto rounded-md border border-border bg-muted/30 p-3 text-xs whitespace-pre-wrap break-all">{{ pretty(result.body) }}</pre>
        </template>
        <template v-if="mapped !== undefined"><h4 class="text-xs font-medium">{{ t('httpIntegration.mapped') }}</h4><pre class="max-h-72 overflow-auto rounded-md border border-border bg-muted/30 p-3 text-xs whitespace-pre-wrap break-all">{{ pretty(mapped) }}</pre></template>
      </section>
    </template>
  </div>
</template>
