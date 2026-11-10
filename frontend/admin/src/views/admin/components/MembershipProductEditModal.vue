<script setup lang="ts">
// Adapted from ProductEditModal: the same language tabs, form grid, SKU table and dialog components.
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import TelegramBotSelect from '@/components/telegram/TelegramBotSelect.vue'
import { Label } from '@/components/ui/label'
import { Checkbox } from '@/components/ui/checkbox'
import { Textarea } from '@/components/ui/textarea'
import { Dialog, DialogScrollContent, DialogHeader, DialogTitle, DialogDescription } from '@/components/ui/dialog'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useMembershipProductEditor } from '@/composables/useMembershipProductEditor'
import { membershipSku } from '@/utils/membershipProduct'
import { adminUrl } from '@/utils/adminBase'
import ProductSaveRecovery from './ProductSaveRecovery.vue'
import ProductCategoryField from './ProductCategoryField.vue'
const { t } = useI18n()
const currentLang = ref('zh-CN')
const editor = useMembershipProductEditor()
const { open, loading, saving, conflict, error, groupError, groupsLoading, botFilter, product, draft, categories,
  canGroups, canSave, valid, visibleGroups, missingIDs, mixedBots } = editor
const emit = defineEmits<{ success: [] }>()
/** Expose authorized editor entry to the copied product list; id omitted creates a new product. */
function edit(id?: number) { currentLang.value = 'zh-CN'; void editor.edit(id) }
/** Emit refresh only after the atomic request succeeds. */
async function submit() { if (await editor.save()) emit('success') }
const toggleTarget = editor.toggleTarget
/** Add a custom SKU row using existing price fields rather than a second pricing model. */
function addSku() { draft.value.skus.push({ ...membershipSku(), sku_code: '', spec_values: {} }) }
/** 重新读取群选择列表；只读取数据，不改变商品草稿。@returns 查询完成。 */
async function searchGroups(): Promise<void> {
  if (groupsLoading.value || loading.value || saving.value || !canGroups.value) return
  await editor.loadGroups()
}
/** 清空可选机器人筛选并重新读取列表，保留已选群与商品字段。@returns 查询完成。 */
async function resetGroupFilters(): Promise<void> {
  if (groupsLoading.value || loading.value || saving.value || !canGroups.value) return
  botFilter.value = ''
  await searchGroups()
}
defineExpose({ edit })
</script>

