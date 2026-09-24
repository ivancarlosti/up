<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import HeartbeatBar from '@/components/monitors/HeartbeatBar.vue'
import Badge from '@/components/ui/Badge.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import LocaleSwitcher from '@/components/layout/LocaleSwitcher.vue'
import ThemeToggle from '@/components/layout/ThemeToggle.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { certificateTitle, domainTitle, expiryStateVariant } from '@/lib/expiry'
import { formatDateTime, formatRelative, formatUptime, statusColor } from '@/lib/format'
import type { StatusPage } from '@/lib/types'

const route = useRoute()
const { t, locale } = useI18n()

const page = ref<StatusPage | null>(null)
const error = ref('')
const refreshing = ref(false)

const slug = computed(() => String(route.params.slug ?? ''))

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
        <span class="ml-auto text-[11px] text-muted-foreground">
          {{ t('publicStatus.updatedAt', { time: formatRelative(new Date().toISOString(), locale) }) }}
        </span>
      </div>

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

              <!-- Opt-in per page: only the days-left badge is published, never
                   the internal reason of a failed lookup. -->
              <Badge
                v-if="page.show_expiry && monitor.certificate"
                :variant="expiryStateVariant('ok', monitor.certificate.days_left)"
                :title="certificateTitle(monitor.certificate, locale)"
              >
                {{ t('certificate.daysLeft', { days: monitor.certificate.days_left }) }}
              </Badge>
              <Badge
                v-if="page.show_expiry && monitor.domain && monitor.domain.status === 'ok'"
                :variant="expiryStateVariant(monitor.domain.status, monitor.domain.days_left)"
                :title="domainTitle(monitor.domain, locale)"
              >
                {{ t('domain.daysLeft', { days: monitor.domain.days_left }) }}
              </Badge>

              <span class="ml-auto text-[11px] text-muted-foreground">{{ t(`status.${monitor.status}`) }}</span>
            </div>

            <HeartbeatBar v-if="page.show_charts" class="mt-3" :heartbeats="monitor.heartbeats" :size="40" />

            <div class="mt-2 flex flex-wrap items-center gap-4 text-[11px] text-muted-foreground">
              <span v-if="page.show_uptime">{{ t('publicStatus.uptime') }} 24h: {{ formatUptime(monitor.uptime_24h) }}</span>
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
