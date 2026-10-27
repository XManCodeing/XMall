<template>
  <footer
    class="relative bg-card/90 text-muted-foreground border-t overflow-hidden">
    <div class="container mx-auto px-4 py-16 relative">
      <div class="grid grid-cols-2 md:grid-cols-4 gap-8 md:gap-12 mb-16">
        <!-- Brand -->
        <div class="col-span-2 space-y-6">
          <div class="flex items-center space-x-3">
            <img
              v-if="brandLogo"
              :src="brandLogo"
              :alt="brandSiteName"
              class="h-9 max-w-[180px] object-contain"
            />
            <div
              v-else
              class="w-8 h-8 bg-primary rounded-lg flex items-center justify-center">
              <span class="text-primary-foreground font-black text-sm">{{ brandInitial }}</span>
            </div>
            <h3 class="text-foreground text-xl font-bold tracking-tight">{{ brandSiteName }}</h3>
          </div>
          <p class="text-sm leading-relaxed max-w-sm text-muted-foreground">
            {{ brandDescription || t('footer.description') }}
          </p>
        </div>

        <!-- Links -->
        <div>
          <h4 class="text-foreground font-bold mb-6 tracking-wide">{{ t('footer.quickLinks') }}</h4>
          <ul class="space-y-3 text-sm">
            <li v-for="item in quickLinks" :key="item.path">
              <router-link :to="item.path" class="text-muted-foreground hover:text-foreground transition-colors flex items-center gap-2 group">
                <component :is="item.icon" class="w-4 h-4 shrink-0 opacity-50 group-hover:opacity-80 transition-opacity" />
                {{ t(item.label) }}
              </router-link>
            </li>
          </ul>
        </div>

        <!-- Contact -->
        <div>
          <h4 class="text-foreground font-bold mb-6 tracking-wide">{{ t('footer.contact') }}</h4>
          <div class="space-y-4">
            <a v-if="config?.contact?.telegram" :href="config.contact.telegram" target="_blank"
              rel="noopener noreferrer"
              class="flex items-center space-x-3 text-sm hover:text-gray-900 dark:hover:text-white transition-colors p-3 rounded-lg bg-gray-50 dark:bg-white/5 hover:bg-gray-100 dark:hover:bg-white/10 border border-gray-100 dark:border-white/5 hover:border-gray-200 dark:hover:border-white/10">
              <svg class="w-5 h-5 text-blue-400" fill="currentColor" viewBox="0 0 24 24">
                <path
                  d="M12 0C5.373 0 0 5.373 0 12s5.373 12 12 12 12-5.373 12-12S18.627 0 12 0zm5.894 8.221l-1.97 9.28c-.145.658-.537.818-1.084.508l-3-2.21-1.446 1.394c-.14.18-.357.295-.6.295-.002 0-.003 0-.005 0l.213-3.054 5.56-5.022c.24-.213-.054-.334-.373-.121l-6.869 4.326-2.96-.924c-.64-.203-.658-.64.135-.954l11.566-4.458c.538-.196 1.006.128.832.941z" />
              </svg>
              <span>Telegram</span>
            </a>
            <a v-if="config?.contact?.whatsapp" :href="config.contact.whatsapp" target="_blank"
              rel="noopener noreferrer"
              class="flex items-center space-x-3 text-sm hover:text-gray-900 dark:hover:text-white transition-colors p-3 rounded-lg bg-gray-50 dark:bg-white/5 hover:bg-gray-100 dark:hover:bg-white/10 border border-gray-100 dark:border-white/5 hover:border-gray-200 dark:hover:border-white/10">
              <svg class="w-5 h-5 text-green-400" fill="currentColor" viewBox="0 0 24 24">
                <path
                  d="M17.472 14.382c-.297-.149-1.758-.867-2.03-.967-.273-.099-.471-.148-.67.15-.197.297-.767.966-.94 1.164-.173.199-.347.223-.644.075-.297-.15-1.255-.463-2.39-1.475-.883-.788-1.48-1.761-1.653-2.059-.173-.297-.018-.458.13-.606.134-.133.298-.347.446-.52.149-.174.198-.298.298-.497.099-.198.05-.371-.025-.52-.075-.149-.669-1.612-.916-2.207-.242-.579-.487-.5-.669-.51-.173-.008-.371-.01-.57-.01-.198 0-.52.074-.792.372-.272.297-1.04 1.016-1.04 2.479 0 1.462 1.065 2.875 1.213 3.074.149.198 2.096 3.2 5.077 4.487.709.306 1.262.489 1.694.625.712.227 1.36.195 1.871.118.571-.085 1.758-.719 2.006-1.413.248-.694.248-1.289.173-1.413-.074-.124-.272-.198-.57-.347m-5.421 7.403h-.004a9.87 9.87 0 01-5.031-1.378l-.361-.214-3.741.982.998-3.648-.235-.374a9.86 9.86 0 01-1.51-5.26c.001-5.45 4.436-9.884 9.888-9.884 2.64 0 5.122 1.03 6.988 2.898a9.825 9.825 0 012.893 6.994c-.003 5.45-4.437 9.884-9.885 9.884m8.413-18.297A11.815 11.815 0 0012.05 0C5.495 0 .16 5.335.157 11.892c0 2.096.547 4.142 1.588 5.945L.057 24l6.305-1.654a11.882 11.882 0 005.683 1.448h.005c6.554 0 11.89-5.335 11.893-11.893a11.821 11.821 0 00-3.48-8.413Z" />
              </svg>
              <span>WhatsApp</span>
            </a>
            <a v-if="config?.contact?.email" :href="`mailto:${config.contact.email}`"
              class="flex items-center space-x-3 text-sm hover:text-gray-900 dark:hover:text-white transition-colors p-3 rounded-lg bg-gray-50 dark:bg-white/5 hover:bg-gray-100 dark:hover:bg-white/10 border border-gray-100 dark:border-white/5 hover:border-gray-200 dark:hover:border-white/10">
              <Mail class="w-5 h-5 text-amber-400" />
              <span>{{ t('footer.email') }}</span>
            </a>
            <a v-if="config?.contact?.support_url" :href="config.contact.support_url"
              :target="config.contact.support_url.startsWith('https://') ? '_blank' : undefined" rel="noopener noreferrer"
              class="flex items-center space-x-3 text-sm hover:text-gray-900 dark:hover:text-white transition-colors p-3 rounded-lg bg-gray-50 dark:bg-white/5 hover:bg-gray-100 dark:hover:bg-white/10 border border-gray-100 dark:border-white/5 hover:border-gray-200 dark:hover:border-white/10">
              <Headphones class="w-5 h-5 text-violet-400" />
              <span>{{ t('footer.supportUrl') }}</span>
            </a>
          </div>
        </div>
      </div>

      <!-- Copyright -->
      <div
        class="border-t pt-8 flex flex-col md:flex-row items-center justify-between gap-4 text-xs text-muted-foreground">
        <p class="text-center md:text-left">&copy; {{ currentYear }} {{ brandSiteName }}. {{ t('footer.rights') }}</p>
        <div class="flex flex-col items-center gap-2 md:items-end">
          <div class="flex flex-wrap items-center gap-x-4 gap-y-1 justify-center md:justify-end">
            <router-link to="/privacy" class="hover:text-gray-900 dark:hover:text-gray-400">{{ t('footer.privacy') || 'Privacy Policy' }}</router-link>
            <router-link to="/terms" class="hover:text-gray-900 dark:hover:text-gray-400">{{ t('footer.terms') || 'Terms of Service' }}</router-link>
          </div>
          <div v-if="footerLinks.length" class="flex flex-wrap items-center gap-x-4 gap-y-1 justify-center md:justify-end">
            <a
              v-for="link in footerLinks"
              :key="link.name"
              :href="link.url || 'javascript:void(0)'"
              :target="link.url ? '_blank' : undefined"
              rel="noopener noreferrer"
              class="hover:text-gray-900 dark:hover:text-gray-400"
            >{{ link.name }}</a>
          </div>
        </div>
      </div>
    </div>
  </footer>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Home, LayoutGrid, Newspaper, Info, Mail, Headphones } from 'lucide-vue-next'
