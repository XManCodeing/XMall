import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAdminAuthStore } from '@/stores/auth'
import {
  LayoutDashboard, ShoppingBag, CreditCard, Package, Users, FileText, BadgePercent,
  Settings, FolderTree, Boxes, KeyRound, Download, ListOrdered, WalletCards,
  ReceiptText, UserRound, Wallet, History, Images, Newspaper, Ticket, Gift,
  UserPlus, SlidersHorizontal, ShieldCheck, ScrollText, Link, Truck,
  ClipboardCheck, Lock, Bot, Send, Crown, Bell, ImageIcon, MessagesSquare, ArrowRightLeft,
} from 'lucide-vue-next'

/** 导航子项，沿用菜单路由、权限及图标。 */
export interface NavGroupItem {
  label: string
  to: string
  permission?: string
  icon?: any
}

/** 后台导航分组。 */
export interface NavGroup {
  id: string
  label: string
  icon: any
  items: NavGroupItem[]
}

/**
 * 创建响应语言与权限变化的后台菜单。
 * @returns 包含原有权限过滤规则的 navGroups 计算属性。
 */
export function useAdminNavigation() {
  const { t } = useI18n()
  const authStore = useAdminAuthStore()
  const navGroups = computed<NavGroup[]>(() => {
    const groups: NavGroup[] = [
      { id: 'products', label: t('admin.navGroups.productManagement'), icon: Package, items: [
        { label: t('admin.navItems.productCategories'), to: '/categories', icon: FolderTree, permission: 'GET:/admin/categories' },
        { label: t('admin.navItems.productList'), to: '/products', icon: Boxes, permission: 'GET:/admin/products' },
        { label: t('admin.navItems.cardSecrets'), to: '/card-secrets', icon: KeyRound, permission: 'GET:/admin/card-secrets' },
        { label: t('admin.navItems.cardSecretImports'), to: '/card-secret-imports', icon: KeyRound, permission: 'GET:/admin/card-secrets' },
        { label: t('admin.navItems.cardSecretExports'), to: '/card-secret-exports', icon: Download, permission: 'GET:/admin/card-secrets' },
      ] }, { id: 'orders', label: t('admin.navGroups.orderManagement'), icon: ShoppingBag, items: [
        { label: t('admin.navItems.orderList'), to: '/orders', icon: ListOrdered, permission: 'GET:/admin/orders' },
        { label: t('admin.navItems.orderRiskControl'), to: '/order-risk-control', icon: ShieldCheck, permission: 'GET:/admin/settings' },
        { label: t('admin.navItems.orderRefunds'), to: '/order-refunds', icon: ReceiptText, permission: 'GET:/admin/order-refunds' },
      ] }, { id: 'payments', label: t('admin.navGroups.paymentManagement'), icon: CreditCard, items: [
        { label: t('admin.navItems.paymentChannels'), to: '/payment-channels', icon: WalletCards, permission: 'GET:/admin/payment-channels' },
        { label: t('admin.navItems.payments'), to: '/payments', icon: ReceiptText, permission: 'GET:/admin/payments' },
        { label: t('admin.navItems.callbackRoutes'), to: '/callback-routes', icon: Link, permission: 'GET:/admin/settings' },
      ] }, { id: 'users', label: t('admin.navGroups.userManagement'), icon: Users, items: [
        { label: t('admin.navItems.userList'), to: '/users', icon: UserRound, permission: 'GET:/admin/users' },
        { label: t('admin.navItems.walletManagement'), to: '/wallet-recharges', icon: Wallet, permission: 'GET:/admin/wallet/recharges' },
        { label: t('admin.navItems.walletConfig'), to: '/wallet-config', icon: Wallet, permission: 'GET:/admin/settings' },
        { label: t('admin.navItems.userLoginLogs'), to: '/user-login-logs', icon: History, permission: 'GET:/admin/user-login-logs' },
        { label: t('admin.navItems.memberLevels'), to: '/member-levels', icon: Crown, permission: 'GET:/admin/member-levels' },
      ] }, { id: 'articles', label: t('admin.navGroups.articleManagement'), icon: Newspaper, items: [
        { label: t('admin.navItems.articleList'), to: '/posts/blog', icon: FileText, permission: 'GET:/admin/posts' },
        { label: t('admin.navItems.postCategories'), to: '/posts/categories', icon: FolderTree, permission: 'GET:/admin/post-categories' },
      ] }, { id: 'content', label: t('admin.navGroups.contentManagement'), icon: FileText, items: [
        { label: t('admin.navItems.banners'), to: '/banners', icon: Images, permission: 'GET:/admin/banners' },
        { label: t('admin.navItems.media'), to: '/media', icon: ImageIcon, permission: 'GET:/admin/media' },
        { label: t('admin.navItems.announcementList'), to: '/posts/notice', icon: Bell, permission: 'GET:/admin/posts' },
      ] }, { id: 'marketing', label: t('admin.navGroups.marketingManagement'), icon: BadgePercent, items: [
        { label: t('admin.navItems.coupons'), to: '/coupons', icon: Ticket, permission: 'GET:/admin/coupons' },
        { label: t('admin.navItems.promotions'), to: '/promotions', icon: BadgePercent, permission: 'GET:/admin/promotions' },
        { label: t('admin.navItems.wholesalePrices'), to: '/wholesale-prices', icon: BadgePercent, permission: 'GET:/admin/products' },
        { label: t('admin.navItems.giftCards'), to: '/gift-cards', icon: Gift, permission: 'GET:/admin/gift-cards' },
        { label: t('admin.navItems.inviteCodes'), to: '/invite-codes', icon: UserPlus, permission: 'GET:/admin/invite-codes' },
      ] }, { id: 'affiliate', label: t('admin.navGroups.affiliateManagement'), icon: BadgePercent, items: [
        { label: t('admin.navItems.affiliatesSettings'), to: '/affiliates/settings', icon: SlidersHorizontal, permission: 'GET:/admin/settings/affiliate' },
        { label: t('admin.navItems.affiliatesUsers'), to: '/affiliates/users', icon: Users, permission: 'GET:/admin/affiliates/users' },
        { label: t('admin.navItems.affiliatesCommissions'), to: '/affiliates/commissions', icon: ReceiptText, permission: 'GET:/admin/affiliates/commissions' },
        { label: t('admin.navItems.affiliatesWithdraws'), to: '/affiliates/withdraws', icon: WalletCards, permission: 'GET:/admin/affiliates/withdraws' },
      ] }, { id: 'reseller', label: t('admin.navGroups.resellerManagement'), icon: Users, items: [
        { label: t('admin.navItems.resellerOperations'), to: '/resellers/operations', icon: LayoutDashboard, permission: 'GET:/admin/resellers/operations/overview' },
        { label: t('admin.navItems.resellerProfiles'), to: '/resellers/profiles', icon: Users, permission: 'GET:/admin/resellers/profiles' },
        { label: t('admin.navItems.resellerDomains'), to: '/resellers/domains', icon: Link, permission: 'GET:/admin/resellers/domains' },
        { label: t('admin.navItems.resellerSiteConfigs'), to: '/resellers/site-configs', icon: Settings, permission: 'GET:/admin/resellers/site-configs' },
        { label: t('admin.navItems.resellerProductSettings'), to: '/resellers/product-settings', icon: SlidersHorizontal, permission: 'GET:/admin/resellers/product-settings' },
        { label: t('admin.navItems.resellerLedgerEntries'), to: '/resellers/ledger-entries', icon: ReceiptText, permission: 'GET:/admin/resellers/ledger-entries' },
        { label: t('admin.navItems.resellerBalanceAccounts'), to: '/resellers/balance-accounts', icon: Wallet, permission: 'GET:/admin/resellers/balance-accounts' },
        { label: t('admin.navItems.resellerWithdraws'), to: '/resellers/withdraws', icon: WalletCards, permission: 'GET:/admin/resellers/withdraws' },
      ] }, { id: 'integration', label: t('admin.navGroups.integrationManagement'), icon: Link, items: [
        { label: t('admin.navItems.siteConnections'), to: '/site-connections', icon: Link, permission: 'GET:/admin/site-connections' },
        { label: t('admin.navItems.productMappings'), to: '/product-mappings', icon: Boxes, permission: 'GET:/admin/product-mappings' },
        { label: t('admin.navItems.procurementOrders'), to: '/procurement-orders', icon: Truck, permission: 'GET:/admin/procurement-orders' },
        { label: t('admin.navItems.reconciliation'), to: '/reconciliation', icon: ClipboardCheck, permission: 'GET:/admin/reconciliation/jobs' },
        { label: t('admin.navItems.apiCredentials'), to: '/api-credentials', icon: KeyRound, permission: 'GET:/admin/api-credentials' },
      ] }, { id: 'telegramBot', label: t('admin.navGroups.telegramBot'), icon: Bot, items: [
        { label: t('admin.navItems.telegramBotOverview'), to: '/telegram-bot', icon: Bot, permission: 'GET:/admin/telegram-bot/instances' },
        { label: t('admin.navItems.telegramBotSettings'), to: '/telegram-bot/settings', icon: SlidersHorizontal, permission: 'GET:/admin/settings/telegram-bot' },
        { label: t('admin.navItems.telegramBotHelpCenter'), to: '/telegram-bot/help-center', icon: ScrollText, permission: 'GET:/admin/settings/telegram-bot' },
        { label: t('admin.navItems.telegramBotMenuSettings'), to: '/telegram-bot/menu', icon: ListOrdered, permission: 'GET:/admin/settings/telegram-bot' },
        { label: t('admin.navItems.telegramBotBroadcasts'), to: '/telegram-bot/broadcasts', icon: Send, permission: 'GET:/admin/telegram-bot/broadcasts' },
        { label: t('telegramBot.groups.title'), to: '/telegram-bot/groups', icon: Users, permission: 'GET:/admin/telegram-bot/groups' },
        { label: t('telegramMembership.products'), to: '/telegram-bot/membership/products', icon: Package, permission: 'GET:/admin/telegram-bot/membership/products' },
        { label: t('telegramMembership.orders'), to: '/telegram-bot/membership/orders', icon: ReceiptText, permission: 'GET:/admin/telegram-bot/membership/orders' },
        // 只保留一个客服入口；仅有任务权限的账号仍可直达原任务页。
        { label: t('telegramSupport.sessionsTitle'), to: authStore.hasPermission('GET:/admin/telegram-bot/support/sessions') ? '/telegram-bot/support/sessions' : '/telegram-bot/support/tasks', icon: MessagesSquare, permission: authStore.hasPermission('GET:/admin/telegram-bot/support/sessions') ? 'GET:/admin/telegram-bot/support/sessions' : 'GET:/admin/telegram-bot/support/downstreams' },
        { label: t('telegramRecovery.title'), to: '/telegram-bot/recovery', icon: ArrowRightLeft, permission: 'GET:/admin/telegram-bot/recovery/tasks' },
      ] }, { id: 'system', label: t('admin.navGroups.systemSettings'), icon: Settings, items: [
        { label: t('admin.navItems.siteSettings'), to: '/settings', icon: SlidersHorizontal, permission: 'GET:/admin/settings' },
        { label: t('admin.navItems.notificationCenter'), to: '/settings/notifications', icon: Bell, permission: 'GET:/admin/settings/notification-center' },
        { label: t('admin.navItems.authz'), to: '/authz', icon: ShieldCheck, permission: 'GET:/admin/authz/roles' },
        { label: t('admin.navItems.authzAudit'), to: '/authz-audit-logs', icon: ScrollText, permission: 'GET:/admin/authz/audit-logs' },
        { label: t('admin.navItems.security'), to: '/security', icon: Lock },
      ] },
    ]
    return groups
      .map((group) => ({
        ...group,
        items: group.items.filter((item) => authStore.hasPermission(item.permission)),
      }))
      .filter((group) => group.items.length > 0)
  })
  return { navGroups }
}
