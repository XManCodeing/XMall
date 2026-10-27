import { ref, watch } from 'vue'

const TEA_THEME_KEY = 'dujiao_theme'
type Theme = 'light' | 'dark'

const getSystemTheme = (): Theme =>
    window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'

const saved = localStorage.getItem(TEA_THEME_KEY)
let hasPreference = saved === 'light' || saved === 'dark'
const theme = ref<Theme>(hasPreference ? saved as Theme : getSystemTheme())

// Only explicit user choices are persisted. Applying a template default must
// not overwrite an existing preference or create one during app bootstrap.
watch(theme, value => {
    document.documentElement.classList.toggle('dark', value === 'dark')
}, { immediate: true, flush: 'sync' })

export const useTheme = () => {
    const toggleTheme = () => {
        theme.value = theme.value === 'dark' ? 'light' : 'dark'
        hasPreference = true
        localStorage.setItem(TEA_THEME_KEY, theme.value)
    }

    const setTemplateDefault = (fallback?: Theme) => {
        if (!hasPreference) theme.value = fallback || getSystemTheme()
    }

    return { theme, toggleTheme, setTemplateDefault }
}
