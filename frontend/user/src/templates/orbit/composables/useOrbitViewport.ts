import { onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { orbitKeyboardVisible, orbitNavigationMotion } from '../navigation'

export function useOrbitViewport() {
  const router = useRouter()
  const direction = ref<'neutral' | 'back' | 'forward'>('neutral')
  const keyboardOpen = ref(false)
  const viewportHeight = ref(0)
  let frame = 0
  let previousPosition: number | null = null
  let removeAfterEach: (() => void) | undefined
  const position = () => typeof window.history.state?.position === 'number' ? window.history.state.position as number : null
  const updateViewport = () => {
    cancelAnimationFrame(frame)
    frame = requestAnimationFrame(() => {
      const viewport = window.visualViewport
      const mobile = window.matchMedia('(max-width: 767px)').matches
      const active = document.activeElement
      const editing = active instanceof HTMLElement && active.matches('input:not([type="checkbox"]):not([type="radio"]):not([type="button"]):not([type="submit"]), textarea, [contenteditable="true"]')
      // Pinch zoom is not a keyboard. Do not lift actions over the keyboard or obscure inputs.
      keyboardOpen.value = orbitKeyboardVisible(mobile, editing, window.innerHeight, viewport?.height || window.innerHeight, viewport?.scale || 1)
      viewportHeight.value = viewport?.height || window.innerHeight
    })
  }
  onMounted(() => {
    previousPosition = position()
    removeAfterEach = router.afterEach((to, from, failure) => {
      if (failure) return
      const current = position()
      direction.value = orbitNavigationMotion(from.path, to.path, previousPosition, current)
      previousPosition = current
      updateViewport()
    })
    window.visualViewport?.addEventListener('resize', updateViewport)
    window.visualViewport?.addEventListener('scroll', updateViewport)
    window.addEventListener('resize', updateViewport)
    document.addEventListener('focusin', updateViewport)
    document.addEventListener('focusout', updateViewport)
    updateViewport()
  })
  onUnmounted(() => {
    removeAfterEach?.()
    cancelAnimationFrame(frame)
    window.visualViewport?.removeEventListener('resize', updateViewport)
    window.visualViewport?.removeEventListener('scroll', updateViewport)
    window.removeEventListener('resize', updateViewport)
    document.removeEventListener('focusin', updateViewport)
    document.removeEventListener('focusout', updateViewport)
  })
  return { direction, keyboardOpen, viewportHeight }
}
