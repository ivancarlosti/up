<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ExternalLink, Pencil, Pause, Play, RefreshCw, Trash2 } from 'lucide-vue-next'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import HeartbeatBar from '@/components/monitors/HeartbeatBar.vue'
import StatusBadge from '@/components/monitors/StatusBadge.vue'
import { formatInterval, formatLatency, formatRelative, formatUptime } from '@/lib/format'
import type { Monitor } from '@/lib/types'

const props = defineProps<{ monitor: Monitor }>()
const emit = defineEmits<{
  open: [monitor: Monitor]
  edit: [monitor: Monitor]
  toggle: [monitor: Monitor]
  check: [monitor: Monitor]
  remove: [monitor: Monitor]
}>()

const { t, locale } = useI18n()

const tags = computed(() =>
  (props.monitor.tags ?? '')
    .split(',')
    .map((tag) => tag.trim())
    .filter(Boolean),
)

const target = computed(() => {
  const config = props.monitor.config
  switch (props.monitor.type) {
    case 'tcp':
      return `${config.host}:${config.port}`
    case 'dns':
      return `${config.record_type} ${config.hostname}`
    default:
      return config.url ?? ''
  }
})

/**
 * targetHref makes the target clickable for the types that are an URL. It lives
 * outside the button that opens the detail view: an anchor inside a button is
 * invalid HTML and the click would be ambiguous.
 */
const targetHref = computed(() => {
  if (props.monitor.type !== 'http' && props.monitor.type !== 'keyword') return ''
  const url = props.monitor.config?.url ?? ''
  return /^https?:\/\//i.test(url) ? url : ''
})
</script>

<template>
  <article
    class="flex flex-col gap-3 rounded-xl border border-border bg-card p-4 shadow-sm transition-colors hover:border-primary/40"
  >
    <div class="flex items-start justify-between gap-3">
      <div class="min-w-0">
        <button class="block max-w-full text-left" @click="emit('open', monitor)">
          <h3 class="truncate text-sm font-semibold">{{ monitor.name }}</h3>
        </button>
        <a
          v-if="targetHref"
          class="mt-0.5 block truncate text-xs text-muted-foreground hover:text-foreground hover:underline"
          :href="targetHref"
          target="_blank"
          rel="noopener noreferrer"
        >
          {{ target }}
        </a>
        <p v-else class="mt-0.5 truncate text-xs text-muted-foreground">{{ target }}</p>
      </div>
      <StatusBadge :status="monitor.status" pulse />
    </div>

    <!-- The dashboard payload does not carry the heartbeat series yet, so the bar
         is only drawn when there is something to show (an empty track reads as a
         broken widget). -->
    <HeartbeatBar v-if="monitor.heartbeats?.length" :heartbeats="monitor.heartbeats" :size="30" />

    <dl class="grid grid-cols-3 gap-2 text-[11px] text-muted-foreground">
      <div>
        <dt>{{ t('common.uptime') }} 24h</dt>
        <dd class="text-sm font-medium text-foreground">{{ formatUptime(monitor.uptime_24h) }}</dd>
      </div>
      <div>
        <dt>{{ t('common.latency') }}</dt>
        <dd class="text-sm font-medium text-foreground">{{ formatLatency(monitor.last_latency_ms) }}</dd>
      </div>
      <div>
        <dt>{{ t('common.interval') }}</dt>
        <dd class="text-sm font-medium text-foreground">{{ formatInterval(monitor.interval_seconds) }}</dd>
      </div>
    </dl>

    <div class="flex items-center justify-between gap-2">
      <div class="flex min-w-0 flex-wrap items-center gap-1">
        <Badge variant="secondary">{{ monitor.type }}</Badge>
        <Badge v-if="!monitor.active" variant="warning">{{ t('common.paused') }}</Badge>
        <Badge v-for="tag in tags" :key="tag" variant="outline">{{ tag }}</Badge>
      </div>
      <span class="shrink-0 text-[10px] text-muted-foreground">{{ formatRelative(monitor.last_check_at, locale) }}</span>
    </div>

    <div class="flex flex-wrap items-center gap-1 border-t border-border pt-2">
      <Button variant="ghost" size="sm" @click="emit('open', monitor)">
        <ExternalLink class="h-3.5 w-3.5" aria-hidden="true" />
        {{ t('monitor.detail') }}
      </Button>
      <Button variant="ghost" size="sm" @click="emit('check', monitor)">
        <RefreshCw class="h-3.5 w-3.5" aria-hidden="true" />
        {{ t('dashboard.checkNow') }}
      </Button>
      <Button variant="ghost" size="sm" @click="emit('toggle', monitor)">
        <Play v-if="!monitor.active" class="h-3.5 w-3.5" aria-hidden="true" />
        <Pause v-else class="h-3.5 w-3.5" aria-hidden="true" />
        {{ monitor.active ? t('monitor.pauseTitle') : t('common.active') }}
      </Button>
      <Button variant="ghost" size="sm" @click="emit('edit', monitor)">
        <Pencil class="h-3.5 w-3.5" aria-hidden="true" />
        {{ t('common.edit') }}
      </Button>
      <Button variant="ghost" size="sm" class="text-status-down" @click="emit('remove', monitor)">
        <Trash2 class="h-3.5 w-3.5" aria-hidden="true" />
        {{ t('common.delete') }}
      </Button>
    </div>
  </article>
</template>
