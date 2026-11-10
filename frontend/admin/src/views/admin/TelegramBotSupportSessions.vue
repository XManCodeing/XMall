<script setup lang="ts">
import { computed, onMounted } from 'vue'
import SupportNavigation from '@/components/telegram/SupportNavigation.vue'
import ListPagination from '@/components/ListPagination.vue'
import { membershipStatusVariant } from '@/utils/membershipStatus'
import { useI18n } from 'vue-i18n'
import { useTelegramSupportList } from '@/composables/useTelegramSupportList'
import { useTelegramSupportSession } from '@/composables/useTelegramSupportSession'
import type { SupportSession } from '@/api/telegramSupport'
import { Button } from '@/components/ui/button'
import TelegramBotSelect from '@/components/telegram/TelegramBotSelect.vue'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { supportSessionMessages } from '@/i18n/telegramSupportSessions'
import { formatDate } from '@/utils/format'

const { t: op } = useI18n({ useScope: 'local', messages: supportSessionMessages })
const { t, te } = useI18n({ useScope: 'global' })
const { can, canList, items, page, total, pageSize, loading, error, botFilter, stateFilter, unrepliedFilter, blockedFilter, topicFilter, failureFilter, validFilter, load, search, resetFilters } = useTelegramSupportList('sessions')
const rows = computed(() => items.value as SupportSession[])
const { open, loading: detailLoading, error: detailError, session, messages, page: messagePage, pageSize: messagePageSize, total: messageTotal,
  messageLoading, messageError, canMessages, canContent, content, contentID, contentLoading, contentError, show, loadMessages, viewContent, clearContent } = useTelegramSupportSession()
const states = ['waiting_verification', 'open', 'closed', 'cooldown']
/** Translate finite server states and media kinds; unknown future values have a safe fallback. */
const label = (value?: string) => value && te(`telegramSupport.values.${value}`) ? t(`telegramSupport.values.${value}`) : t('telegramSupport.values.unknown')
const filters = [
  { key: 'replyStatus', model: unrepliedFilter, yes: 'unreplied', no: 'replied' },
  { key: 'blockStatus', model: blockedFilter, yes: 'blocked', no: 'notBlocked' },
  { key: 'topic', model: topicFilter, yes: 'topicYes', no: 'topicNo' },
]
/** Accept only the server's numeric Telegram topic link; never render arbitrary schemes. */
const topicURL = (row: SupportSession) => /^https:\/\/t\.me\/c\/[1-9]\d*\/[1-9]\d*$/.test(row.topic_url || '') ? row.topic_url : ''
onMounted(() => { void load() })
</script>

