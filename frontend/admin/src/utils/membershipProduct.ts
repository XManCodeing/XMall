import type { MembershipProduct, MembershipTermKind, MembershipProductInput } from '@/api/telegramMembership'
import type { LocalizedText } from '@/api/types'
import { parsePaymentChannelIDs } from './paymentChannelDisplay'

export interface MembershipSkuDraft {
  id: number
  sku_code: string
  spec_values: Record<string, string>
  price_amount: number
  cost_price_amount: number
  manual_stock_total?: number
  is_active: boolean
  sort_order: number
  kind: MembershipTermKind
  months: number
}
export interface MembershipDraft {
  title: LocalizedText
  description: LocalizedText
  content: LocalizedText
  slug: string
  category_id: number
  expected_version: number
  target_ids: number[]
  skus: MembershipSkuDraft[]
}
/** Create one editable row from existing SKU fields; omitted input means the hidden single-spec DEFAULT. */
export function membershipSku(): MembershipSkuDraft {
  return { id: 0, sku_code: 'DEFAULT', spec_values: {}, price_amount: 0, cost_price_amount: 0, manual_stock_total: -1, is_active: true, sort_order: 0, kind: 'months', months: 1 }
}
/** Restore all SKU prices and terms from one versioned product response, including DEFAULT. */
export function membershipDraft(product?: MembershipProduct): MembershipDraft {
  const empty = () => ({ 'zh-CN': '', 'zh-TW': '', 'en-US': '' })
  return {
    title: { ...empty(), ...product?.title }, description: { ...empty(), ...product?.description },
    content: { ...empty(), ...product?.content }, slug: product?.slug || '', category_id: product?.category_id || 0,
    expected_version: product?.membership.version || 0,
    target_ids: [...(product?.membership.target_ids || [])],
    skus: product?.skus?.length ? product.skus.map(sku => {
      const term = product.membership.terms.find(term => term.sku_id === sku.id || term.sku_code === sku.sku_code)
      // Preserve live stock: don't send a stale stock counter during an unrelated edit.
      return { id: sku.id, sku_code: sku.sku_code, spec_values: { ...sku.spec_values }, price_amount: Number(sku.price_amount),
        cost_price_amount: Number(sku.cost_price_amount), is_active: sku.is_active, sort_order: sku.sort_order,
        kind: term?.kind || 'months', months: term?.months ?? 1 }
    }) : [membershipSku()],
  }
}
/** Validate the complete draft; returns false for unsafe integers, duplicates or incomplete prices. */
export function validMembershipDraft(draft: MembershipDraft): boolean {
  const codes = draft.skus.map(sku => sku.sku_code.trim())
  return Object.values(draft.title).some(title => title.trim()) && !!draft.slug.trim() && draft.category_id > 0 &&
    draft.target_ids.length > 0 && draft.target_ids.every(id => Number.isSafeInteger(id) && id > 0) &&
    draft.skus.length > 0 && new Set(codes).size === codes.length && draft.skus.every(sku =>
      // Positive prices must remain positive after the backend rounds to two decimal places.
      !!sku.sku_code.trim() && Number.isFinite(Number(sku.price_amount)) && Number(sku.price_amount) >= 0.005 &&
      (sku.kind === 'permanent' || (sku.kind === 'months' && Number.isInteger(Number(sku.months)) && Number(sku.months) > 0 && Number(sku.months) <= 3558)))
}
/** Produce one atomic product+configuration payload; product input preserves untouched existing fields. */
export function membershipPayload(draft: MembershipDraft, product?: MembershipProduct): MembershipProductInput {
  const skus = draft.skus.map(({ kind: _kind, months: _months, ...sku }) => ({ ...sku, sku_code: sku.sku_code.trim(), price_amount: Number(sku.price_amount) }))
  const original = product || { images: [], tags: [], seo_meta: {}, stock_display_mode: 'hidden', fulfillment_type: 'manual', manual_stock_total: -1, is_affiliate_enabled: false, sort_order: 0 }
  return {
    images: original.images, tags: original.tags, seo_meta: original.seo_meta,
    stock_display_mode: original.stock_display_mode, fulfillment_type: original.fulfillment_type,
    is_affiliate_enabled: original.is_affiliate_enabled, sort_order: original.sort_order,
    manual_stock_total: product ? undefined : -1,
    // PUT replaces these fields; preserve them explicitly without leaking response-only counters.
    instructions: product?.instructions ?? {},
    payment_channel_ids: parsePaymentChannelIDs(product?.payment_channel_ids),
    manual_form_schema: product?.manual_form_schema ?? {},
    title: { ...draft.title }, description: { ...draft.description }, content: { ...draft.content }, slug: draft.slug.trim(),
    category_id: draft.category_id, price_amount: skus[0]?.price_amount || 0, cost_price_amount: skus[0]?.cost_price_amount || 0,
    skus, purchase_type: 'member', min_purchase_quantity: 1, max_purchase_quantity: 1, is_active: product?.is_active ?? false,
    membership: { expected_version: draft.expected_version, enabled: product?.membership.enabled ?? false, quantity: 1, target_ids: [...draft.target_ids],
      terms: draft.skus.map(sku => ({ sku_code: sku.sku_code.trim(), kind: sku.kind, months: sku.kind === 'permanent' ? 0 : Number(sku.months) })) },
  }
}