import { useAppStore } from '../stores/app'
import { getImageUrl } from '../utils/image'
import { getLocalizedText } from '../utils/resellerSiteConfig'

const { t } = useI18n()
const appStore = useAppStore()

const config = computed(() => appStore.config)

const brandSiteName = computed(() => appStore.brandSiteName)

const brandDescription = computed(() => {
  const desc = config.value?.brand?.site_description
  if (desc && typeof desc === 'object') {
    const val = desc[appStore.locale] || desc['zh-CN'] || ''
    return typeof val === 'string' ? val.trim() : ''
  }
  return ''
})

const brandInitial = computed(() => {
  return brandSiteName.value.charAt(0).toUpperCase()
})

const brandLogo = computed(() => {
  const raw = String(config.value?.brand?.site_logo || '').trim()
  return raw ? getImageUrl(raw) : ''
})

const isListMode = computed(() => config.value?.template_mode === 'list')
const navConfig = computed(() => config.value?.nav_config as { builtin?: Record<string, boolean> } | undefined)

const quickLinks = computed(() => {
  const items = [
    { path: '/', label: 'nav.home', icon: Home },
  ]
  if (!isListMode.value) {
    items.push({ path: '/products', label: 'nav.products', icon: LayoutGrid })
  }
  const builtin = navConfig.value?.builtin
  if (!builtin || builtin.blog !== false) {
    items.push({ path: '/blog', label: 'nav.blog', icon: Newspaper })
  }
  if (!builtin || builtin.about !== false) {
    items.push({ path: '/about', label: 'nav.about', icon: Info })
  }
  return items
})

const footerLinks = computed(() => {
  const links = config.value?.footer_links
  if (!Array.isArray(links)) return []
  return links
    .map((item: any) => ({
      name: typeof item?.name === 'string' ? item.name.trim() : getLocalizedText(item?.name, appStore.locale),
      url: String(item?.url || '').trim(),
    }))
    .filter((item) => item.name)
})

const currentYear = new Date().getFullYear()
</script>