<template>
  <div class="-mt-2 space-y-6 md:-mt-4">
    <SupportNavigation data-guide="support-tabs" active="sessions" class="mb-2" />
    <p v-if="!canList" class="text-muted-foreground">{{ t('telegramBot.groups.forbidden') }}</p>
    <div v-if="canList" data-guide="support-filter" class="flex flex-wrap items-end gap-3">
      <div class="w-56 space-y-2"><Label for="support-session-bot">{{ t('telegramBot.groups.instance') }}</Label><TelegramBotSelect id="support-session-bot" v-model="botFilter" :disabled="loading" /></div>
      <div class="w-48 space-y-2"><Label>{{ t('telegramBot.instances.state') }}</Label><Select v-model="stateFilter" :disabled="loading"><SelectTrigger><SelectValue /></SelectTrigger><SelectContent><SelectItem value="all">{{ t('telegramBot.groups.all') }}</SelectItem><SelectItem v-for="state in states" :key="state" :value="state">{{ label(state) }}</SelectItem></SelectContent></Select></div>
      <div v-for="filter in filters" :key="filter.key" class="w-40 space-y-2"><Label>{{ op(filter.key) }}</Label><Select v-model="filter.model.value" :disabled="loading"><SelectTrigger><SelectValue /></SelectTrigger><SelectContent><SelectItem value="all">{{ t('telegramBot.groups.all') }}</SelectItem><SelectItem value="true">{{ op(filter.yes) }}</SelectItem><SelectItem value="false">{{ op(filter.no) }}</SelectItem></SelectContent></Select></div>
      <div class="w-44 space-y-2"><Label>{{ op('failure') }}</Label><Select v-model="failureFilter" :disabled="loading"><SelectTrigger><SelectValue /></SelectTrigger><SelectContent><SelectItem value="all">{{ t('telegramBot.groups.all') }}</SelectItem><SelectItem v-for="value in ['any', 'upstream', 'downstream', 'none']" :key="value" :value="value">{{ op(value) }}</SelectItem></SelectContent></Select></div>
      <div class="flex gap-2">
        <Button variant="outline" :disabled="loading || !validFilter" @click="search">{{ t('admin.common.search') }}</Button>
        <Button variant="outline" :disabled="loading" @click="resetFilters">{{ t('admin.common.reset') }}</Button>
      </div>
    </div>
    <p v-if="loading" role="status" class="text-muted-foreground">{{ t('telegramBot.instances.loading') }}</p>
    <div v-if="canList" data-guide="support-list" class="rounded-xl border border-border bg-card overflow-hidden">
      <Table><TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground"><TableRow>
        <TableHead class="px-6 py-3">{{ t('telegramSupport.sessionID') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.groups.instance') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramSupport.userID') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.instances.state') }}</TableHead><TableHead class="px-6 py-3">{{ op('replyStatus') }}</TableHead><TableHead class="px-6 py-3">{{ op('blockStatus') }}</TableHead><TableHead class="px-6 py-3">{{ op('failure') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramSupport.updatedAt') }}</TableHead><TableHead class="px-6 py-3 text-right">{{ t('telegramBot.instances.actions') }}</TableHead>
      </TableRow></TableHeader><TableBody class="divide-y divide-border">
        <TableRow v-for="row in rows" :key="row.id" class="hover:bg-muted/30">
          <TableCell class="px-6 py-4">{{ row.id }}</TableCell><TableCell class="px-6 py-4">{{ row.bot_id }}</TableCell><TableCell class="px-6 py-4 font-mono">{{ row.user_id }}</TableCell><TableCell class="px-6 py-4"><Badge :variant="membershipStatusVariant(row.state)">{{ label(row.state) }}</Badge></TableCell>
          <TableCell class="px-6 py-4"><Badge :variant="membershipStatusVariant(row.unreplied ? 'unreplied' : 'replied')">{{ op(row.unreplied ? 'unreplied' : 'replied') }}</Badge><p v-if="row.unreplied_since" class="text-xs text-muted-foreground">{{ formatDate(row.unreplied_since) }}</p></TableCell>
          <TableCell class="px-6 py-4"><Badge :variant="membershipStatusVariant(row.blocked ? 'blocked' : 'not_blocked')">{{ op(row.blocked ? 'blocked' : 'notBlocked') }}</Badge><p v-if="row.blocked" class="text-xs text-muted-foreground">{{ row.block_expires_at ? op('expires', { time: formatDate(row.block_expires_at) }) : op('permanent') }}</p></TableCell>
          <TableCell class="px-6 py-4 text-xs"><p>{{ op('upstreamSummary', { failed: row.upstream_failed, unknown: row.upstream_unknown }) }}</p><p>{{ op('downstreamSummary', { failed: row.downstream_failed, unknown: row.downstream_unknown }) }}</p></TableCell>
          <TableCell class="px-6 py-4">{{ formatDate(row.updated_at) }}</TableCell>
          <TableCell class="px-6 py-4 text-right"><div data-guide="support-actions" class="flex flex-wrap items-center justify-end gap-2">
            <Button v-if="can('GET', `/${row.id}`)" size="sm" variant="outline" @click="show(row.id)" :title="t('telegramBot.groups.detail')" :aria-label="t('telegramBot.groups.detail')">{{ t('tableAction.detail') }}</Button>
            <Button v-if="topicURL(row)" as-child size="sm" variant="outline"><a :href="topicURL(row)" target="_blank" rel="noopener noreferrer">{{ op('openTopic') }}</a></Button>
          </div></TableCell>
        </TableRow>
        <TableRow v-if="!rows.length && !loading && !error"><TableCell :colspan="9" class="px-6 py-8 text-center text-muted-foreground">{{ t('telegramBot.instances.empty') }}</TableCell></TableRow>
      </TableBody></Table>
      <ListPagination always-show :page="page" :page-size="pageSize" :page-size-options="[10, 20, 50, 100]" :total="total" :total-page="Math.ceil(total / pageSize)" :loading="loading" :disabled="!validFilter" @change-page="load" @change-page-size="size => { pageSize = size; load(1) }" />
    </div>

    <Dialog v-model:open="open"><DialogContent class="sm:max-w-5xl max-h-[85vh] overflow-y-auto"><DialogHeader><DialogTitle>{{ t('telegramSupport.sessionDetail') }}{{ session ? ` · ${session.id}` : '' }}</DialogTitle></DialogHeader>
      <p v-if="detailLoading" role="status">{{ t('telegramBot.instances.loading') }}</p><p v-if="detailError" role="alert" class="text-sm text-destructive">{{ detailError }}</p>
      <template v-if="session">
        <dl class="grid grid-cols-2 gap-x-6 gap-y-2 text-sm"><div v-for="entry in [['userID', session.user_id], ['chatID', session.chat_id], ['version', session.version], ['createdAt', formatDate(session.created_at)]]" :key="String(entry[0])" class="flex justify-between gap-3 border-b border-border py-1"><dt class="text-muted-foreground">{{ t(`telegramSupport.${entry[0]}`) }}</dt><dd class="break-all text-right">{{ entry[1] }}</dd></div></dl>
        <dl class="grid grid-cols-2 gap-x-6 gap-y-2 text-sm"><div v-for="entry in [['replyStatus', op(session.unreplied ? 'unreplied' : 'replied')], ['since', session.unreplied_since ? formatDate(session.unreplied_since) : '—'], ['lastCustomer', session.last_customer_delivered_at ? formatDate(session.last_customer_delivered_at) : '—'], ['lastReply', session.last_reply_delivered_at ? formatDate(session.last_reply_delivered_at) : '—'], ['blockStatus', session.blocked ? (session.block_expires_at ? op('expires', { time: formatDate(session.block_expires_at) }) : op('permanent')) : op('notBlocked')]]" :key="String(entry[0])" class="flex justify-between gap-3 border-b border-border py-1"><dt class="text-muted-foreground">{{ op(String(entry[0])) }}</dt><dd>{{ entry[1] }}</dd></div></dl>
        <p class="text-sm">{{ op('upstreamSummary', { failed: session.upstream_failed, unknown: session.upstream_unknown }) }} · {{ op('downstreamSummary', { failed: session.downstream_failed, unknown: session.downstream_unknown }) }}</p>
        <p class="text-sm"><a v-if="topicURL(session)" :href="topicURL(session)" target="_blank" rel="noopener noreferrer" class="underline underline-offset-4">{{ op('openTopic') }} · {{ session.topic_chat_id }} / {{ session.topic_thread_id }} · {{ label(session.topic_state) }}</a><span v-else class="text-muted-foreground">{{ op('topicNo') }}</span></p>
        <p class="text-sm text-muted-foreground">{{ op('semantics') }}</p>
        <p class="text-sm text-muted-foreground">{{ op('topicHint') }}</p>
        <p class="text-sm text-muted-foreground">{{ t('telegramSupport.contentHint') }}</p>
        <p v-if="!canMessages" class="text-sm text-muted-foreground">{{ t('telegramBot.groups.forbidden') }}</p>
        <p v-if="messageLoading" role="status">{{ t('telegramBot.instances.loading') }}</p><p v-if="messageError" role="alert" class="text-sm text-destructive">{{ messageError }}</p>
        <template v-if="canMessages">
          <div class="rounded-xl border border-border bg-card overflow-hidden">
            <Table><TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground"><TableRow>
              <TableHead class="px-6 py-3">{{ t('telegramSupport.messageID') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramSupport.sequence') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramBot.instances.state') }}</TableHead><TableHead class="px-6 py-3">{{ t('telegramSupport.retainUntil') }}</TableHead><TableHead class="px-6 py-3 text-right">{{ t('telegramBot.instances.actions') }}</TableHead>
            </TableRow></TableHeader><TableBody class="divide-y divide-border">
              <TableRow v-for="message in messages" :key="message.id" class="hover:bg-muted/30"><TableCell class="px-6 py-4">{{ message.id }}</TableCell><TableCell class="px-6 py-4">{{ message.sequence }}</TableCell><TableCell class="px-6 py-4"><Badge :variant="membershipStatusVariant(message.state === 'ready' ? 'queued' : message.state)">{{ label(message.state) }}</Badge></TableCell><TableCell class="px-6 py-4">{{ formatDate(message.retain_until) }}</TableCell><TableCell class="px-6 py-4 text-right"><div class="flex flex-wrap items-center justify-end gap-2"><Button v-if="canContent" size="sm" variant="outline" :disabled="contentLoading" @click="viewContent(message)" :title="t('telegramSupport.viewContent')" :aria-label="t('telegramSupport.viewContent')">{{ t('tableAction.content') }}</Button><span v-else class="text-xs text-muted-foreground">{{ t('telegramSupport.contentForbidden') }}</span></div></TableCell></TableRow>
              <TableRow v-if="!messages.length && !messageLoading"><TableCell :colspan="5" class="px-6 py-8 text-center text-muted-foreground">{{ messageError || t('telegramBot.instances.empty') }}</TableCell></TableRow>
            </TableBody></Table>
            <ListPagination always-show :page="messagePage" :page-size="messagePageSize" :page-size-options="[10, 20, 50, 100]" :total="messageTotal" :total-page="Math.ceil(messageTotal / messagePageSize)" :loading="messageLoading" @change-page="loadMessages" @change-page-size="size => { messagePageSize = size; loadMessages(1) }" />
          </div>
        </template>
        <section v-if="contentID !== null" class="space-y-3 rounded-lg border border-border p-4">
          <div class="flex items-center justify-between"><h3 class="font-medium">{{ t('telegramSupport.contentTitle', { id: contentID }) }}</h3><Button size="sm" variant="ghost" @click="clearContent">{{ t('telegramSupport.hideContent') }}</Button></div>
          <p v-if="contentLoading" role="status">{{ t('telegramBot.instances.loading') }}</p><p v-if="contentError" role="alert" class="text-sm text-destructive">{{ contentError }}</p>
          <template v-if="content"><template v-if="content.available"><p class="text-sm text-muted-foreground">{{ label(content.kind) }}<template v-if="content.media_kinds?.length"> · {{ content.media_kinds.map(label).join(', ') }}</template></p><p v-if="content.text" class="whitespace-pre-wrap break-words text-sm">{{ content.text }}</p><p v-if="content.caption" class="whitespace-pre-wrap break-words text-sm">{{ content.caption }}</p><p v-if="!content.text && !content.caption" class="text-sm text-muted-foreground">{{ t('telegramSupport.noText') }}</p></template><p v-else class="text-sm text-muted-foreground">{{ t(content.reason === 'expired' ? 'telegramSupport.expired' : content.reason === 'cleared' ? 'telegramSupport.cleared' : 'telegramSupport.unavailable') }}</p></template>
        </section>
      </template>
    </DialogContent></Dialog>
  </div>
</template>
