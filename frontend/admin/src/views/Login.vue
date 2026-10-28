<script setup lang="ts">
import BrandMark from '@/components/BrandMark.vue'
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useAdminAuthStore } from '@/stores/auth'
import { adminAPI, type CaptchaPayload } from '@/api/admin'
import { applySiteIcon } from '@/utils/favicon'
import ImageCaptcha from '@/components/captcha/ImageCaptcha.vue'
import TurnstileCaptcha from '@/components/captcha/TurnstileCaptcha.vue'

const { t } = useI18n()
const router = useRouter()
const authStore = useAdminAuthStore()

type Step = 'password' | 'totp'
const step = ref<Step>('password')

const username = ref('')
const password = ref('')
const totpCode = ref('')
const recoveryCode = ref('')
const useRecovery = ref(false)
const error = ref('')
const loadingCaptcha = ref(false)
const captchaConfig = ref<any>(null)
const captchaPayload = ref<CaptchaPayload>({})
const turnstileToken = ref('')
const imageCaptchaRef = ref<InstanceType<typeof ImageCaptcha> | null>(null)
const turnstileRef = ref<InstanceType<typeof TurnstileCaptcha> | null>(null)

const captchaProvider = computed(() => String(captchaConfig.value?.provider || 'none'))
const loginCaptchaEnabled = computed(() => {
  const loginScene = !!captchaConfig.value?.scenes?.login
  return loginScene && captchaProvider.value !== 'none'
})
const turnstileSiteKey = computed(() => String(captchaConfig.value?.turnstile?.site_key || ''))

const challengeRemaining = ref(0)
let countdownTimer: ReturnType<typeof setInterval> | null = null

const updateCountdown = () => {
  if (!authStore.challengeExpiresAt) {
    challengeRemaining.value = 0
    return
  }
  const ms = new Date(authStore.challengeExpiresAt).getTime() - Date.now()
  challengeRemaining.value = Math.max(0, Math.floor(ms / 1000))
  if (challengeRemaining.value === 0 && step.value === 'totp') {
    backToPassword()
    error.value = t('admin.login.totp.expired')
  }
}

const startCountdown = () => {
  updateCountdown()
  if (countdownTimer) clearInterval(countdownTimer)
  countdownTimer = setInterval(updateCountdown, 1000)
}

const stopCountdown = () => {
  if (countdownTimer) {
    clearInterval(countdownTimer)
    countdownTimer = null
  }
}

const getCaptchaPayload = (): CaptchaPayload | undefined => {
  if (!loginCaptchaEnabled.value) return undefined
  if (captchaProvider.value === 'image') {
    return {
      captcha_id: captchaPayload.value.captcha_id || '',
      captcha_code: captchaPayload.value.captcha_code || '',
    }
  }
  if (captchaProvider.value === 'turnstile') {
    return { turnstile_token: turnstileToken.value }
  }
  return undefined
}

const submitPassword = async () => {
  error.value = ''
  if (loginCaptchaEnabled.value && captchaProvider.value === 'image') {
    if (!captchaPayload.value.captcha_id || !captchaPayload.value.captcha_code) {
      error.value = t('admin.login.captchaRequired')
      return
    }
  }
  if (loginCaptchaEnabled.value && captchaProvider.value === 'turnstile') {
    if (!turnstileToken.value) {
      error.value = t('admin.login.captchaRequired')
      return
    }
  }
  try {
    const res = await authStore.login({
      username: username.value.trim(),
      password: password.value,
      captcha_payload: getCaptchaPayload(),
    })
    if (res.requiresTotp) {
      step.value = 'totp'
      totpCode.value = ''
      recoveryCode.value = ''
      useRecovery.value = false
      startCountdown()
    } else {
      router.push('/')
    }
  } catch (err: any) {
    error.value = err?.message || t('admin.login.errors.invalidCredentials')
    if (captchaProvider.value === 'image') imageCaptchaRef.value?.refresh()
    if (captchaProvider.value === 'turnstile') {
      turnstileRef.value?.reset()
      turnstileToken.value = ''
    }
  }
}

const submit2FA = async () => {
  error.value = ''
  if (useRecovery.value) {
    if (!recoveryCode.value.trim()) {
      error.value = t('admin.login.totp.recoveryRequired')
      return
    }
  } else {
    if (totpCode.value.length !== 6) {
      error.value = t('admin.login.totp.codeFormat')
      return
    }
  }
  try {
    await authStore.verify2FA(
      useRecovery.value
        ? { recovery_code: recoveryCode.value.trim() }
        : { code: totpCode.value },
    )
    stopCountdown()
    router.push('/')
  } catch (err: any) {
    // API client (src/api/client.ts) reject 的是 Error 实例，仅含已翻译的 message。
    // 5 次失败 / 挑战失效后 backend 返回相应翻译消息，用户可通过倒计时到期自动退回或手动点击「返回」按钮回到密码步骤。
    error.value = err?.message || t('admin.login.totp.verifyFailed')
  }
}

const backToPassword = () => {
  authStore.clearChallenge()
  stopCountdown()
  step.value = 'password'
  password.value = ''
  totpCode.value = ''
  recoveryCode.value = ''
}

const loadCaptchaConfig = async () => {
  loadingCaptcha.value = true
  try {
    const res = await adminAPI.getPublicConfig()
    const payload = res.data?.data as any
    applySiteIcon(payload?.brand?.site_icon)
    captchaConfig.value = payload?.captcha || null
  } catch {
    captchaConfig.value = null
  } finally {
    loadingCaptcha.value = false
  }
}

