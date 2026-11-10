<script setup lang="ts">
import { formatDate as formatBusinessDate } from '@/utils/format'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Users } from 'lucide-vue-next'
import PanelHeading from '@/components/shared/PanelHeading.vue'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { useAppStore } from '@/stores/app'
import { useMembershipGroups } from '@/composables/useMembershipGroups'
import { canRequestMembership } from '@/utils/membershipGroups'

const { t } = useI18n()
const appStore = useAppStore()
const { items, nextCursor, loading, error, invitations, requesting, load, requestInvitation, checkInvitation } = useMembershipGroups()

// 复用店面客服设置；排除 Telegram Bot，故障恢复不能只依赖同一 Bot。
const supportLinks = computed(() => {
  const contact = appStore.config?.contact
  const links: { href: string; label: string }[] = []
  if (contact?.email && /^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$/.test(contact.email)) {
    links.push({ href: `mailto:${contact.email}`, label: 'footer.email' })
  }
  for (const [href, label] of [[contact?.support_url, 'footer.supportUrl'], [contact?.whatsapp, 'footer.whatsapp']]) {
    if (!href) continue
    try {
      const url = new URL(href, window.location.origin)
      if ((url.protocol === 'https:' || (href.startsWith('/') && !href.startsWith('//')))
        && !url.username && !url.password && !/(^|\.)(t\.me|telegram\.me|telegram\.org)$/i.test(url.hostname)) {
        links.push({ href: url.href, label: label! })
      }
    } catch {
      // 无效配置不作为可点击的客服入口。
      continue
    }
  }
  return links
})

/** 按东八区显示真实截止时刻。@param raw ISO 时间。@returns 格式化时间或待确认文案。 */
function formatExpiry(raw?: string): string {
  const formatted = formatBusinessDate(raw)
  return formatted === '—' ? t('membershipGroups.expiryUnknown') : formatted
}
</script>

<template>
  <div class="min-w-0 space-y-4 pb-[env(safe-area-inset-bottom)]">
    <PanelHeading :title="t('membershipGroups.title')" :description="t('membershipGroups.description')" :icon="Users">
      <template #actions>
        <Button variant="outline" class="min-h-11" :disabled="loading || !!requesting" @click="load()">
          {{ t('membershipGroups.refresh') }}
        </Button>
        <Button as-child variant="ghost" class="min-h-11">
          <router-link to="/products">{{ t('membershipGroups.renew') }}</router-link>
        </Button>
      </template>
    </PanelHeading>

    <p v-if="loading" role="status" class="rounded-2xl border bg-card p-4 text-sm text-muted-foreground">{{ t('membershipGroups.loading') }}</p>
    <p v-if="error" role="alert" class="rounded-2xl border bg-card p-4 text-sm text-destructive">{{ t('membershipGroups.loadFailed') }}</p>
    <p v-else-if="!loading && !items.length" class="rounded-2xl border bg-card p-6 text-sm text-muted-foreground">{{ t('membershipGroups.empty') }}</p>

    <div v-if="items.length" class="overflow-hidden rounded-2xl border bg-card shadow-sm" :aria-busy="loading">
      <div class="hidden grid-cols-[minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)_auto] gap-4 border-b bg-muted px-4 py-3 text-xs font-semibold text-muted-foreground xl:grid" aria-hidden="true">
        <span>{{ t('membershipGroups.group') }}</span><span>{{ t('membershipGroups.expiry') }}</span><span>{{ t('membershipGroups.status') }}</span><span>{{ t('membershipGroups.action') }}</span>
      </div>
      <ul class="divide-y">
        <li v-for="group in items" :key="group.target_id" class="grid min-w-0 gap-3 p-4 xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)_auto] xl:items-center xl:gap-4">
          <div class="min-w-0">
            <h3 class="break-words text-sm font-semibold text-foreground">{{ group.title }}</h3>
            <p class="mt-1 break-all text-xs text-muted-foreground">{{ t('membershipGroups.identity', { id: group.beneficiary_telegram_id }) }}</p>
          </div>
          <p class="text-sm text-muted-foreground">
            {{ group.permanent ? t('membershipGroups.permanent') : t('membershipGroups.expiresAt', { date: formatExpiry(group.expires_at) }) }}
          </p>
          <div class="space-y-2" aria-live="polite">
            <Badge variant="neutral">{{ t(`membershipGroups.states.${group.state}`) }}</Badge>
            <p v-if="['recovering', 'unknown', 'identity_mismatch', 'processing'].includes(group.state)" class="text-xs text-muted-foreground">{{ t(`membershipGroups.hints.${group.state}`) }}</p>
          </div>
          <div class="xl:w-28">
            <Button v-if="invitations[group.target_id]?.invite_url" as-child class="min-h-11 w-full">
              <a :href="invitations[group.target_id]?.invite_url" target="_blank" rel="noopener noreferrer" referrerpolicy="no-referrer" @click="checkInvitation(group, $event)">{{ t('membershipGroups.apply') }}</a>
            </Button>
            <Button v-else class="min-h-11 w-full" :disabled="loading || !!requesting || !canRequestMembership(group)" @click="requestInvitation(group)">
              {{ requesting === group.target_id ? t('membershipGroups.requesting') : t('membershipGroups.getInvitation') }}
            </Button>
          </div>
        </li>
      </ul>
    </div>
    <Button v-if="nextCursor" variant="outline" class="min-h-11 w-full xl:w-auto" :disabled="loading || !!requesting" @click="load(true)">{{ t('membershipGroups.loadMore') }}</Button>
    <div class="rounded-2xl border bg-card p-4 text-sm">
      <h3 class="font-semibold">{{ t('membershipGroups.support') }}</h3>
      <div class="mt-1 flex flex-wrap gap-x-4">
        <a v-for="link in supportLinks" :key="link.href" :href="link.href" target="_blank" rel="noopener noreferrer" class="inline-flex min-h-11 items-center text-primary underline underline-offset-4">{{ t(link.label) }}</a>
        <router-link to="/about" class="inline-flex min-h-11 items-center text-primary underline underline-offset-4">{{ t('membershipGroups.contactPage') }}</router-link>
      </div>
      <p v-if="!supportLinks.length" class="text-xs text-muted-foreground">{{ t('membershipGroups.supportUnavailable') }}</p>
    </div>
  </div>
</template>
