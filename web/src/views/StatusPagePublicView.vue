<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import HeartbeatBar from '@/components/monitors/HeartbeatBar.vue'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Input from '@/components/ui/Input.vue'
import Recaptcha from '@/components/ui/Recaptcha.vue'
import LocaleSwitcher from '@/components/layout/LocaleSwitcher.vue'
import ThemeToggle from '@/components/layout/ThemeToggle.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { certificateTitle, domainTitle, expiryStateVariant } from '@/lib/expiry'
import { formatDateTime, formatRelative, formatUptime, formatUptimeWindow, statusColor } from '@/lib/format'
import { useAppStore } from '@/stores/app'
import type { StatusPage } from '@/lib/types'

const route = useRoute()
const { t, locale } = useI18n()
const app = useAppStore()

const page = ref<StatusPage | null>(null)
const error = ref('')
const refreshing = ref(false)

const subscribeEmail = ref('')
const subscribeError = ref('')
const subscribing = ref(false)
const subscribed = ref(false)
const recaptcha = ref<InstanceType<typeof Recaptcha> | null>(null)

// The action the token is minted for (models.RecaptchaActionStatusSubscribe on
// the server): a token minted for another form is rejected.
const recaptchaAction = 'status_page_subscribe'

const slug = computed(() => String(route.params.slug ?? ''))

const subscriptionOutcome = computed(() => String(route.query.subscription ?? ''))
const subscriptionNotice = computed(() => {
  switch (subscriptionOutcome.value) {
    case 'confirmed':
      return { text: t('publicStatus.subscribeConfirmed'), tone: 'success' }
    case 'unsubscribed':
      return { text: t('publicStatus.subscribeUnsubscribed'), tone: 'info' }
    case 'invalid':
      return { text: t('publicStatus.subscribeLinkInvalid'), tone: 'danger' }
    default:
      return null
  }
})

const banner = computed(() => {
  switch (page.value?.overall_status) {
    case 'up':
      return { key: 'publicStatus.overallUp', color: 'bg-status-up' }
    case 'down':
      return { key: 'publicStatus.overallDown', color: 'bg-status-down' }
    case 'degraded':
      return { key: 'publicStatus.overallDegraded', color: 'bg-status-degraded' }
    case 'pending':
      return { key: 'publicStatus.overallPending', color: 'bg-status-pending' }
    default:
      return { key: 'publicStatus.overallUnknown', color: 'bg-status-unknown' }
  }
})

/**
 * sections is what the page renders: one section per group (in page order)
 * followed by the monitors that are on the page but in no group. The API returns
 * the same order in `monitors` (the flat list) and in `groups` (the sections), so
 * a monitor never appears twice.
 */
const sections = computed(() => {
  const groups = (page.value?.groups ?? []).filter((group) => group.monitors?.length)
  const grouped = new Set(groups.flatMap((group) => group.monitors.map((monitor) => monitor.id)))
  const ungrouped = (page.value?.monitors ?? []).filter((monitor) => !grouped.has(monitor.id))
  return [
    ...groups.map((group) => ({ name: group.name, monitors: group.monitors })),
    ...(ungrouped.length ? [{ name: '', monitors: ungrouped }] : []),
  ]
})

async function subscribe(): Promise<void> {
  subscribeError.value = ''
  subscribed.value = false
  subscribing.value = true
  try {
    // v3 mints the token here, right before the request: it is single use and
    // expires after two minutes, so a token obtained earlier would be rejected.
    let token = ''
    if (app.settings?.recaptcha_enabled) {
      try {
        token = (await recaptcha.value?.execute()) ?? ''
      } catch {
        subscribeError.value = t('common.captchaUnavailable')
        return
      }
    }
    await api.subscribeStatusPage(slug.value, {
      email: subscribeEmail.value,
      locale: locale.value,
      recaptcha_token: token,
    })
    subscribed.value = true
    subscribeEmail.value = ''
  } catch (caught) {
    subscribeError.value = translateError(caught)
  } finally {
    subscribing.value = false
  }
}

async function load(): Promise<void> {
  refreshing.value = true
  try {
    page.value = await api.publicStatusPage(slug.value)
    error.value = ''
    if (page.value.custom_css) injectCustomCSS(page.value.custom_css)
  } catch (caught) {
    error.value = translateError(caught)
  } finally {
    refreshing.value = false
  }
}

function injectCustomCSS(css: string): void {
  const id = 'up-status-page-css'
  let element = document.getElementById(id) as HTMLStyleElement | null
  if (!element) {
    element = document.createElement('style')
    element.id = id
    document.head.appendChild(element)
  }
  element.textContent = css
}

let timer: ReturnType<typeof setInterval> | undefined

onMounted(async () => {
  await load()
  // The public page is anonymous, so it polls instead of using the WebSocket
  // session channel.
  timer = setInterval(load, 30000)
})

onUnmounted(() => {
  if (timer) clearInterval(timer)
  document.getElementById('up-status-page-css')?.remove()
})

watch(slug, load)
</script>