onMounted(() => {
  loadCaptchaConfig()
})

onUnmounted(() => {
  stopCountdown()
})
</script>

<template>
  <main class="admin-login min-h-screen bg-background text-foreground flex items-center justify-center px-6 py-16">
    <section class="w-full max-w-[460px] rounded-[28px] border border-border/65 bg-card px-9 pb-8 pt-10 shadow-sm" aria-labelledby="login-title">
        <header class="mb-8 text-center">
          <BrandMark class="mx-auto mb-6 h-12 w-12" />
          <h1 id="login-title" class="text-[28px] font-semibold leading-tight tracking-tight">
            {{ step === 'password' ? t('admin.login.title') : t('admin.login.totp.title') }}
          </h1>
          <p class="mt-3 text-sm leading-relaxed text-muted-foreground">
            {{ step === 'password' ? t('admin.login.subtitle') : t('admin.login.totp.subtitle') }}
          </p>
        </header>
          <form v-if="step === 'password'" class="space-y-6" :aria-busy="authStore.loading || loadingCaptcha" @submit.prevent="submitPassword">
            <div class="login-credentials rounded-2xl bg-secondary/60 px-4">
            <div class="login-field">
              <Label for="username">{{ t('admin.login.username') }}</Label>
              <Input id="username" v-model="username" autocomplete="username" autocapitalize="none" :spellcheck="false" required :placeholder="t('admin.login.username')" />
            </div>
            <div class="login-field">
              <Label for="password">{{ t('admin.login.password') }}</Label>
              <Input
                id="password"
                v-model="password"
                type="password"
                autocomplete="current-password"
                required
                :placeholder="t('admin.login.password')"
              />
            </div>
            </div>

            <div v-if="loginCaptchaEnabled" class="space-y-2">
              <Label>{{ t('admin.login.captchaLabel') }}</Label>
              <ImageCaptcha
                v-if="captchaProvider === 'image'"
                ref="imageCaptchaRef"
                v-model="captchaPayload"
                :disabled="authStore.loading || loadingCaptcha"
              />
              <TurnstileCaptcha
                v-else-if="captchaProvider === 'turnstile'"
                ref="turnstileRef"
                v-model="turnstileToken"
                :site-key="turnstileSiteKey"
              />
            </div>

            <div v-if="error" role="alert" class="rounded-xl bg-destructive/10 px-3 py-2.5 text-sm text-destructive">{{ error }}</div>
            <Button type="submit" class="h-11 w-full rounded-[14px] font-semibold" :disabled="authStore.loading || loadingCaptcha">
              {{ authStore.loading ? t('admin.login.submitting') : t('admin.login.submit') }}
            </Button>
          </form>

          <form v-else class="space-y-6" :aria-busy="authStore.loading" @submit.prevent="submit2FA">
            <p class="rounded-xl bg-secondary/60 px-3 py-2.5 text-center text-xs text-muted-foreground">
              {{ t('admin.login.totp.remaining', { seconds: challengeRemaining }) }}
            </p>

            <template v-if="!useRecovery">
              <div class="space-y-2">
                <Label for="totp">{{ t('admin.login.totp.codeLabel') }}</Label>
                <Input
                  id="totp"
                  v-model="totpCode"
                  class="h-11 rounded-xl text-center tracking-[0.35em]"
                  inputmode="numeric"
                  maxlength="6"
                  pattern="[0-9]{6}"
                  autocomplete="one-time-code"
                  autofocus
                  placeholder="123456"
                />
              </div>
            </template>
            <template v-else>
              <div class="space-y-2">
                <Label for="rc">{{ t('admin.login.totp.recoveryLabel') }}</Label>
                <Input id="rc" v-model="recoveryCode" class="h-11 rounded-xl" autofocus placeholder="xxxx-xxxxxx" />
              </div>
            </template>

            <button
              type="button"
              class="text-sm text-muted-foreground underline-offset-4 hover:text-foreground hover:underline focus-visible:outline-2 focus-visible:outline-ring"
              @click="useRecovery = !useRecovery"
            >
              {{ useRecovery ? t('admin.login.totp.useCode') : t('admin.login.totp.useRecovery') }}
            </button>

            <div v-if="error" role="alert" class="rounded-xl bg-destructive/10 px-3 py-2.5 text-sm text-destructive">{{ error }}</div>
            <div class="flex gap-2">
              <Button type="button" variant="outline" class="h-11 flex-1 rounded-[14px]" @click="backToPassword">
                {{ t('admin.login.totp.back') }}
              </Button>
              <Button type="submit" class="h-11 flex-1 rounded-[14px]" :disabled="authStore.loading">
                {{ authStore.loading ? t('admin.login.submitting') : t('admin.login.totp.submit') }}
              </Button>
            </div>
          </form>
    </section>
  </main>
</template>

<style scoped>
.login-field {
  display: grid;
  grid-template-columns: minmax(64px, 0.3fr) minmax(0, 1fr);
  align-items: center;
  gap: 12px;
  padding-block: 9px;
}

.login-field + .login-field {
  border-top: 1px solid hsl(var(--border) / 0.65);
}

.login-field :deep(input) {
  min-width: 0;
  height: 44px;
  padding-inline: 4px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  box-shadow: none;
}

.login-field :deep(input:focus-visible) {
  outline: 2px solid hsl(var(--ring));
  outline-offset: 2px;
}

@media (max-width: 480px) {
  .admin-login {
    padding: 32px 16px;
  }

  .admin-login > section {
    padding: 32px 20px 24px;
  }
}
</style>