<template>
  <Dialog scope="page" :open="open" @update:open="value => { if (!value) editor.close() }">
    <DialogScrollContent class="sm:max-w-5xl">
      <DialogHeader><DialogTitle>{{ t('telegramMembership.products') }} · {{ t(product ? 'admin.common.edit' : 'admin.products.create') }}</DialogTitle></DialogHeader>
      <DialogDescription class="text-sm text-muted-foreground">{{ t('telegramMembership.closed') }}</DialogDescription>
      <p v-if="loading" role="status">{{ t('telegramBot.instances.loading') }}</p>
      <p v-if="error" role="alert" class="text-sm text-destructive">{{ error }}</p>
      <ProductSaveRecovery :recovery="editor.recovery" />
      <form class="space-y-6" @submit.prevent="submit">
        <fieldset :disabled="loading || saving || editor.recovery.unknown.value || !canSave" class="space-y-6">
          <div class="border-b border-border"><div class="flex gap-4 overflow-x-auto pb-1">
            <button v-for="language in ['zh-CN', 'zh-TW', 'en-US']" :key="language" type="button" class="shrink-0 border-b-2 px-4 py-2 text-sm font-medium"
              :class="currentLang === language ? 'border-primary text-foreground' : 'border-transparent text-muted-foreground hover:text-foreground'" @click="currentLang = language">{{ language }}</button>
          </div></div>
          <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
            <div class="md:col-span-2 space-y-1.5"><Label for="membership-title">{{ t('admin.products.form.title', { lang: currentLang }) }}</Label><Input id="membership-title" v-model="draft.title[currentLang]" :placeholder="t('admin.products.form.titlePlaceholder')" /></div>
            <div class="space-y-1.5"><Label for="membership-slug">{{ t('admin.products.form.slug') }}</Label><Input id="membership-slug" v-model="draft.slug" required /></div>
            <ProductCategoryField :key="editor.requestedID.value ?? 'new'" v-model="draft.category_id" :categories="categories" :active="open" @refreshed="value => categories = value" />
            <div class="md:col-span-2 space-y-1.5"><Label>{{ t('admin.products.form.description', { lang: currentLang }) }}</Label><Textarea v-model="draft.description[currentLang]" /></div>
          </div>
          <section class="space-y-3" aria-labelledby="membership-groups-heading">
            <div class="flex items-center justify-between gap-3"><h3 id="membership-groups-heading" class="text-sm font-semibold">{{ t('telegramMembership.targets') }} <span class="text-muted-foreground font-normal">{{ t('telegramMembership.selected', { count: draft.target_ids.length }) }}</span></h3><a v-if="canGroups" :href="adminUrl('/telegram-bot/groups')" target="_blank" rel="noopener noreferrer" class="text-xs underline underline-offset-4">{{ t('telegramMembership.manageGroups') }}</a></div>
            <p v-if="!canGroups" class="text-sm text-muted-foreground">{{ t('telegramMembership.groupsPermission') }}</p>
            <div v-else class="flex gap-3"><TelegramBotSelect v-model="botFilter" class="w-56" :disabled="groupsLoading || loading || saving" /><Button type="button" variant="outline" :disabled="groupsLoading || loading || saving" @click="searchGroups">{{ t('admin.common.search') }}</Button><Button type="button" variant="outline" :disabled="groupsLoading || loading || saving" @click="resetGroupFilters">{{ t('admin.common.reset') }}</Button></div>
            <p v-if="groupsLoading" role="status" class="text-sm text-muted-foreground">{{ t('telegramBot.instances.loading') }}</p>
            <p v-if="groupError" role="alert" class="text-sm text-destructive">{{ groupError }}</p>
            <p v-if="mixedBots" role="alert" class="text-sm text-destructive">{{ t('telegramMembership.sameBot') }}</p>
            <p v-else-if="draft.target_ids.length && !editor.validGroups.value && !groupsLoading" role="alert" class="text-sm text-destructive">{{ t('telegramMembership.invalidGroups') }}</p>
            <div class="rounded-xl border border-border bg-card overflow-x-auto"><Table><TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground"><TableRow><TableHead class="w-10 px-4 py-3" /><TableHead class="px-4 py-3">{{ t('telegramMembership.groupName') }}</TableHead><TableHead class="px-4 py-3">Bot</TableHead><TableHead class="px-4 py-3">{{ t('telegramMembership.groupStatus') }}</TableHead></TableRow></TableHeader><TableBody class="divide-y divide-border">
              <TableRow v-for="group in visibleGroups" :key="group.id" class="hover:bg-muted/30"><TableCell class="px-4 py-3"><Checkbox :id="`membership-target-${group.id}`" :model-value="draft.target_ids.includes(group.id)" :disabled="!draft.target_ids.includes(group.id) && (!canGroups || groupsLoading || !!groupError || !editor.available(group))" @update:model-value="value => toggleTarget(group.id, value)" /></TableCell><TableCell class="px-4 py-3"><Label :for="`membership-target-${group.id}`">{{ group.name }} · {{ group.id }}</Label></TableCell><TableCell class="px-4 py-3">{{ group.bot_instance_id }}</TableCell><TableCell class="px-4 py-3 text-xs" :class="editor.available(group) ? 'text-muted-foreground' : 'text-destructive'">{{ editor.status(group) }}</TableCell></TableRow>
              <TableRow v-for="id in missingIDs" :key="`missing-${id}`" class="hover:bg-muted/30"><TableCell class="px-4 py-3"><Checkbox :id="`membership-target-${id}`" :model-value="true" @update:model-value="value => toggleTarget(id, value)" /></TableCell><TableCell class="px-4 py-3"><Label :for="`membership-target-${id}`">{{ id }}</Label></TableCell><TableCell class="px-4 py-3">—</TableCell><TableCell class="px-4 py-3 text-xs text-destructive">{{ t('telegramMembership.missingGroup') }}</TableCell></TableRow>
              <TableRow v-if="!groupsLoading && !visibleGroups.length && !missingIDs.length"><TableCell colspan="4" class="px-4 py-8 text-center text-muted-foreground">{{ t('telegramBot.instances.empty') }}</TableCell></TableRow>
            </TableBody></Table></div>
          </section>
          <div class="space-y-3 rounded-xl border border-border bg-muted/20 p-4">
            <div class="flex items-center justify-between"><h3 class="text-sm font-semibold">{{ t('telegramMembership.term') }}</h3><Button type="button" variant="outline" size="sm" @click="addSku">{{ t('telegramMembership.addSku') }}</Button></div>
            <p class="text-xs text-muted-foreground">{{ t('telegramMembership.monthHint') }}</p>
            <div class="rounded-xl border border-border bg-card overflow-x-auto">
              <Table><TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground"><TableRow><TableHead>{{ t('telegramMembership.sku') }}</TableHead><TableHead>{{ t('telegramMembership.price') }}</TableHead><TableHead>{{ t('telegramMembership.term') }}</TableHead><TableHead>{{ t('telegramMembership.months') }}</TableHead><TableHead class="text-right" /></TableRow></TableHeader><TableBody class="divide-y divide-border">
                <TableRow v-for="(sku, index) in draft.skus" :key="index" class="hover:bg-muted/30">
                  <TableCell><Input v-model="sku.sku_code" :aria-label="t('telegramMembership.sku')" :disabled="sku.id > 0" /></TableCell>
                  <TableCell><Input v-model.number="sku.price_amount" type="number" min="0.01" step="0.01" required :aria-label="t('telegramMembership.price')" /></TableCell>
                  <TableCell><Select v-model="sku.kind"><SelectTrigger><SelectValue /></SelectTrigger><SelectContent><SelectItem value="months">{{ t('telegramMembership.months') }}</SelectItem><SelectItem value="permanent">{{ t('telegramMembership.permanent') }}</SelectItem></SelectContent></Select></TableCell>
                  <TableCell><Input v-if="sku.kind === 'months'" v-model.number="sku.months" type="number" min="1" max="3558" step="1" :aria-label="t('telegramMembership.months')" /><span v-else>—</span></TableCell>
                  <TableCell class="text-right"><div class="flex flex-wrap items-center justify-end gap-2"><Button type="button" size="sm" variant="outline" :disabled="draft.skus.length === 1" @click="draft.skus.splice(index, 1)">{{ t('telegramMembership.removeSku') }}</Button></div></TableCell>
                </TableRow>
              </TableBody></Table>
            </div>
          </div>
        </fieldset>
        <p v-if="!valid && !loading" class="text-sm text-muted-foreground">{{ t('telegramMembership.invalid') }}</p>
        <div class="flex justify-end gap-3 border-t border-border pt-4">
          <Button v-if="editor.requestedID.value" type="button" variant="outline" :disabled="saving || loading || editor.recovery.unknown.value" @click="editor.reload">{{ t('telegramMembership.reload') }}</Button>
          <Button type="button" variant="outline" :disabled="saving" @click="editor.close">{{ t('admin.common.cancel') }}</Button>
          <Button type="submit" :disabled="saving || loading || editor.recovery.unknown.value || conflict || !canSave || !valid">{{ t('telegramBot.settings.save') }}</Button>
        </div>
      </form>
    </DialogScrollContent>
  </Dialog>
</template>
