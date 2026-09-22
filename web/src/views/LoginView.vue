<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { KeyRound, LogIn } from 'lucide-vue-next'
import Alert from '@/components/ui/Alert.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { useAppStore } from '@/stores/app'

const app = useAppStore()
const router = useRouter()
const route = useRoute()
const { t } = useI18n()

const email = ref('')
const password = ref('')
const captchaToken = ref('')
const error = ref('')
const loading = ref(false)

const appName = ref('Up')

onMounted(async () => {
  if (!app.settings) await app.bootstrap()
  appName.value = app.appName
  // When the dashboard is open, there is nothing to sign in.
  if (app.authMethod === 'none') await router.replace({ name: 'dashboard' })
})

async function submit(): Promise<void> {
  error.value = ''
  loading.value = true
  try {
    await api.login(email.value, password.value, captchaToken.value)
    await app.refreshSession()
    const redirect = (route.query.redirect as string) || '/'
    await router.push(redirect)
  } catch (caught) {
    error.value = translateError(caught)
  } finally {
    loading.value = false
  }
}

function signInWithKeycloak(): void {
  window.location.href = api.oidcLoginURL((route.query.redirect as string) || '/')
}
</script>

<template>
  <div class="mx-auto flex w-full max-w-md flex-col gap-4 py-10">
    <div class="flex flex-col items-center gap-2 text-center">
      <img src="/logo.svg" :alt="appName" class="h-12 w-12" />
      <h1 class="text-lg font-semibold">{{ t('login.title') }}</h1>
      <p class="text-xs text-muted-foreground">{{ t('login.subtitle', { app: appName }) }}</p>
    </div>

    <Card>
      <Alert v-if="error" variant="danger" class="mb-3">{{ error }}</Alert>
      <Alert v-if="app.authMethod === 'none'" variant="info" class="mb-3">{{ t('login.noneHint') }}</Alert>

      <form v-if="app.session?.login_enabled" class="grid gap-3" @submit.prevent="submit">
        <div class="grid gap-1">
          <Label for="login-email" required>{{ t('login.email') }}</Label>
          <Input id="login-email" v-model="email" type="email" autocomplete="username" required />
        </div>
        <div class="grid gap-1">
          <Label for="login-password" required>{{ t('login.password') }}</Label>
          <Input id="login-password" v-model="password" type="password" autocomplete="current-password" required />
        </div>

        <div v-if="app.session?.recaptcha_enabled" class="grid gap-1">
          <Label>{{ t('login.captchaHint') }}</Label>
          <div
            class="g-recaptcha"
            :data-sitekey="app.session?.recaptcha_client_id"
            data-callback="__upCaptcha"
          />
          <Input v-model="captchaToken" :placeholder="t('login.captchaHint')" />
        </div>

        <Button type="submit" :loading="loading">
          <LogIn class="h-4 w-4" aria-hidden="true" />
          {{ loading ? t('login.signingIn') : t('login.submit') }}
        </Button>
      </form>

      <Button v-if="app.session?.oidc_enabled" variant="outline" class="mt-3 w-full" @click="signInWithKeycloak">
        <KeyRound class="h-4 w-4" aria-hidden="true" />
        {{ t('login.withKeycloak') }}
      </Button>

      <Button v-if="app.authMethod === 'none'" class="mt-3 w-full" @click="router.push({ name: 'dashboard' })">
        {{ t('login.goToDashboard') }}
      </Button>
    </Card>
  </div>
</template>
