<template>
  <div id="app" class="min-h-screen bg-background text-foreground flex flex-col">
    <!-- 独立店面模板外壳；分销控制台保持原有布局 -->
    <component :is="storefrontLayout" v-if="storefrontLayout && !isResellerConsole">
      <ErrorBoundary>
        <RouterView v-slot="{ Component }">
          <Transition name="page-fade" mode="out-in">
            <component :is="Component" />
          </Transition>
        </RouterView>
      </ErrorBoundary>
    </component>

    <!-- classic 模板 / 分销控制台（保持原有结构不变） -->
    <template v-else>
      <Navbar v-if="!isResellerConsole" />
      <main class="flex-1" :class="isResellerConsole ? '' : 'pb-14 lg:pb-0'">
        <ErrorBoundary>
          <RouterView v-slot="{ Component }">
            <Transition name="page-fade" mode="out-in">
              <component :is="Component" />
            </Transition>
          </RouterView>
        </ErrorBoundary>
      </main>
      <Footer v-if="!isResellerConsole" />
      <BackToTop v-if="!isResellerConsole" />
      <MobileBottomNav v-if="!isResellerConsole" />
    </template>

    <Loading :loading="appStore.loading" />
    <Toast />
    <ConfirmDialog />
  </div>
</template>

<script setup lang="ts">
import { computed, defineAsyncComponent, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useAppStore } from './stores/app'
import { getActiveTemplate } from './templates/registry'
import { useTheme } from './utils/theme'
import Navbar from './components/Navbar.vue'
import Footer from './components/Footer.vue'
import Loading from './components/Loading.vue'
import Toast from './components/Toast.vue'
import ConfirmDialog from './components/ConfirmDialog.vue'
import ErrorBoundary from './components/ErrorBoundary.vue'
import BackToTop from './components/BackToTop.vue'
import MobileBottomNav from './components/MobileBottomNav.vue'

// 模板外壳按需加载，不将模板样式带入 classic / 控制台。
const VaultLayout = defineAsyncComponent(() => import('./templates/vault/layout/VaultLayout.vue'))
const OrbitLayout = defineAsyncComponent(() => import('./templates/orbit/layout/OrbitLayout.vue'))

// config 由 router.beforeEach 统一加载，无需在此重复调用
const appStore = useAppStore()
const route = useRoute()
const { setTemplateDefault } = useTheme()
const isResellerConsole = computed(() => route.meta.resellerConsole === true)
const activeTemplate = computed(() => getActiveTemplate())
const storefrontLayout = computed(() => activeTemplate.value === 'orbit' ? OrbitLayout : activeTemplate.value === 'vault' ? VaultLayout : null)
watch([activeTemplate, isResellerConsole], ([template, console]) => {
  // 标记在根节点，确保 Teleport 弹层也继承统一主题令牌。
  const orbit = template === 'orbit' && !console
  document.documentElement.classList.toggle('orbit-theme', orbit)
  setTemplateDefault(orbit ? 'light' : undefined)
}, { immediate: true })
</script>

<style>
.page-fade-enter-active,
.page-fade-leave-active {
  transition: opacity 200ms ease;
}

.page-fade-enter-from,
.page-fade-leave-to {
  opacity: 0;
}
</style>
