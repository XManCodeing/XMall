import { ref, watch } from 'vue'

const THEME_KEY = 'xmall_theme'
const LEGACY_THEME_KEY = 'dujiao_theme'
type Theme = 'light' | 'dark'

const getSystemTheme = (): Theme =>
    window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'

const readSavedTheme = (): Theme | null => {
    const current = localStorage.getItem(THEME_KEY)
    if (current === 'light' || current === 'dark') return current
    const legacy = localStorage.getItem(LEGACY_THEME_KEY)
    if (legacy === 'light' || legacy === 'dark') {
        localStorage.setItem(THEME_KEY, legacy)
        localStorage.removeItem(LEGACY_THEME_KEY)
        return legacy
    }
    return null
}

const saved = readSavedTheme()
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
        localStorage.setItem(THEME_KEY, theme.value)
    }

    const setTemplateDefault = (fallback?: Theme) => {
        if (!hasPreference) theme.value = fallback || getSystemTheme()
    }

    return { theme, toggleTheme, setTemplateDefault }
}