<template>
  <div class="mx-auto flex w-full max-w-3xl flex-col gap-5 py-8">
    <header class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <h1 class="text-xl font-semibold">{{ page?.title ?? t('app.name') }}</h1>
        <p v-if="page?.description" class="mt-1 text-xs text-muted-foreground">{{ page.description }}</p>
      </div>
      <div class="flex items-center gap-2">
        <LocaleSwitcher />
        <ThemeToggle />
      </div>
    </header>

    <EmptyState v-if="error" :title="t('publicStatus.notFound')" :description="error" />

    <template v-else-if="page">
      <div class="flex items-center gap-3 rounded-xl border border-border bg-card px-4 py-3">
        <span class="h-3 w-3 rounded-full" :class="banner.color" />
        <p class="text-sm font-medium">{{ t(banner.key) }}</p>
        <span class="ms-auto text-[11px] text-muted-foreground">
          {{ t('publicStatus.updatedAt', { time: formatRelative(new Date().toISOString(), locale) }) }}
        </span>
      </div>

      <div
        v-if="subscriptionNotice"
        class="rounded-xl border px-4 py-3 text-xs"
        :class="subscriptionNotice.tone === 'success'
          ? 'border-status-up/40 bg-status-up/10 text-status-up'
          : subscriptionNotice.tone === 'danger'
            ? 'border-destructive/40 bg-destructive/10 text-destructive'
            : 'border-border bg-muted text-muted-foreground'"
      >
        {{ subscriptionNotice.text }}
      </div>

      <!-- Opt-in incident mails. The form only appears when the page really can
           send (the backend publishes subscription_available), so a visitor is
           never offered a subscription the server would refuse. -->
      <section v-if="page.subscription_available" class="rounded-xl border border-border bg-card px-4 py-4">
        <h2 class="text-sm font-semibold">{{ t('publicStatus.subscribeTitle') }}</h2>
        <p class="mt-1 text-xs text-muted-foreground">
          {{ t('publicStatus.subscribeHelp', { minutes: page.subscription_delay_minutes }) }}
        </p>
        <form class="mt-3 flex flex-col gap-2 sm:flex-row sm:items-end" @submit.prevent="subscribe">
          <Input
            v-model="subscribeEmail"
            type="email"
            autocomplete="email"
            required
            class="sm:flex-1"
            :placeholder="t('publicStatus.subscribeEmailPlaceholder')"
            :aria-label="t('publicStatus.subscribeEmailPlaceholder')"
          />
          <Button type="submit" :loading="subscribing">{{ t('publicStatus.subscribeSubmit') }}</Button>
        </form>
        <Recaptcha
          v-if="app.settings?.recaptcha_enabled"
          ref="recaptcha"
          :sitekey="app.settings.recaptcha_client_id ?? ''"
          :action="recaptchaAction"
        />
        <p v-if="subscribed" class="mt-2 text-xs text-status-up">{{ t('publicStatus.subscribeSent') }}</p>
        <p v-if="subscribeError" class="mt-2 text-xs text-destructive">{{ subscribeError }}</p>
        <p class="mt-2 text-[11px] text-muted-foreground">{{ t('publicStatus.subscribePrivacy') }}</p>
      </section>

      <EmptyState v-if="!page.monitors?.length" :title="t('publicStatus.noMonitors')" />

      <div v-else class="flex flex-col gap-5">
        <section v-for="(section, index) in sections" :key="`${section.name}-${index}`" class="flex flex-col gap-3">
          <h3 v-if="section.name" class="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            {{ section.name }}
          </h3>
          <article
            v-for="monitor in section.monitors"
            :key="monitor.id"
            class="rounded-xl border border-border bg-card px-4 py-3"
          >
            <div class="flex flex-wrap items-center gap-2">
              <span class="h-2.5 w-2.5 rounded-full" :class="statusColor(monitor.status)" />
              <h2 class="text-sm font-medium">{{ monitor.name }}</h2>

              <!-- Opt-in per page and one switch per badge: only the days-left
                   value is published, never the internal reason of a failed
                   lookup. The label in front of the number is what tells a
                   visitor which of the two dates the badge refers to: the TLS
                   certificate of the host or the registration of the domain. -->
              <Badge
                v-if="page.show_cert_expiry && monitor.certificate"
                :variant="expiryStateVariant('ok', monitor.certificate.days_left)"
                :title="certificateTitle(monitor.certificate, locale)"
              >
                {{ t('certificate.column') }}: {{ t('certificate.daysLeft', { days: monitor.certificate.days_left }) }}
              </Badge>
              <Badge
                v-if="page.show_domain_expiry && monitor.domain && monitor.domain.status === 'ok'"
                :variant="expiryStateVariant(monitor.domain.status, monitor.domain.days_left)"
                :title="domainTitle(monitor.domain, locale)"
              >
                {{ t('domain.column') }}: {{ t('domain.daysLeft', { days: monitor.domain.days_left }) }}
              </Badge>

              <span class="ms-auto text-[11px] text-muted-foreground">{{ t(`status.${monitor.status}`) }}</span>
            </div>

            <HeartbeatBar v-if="page.show_charts" class="mt-3" :heartbeats="monitor.heartbeats" :size="40" />

            <div class="mt-2 flex flex-wrap items-center gap-4 text-[11px] text-muted-foreground">
              <span v-if="page.show_uptime">
                {{ t('publicStatus.uptime') }} {{ formatUptimeWindow(monitor.uptime_hours) }}:
                {{ formatUptime(monitor.uptime) }}
              </span>
              <span>{{ t('publicStatus.lastCheck') }}: {{ formatDateTime(monitor.last_check_at, locale) }}</span>
              <span v-if="page.show_tags && monitor.tags">{{ monitor.tags }}</span>
            </div>
          </article>
        </section>
      </div>

      <footer v-if="page.footer_text" class="pt-2 text-center text-[11px] text-muted-foreground">
        {{ page.footer_text }}
      </footer>
    </template>
  </div>
</template>
