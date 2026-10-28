import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { createHead } from '@unhead/vue/client'
import './style.css'
import App from './App.vue'
import router, { warmupCommonRoutes } from './router/index.ts'
import i18n, { detectLocale, setI18nLocale, warmupLocaleMessages } from './i18n/index.ts'
import { useTelegramMiniAppStore } from './stores/telegramMiniApp.ts'
import { initTemplateOverride } from './templates/registry.ts'

// 预览用：?template=vault 持久化激活模板（站长正式切换走站点配置）
initTemplateOverride()

const app = createApp(App)
const head = createHead()
const pinia = createPinia()

app.use(pinia)
app.use(head)
app.use(router)
app.use(i18n)

// 首次导航守卫会加载站点配置；模板确定后再挂载，避免先渲染 classic 外壳。
// 语言与 Telegram 初始化仍并行进行，等待期间保留空挂载点，不展示错误模板。
Promise.all([
  router.isReady(),
  useTelegramMiniAppStore(pinia).init(),
  setI18nLocale(detectLocale()),
]).then(() => {
  app.mount('#app')
})

void router.isReady().then(() => {
  warmupCommonRoutes()
  warmupLocaleMessages()
})
