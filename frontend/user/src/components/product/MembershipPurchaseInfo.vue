<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useUserAuthStore } from '@/stores/userAuth'
import { purchaseSelection, savePurchaseContext } from '@/utils/purchaseContext'
import { membershipGroupNames, membershipSalesBlocked, membershipTermLabel } from '@/utils/membershipPurchase'
import { Button } from '@/components/ui/button'

const props = defineProps<{ product: any; skuId?: number; returnPath?: string; items?: unknown[] }>()
const { t } = useI18n()
const router = useRouter()
const auth = useUserAuthStore()
const label = computed(() => membershipTermLabel(props.product, props.skuId, t))
const groupNames = computed(() => membershipGroupNames(props.product))
const failed = ref(false)

/** Preserve minimal SKU/source context and use the existing security binding UI. */
async function guideBinding(): Promise<void> {
  failed.value = false
  try {
    await navigateBinding()
  } catch {
    failed.value = true
    console.warn('Membership binding navigation failed')
  }
}

/** Validate selections before storing context or navigating; never store amounts. */
async function navigateBinding(): Promise<void> {
  const returnPath = props.returnPath || `/products/${props.product.slug}`
  const items = (props.items || [{ productId: props.product.id, skuId: props.skuId,
    slug: props.product.slug, quantity: 1 }]).map(purchaseSelection)
  if (!items.length || items.some((item) => !item)) return
  if (!auth.isAuthenticated) {
    const redirect = returnPath.startsWith('/products/') ? `${returnPath}?sku_id=${props.skuId || ''}` : returnPath
    await router.push({ path: '/auth/login', query: { redirect } })
    return
  }
  const saved = savePurchaseContext({ version: 1, accountId: Number(auth.user?.id), issuedAt: Date.now(),
    returnPath, items: items.filter((item) => item !== null) })
  if (!saved) { failed.value = true; return }
  await router.push('/me/security')
}
</script>

<template>
  <section v-if="product?.membership != null" class="my-4 space-y-2 rounded-xl border border-border bg-secondary p-4 text-sm">
    <p class="font-medium text-foreground">{{ label }}</p>
    <div v-if="groupNames.length" class="space-y-1 text-muted-foreground">
      <p>{{ t('membershipPurchase.groups') }}</p>
      <ul class="list-inside list-disc space-y-1">
        <li v-for="(name, index) in groupNames" :key="index" class="break-all">{{ name }}</li>
      </ul>
    </div>
    <p class="text-muted-foreground">{{ t('membershipPurchase.fixedMonth') }}</p>
    <p v-if="membershipSalesBlocked(product)" role="status" class="text-muted-foreground">{{ t('membershipPurchase.closed') }}</p>
    <p v-if="failed" role="alert" class="text-destructive">{{ t('membershipPurchase.contextFailed') }}</p>
    <Button variant="secondary" class="min-h-11" :disabled="!skuId" @click="guideBinding">
      {{ t(auth.isAuthenticated ? 'membershipPurchase.binding' : 'membershipPurchase.login') }}
    </Button>
  </section>
</template>
