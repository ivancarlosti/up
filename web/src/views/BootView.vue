<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Loader2, RefreshCw } from 'lucide-vue-next'
import Alert from '@/components/ui/Alert.vue'
import Button from '@/components/ui/Button.vue'
import { useAppStore } from '@/stores/app'

/**
 * BootView is the screen shown while the backend prepares its database, and for
 * as long as it cannot reach it.
 *
 * It replaces the blank page a start used to show (the API port stayed closed
 * until every migration and backfill had run, and the SPA waited for /api/settings
 * forever). Everything on it comes from GET /api/boot, which the store polls:
 * the phase, the elapsed time, the classified failure and the hint that goes
 * with it.
 */
const app = useAppStore()
const { t } = useI18n()

/** failed is true as soon as the backend reported a classified failure. */
const failed = computed(() => Boolean(app.boot?.code))

/**
 * phaseMessage is the sentence of the step the backend is in. Every branch is a
 * literal key on purpose: the translation check (npm run check:i18n) can only
 * verify keys it can see.
 */
const phaseMessage = computed(() => {
  switch (app.boot?.phase) {
    case 'connecting':
      return t('boot.phase.connecting')
    case 'migrating':
      return t('boot.phase.migrating')
    case 'backfilling':
      return t('boot.phase.backfilling')
    case 'template_auth':
      return t('boot.phase.templateAuth')
    case 'rollups':
      return t('boot.phase.rollups')
    case 'seeding':
      return t('boot.phase.seeding')
    case 'wiring':
      return t('boot.phase.wiring')
    case 'scheduler':
      return t('boot.phase.scheduler')
    case 'ready':
      return t('boot.phase.ready')
    default:
      return t('boot.phase.starting')
  }
})

/** failureTitle names what is wrong, in the words of the visitor. */
const failureTitle = computed(() => {
  switch (app.boot?.code) {
    case 'ERR_DB_UNREACHABLE':
      return t('boot.failure.unreachable')
    case 'ERR_DB_CREDENTIALS':
      return t('boot.failure.credentials')
    case 'ERR_DB_MISSING':
      return t('boot.failure.missing')
    case 'ERR_DB_MIGRATION_FAILED':
      return t('boot.failure.migration')
    default:
      return t('boot.failure.generic')
  }
})

/** failureHint says what the operator has to check. */
const failureHint = computed(() => {
  switch (app.boot?.code) {
    case 'ERR_DB_UNREACHABLE':
      return t('boot.hint.unreachable')
    case 'ERR_DB_CREDENTIALS':
      return t('boot.hint.credentials')
    case 'ERR_DB_MISSING':
      return t('boot.hint.missing')
    case 'ERR_DB_MIGRATION_FAILED':
      return t('boot.hint.migration')
    default:
      return t('boot.hint.generic')
  }
})

/**
 * slowNote explains the one boot step that can take a while (the first start
 * after an upgrade rebuilds 30 days of hourly statistics) so a visitor does not
 * read it as a hang.
 */
const slowNote = computed(() => (app.boot?.phase === 'rollups' ? t('boot.maintenanceRollups') : ''))

/** progress is the percentage of the fixed order of the boot steps. */
const progress = computed(() => {
  const order = ['starting', 'connecting', 'migrating', 'backfilling', 'template_auth', 'rollups', 'seeding', 'wiring', 'scheduler']
  const index = Math.max(order.indexOf(app.boot?.phase ?? 'starting'), 0)
  return Math.round(((index + 1) / order.length) * 100)
})

const elapsedSeconds = computed(() => Math.round((app.boot?.elapsed_ms ?? 0) / 1000))
const attempts = computed(() => app.boot?.attempts ?? 0)
const retrySeconds = computed(() => Math.ceil((app.boot?.next_retry_ms ?? 0) / 1000))
</script>

<template>
  <div
    class="mx-auto flex min-h-screen w-full max-w-lg flex-col items-center justify-center gap-6 px-5 py-10 text-center"
  >
    <img src="/logo.svg" :alt="app.appName" class="h-12 w-12" />

    <div class="flex flex-col gap-1">
      <h1 class="text-lg font-semibold">{{ t('boot.title') }}</h1>
      <p class="text-xs text-muted-foreground">{{ t('boot.subtitle') }}</p>
    </div>

    <!-- A classified failure: what happened, what to check, and the raw driver
         message for whoever reads the logs. -->
    <div v-if="failed" class="w-full text-start">
      <Alert variant="danger">
        <div class="flex flex-col gap-2">
          <p class="text-sm font-medium">{{ failureTitle }}</p>
          <p>{{ failureHint }}</p>
          <p v-if="app.boot?.detail" class="font-mono text-[11px] break-words opacity-80">
            {{ app.boot.detail }}
          </p>
        </div>
      </Alert>
    </div>

    <!-- The boot is progressing: the step, how far along it is and how long it
         has been running. -->
    <div v-else class="flex w-full flex-col items-center gap-3">
      <div class="flex items-center gap-2 text-sm font-medium">
        <Loader2 class="h-4 w-4 animate-spin text-primary" aria-hidden="true" />
        <span>{{ phaseMessage }}</span>
      </div>
      <div class="h-1 w-full overflow-hidden rounded-full bg-accent">
        <div
          class="h-full rounded-full bg-primary transition-all duration-500"
          :style="{ width: `${progress}%` }"
        />
      </div>
      <p class="text-xs text-muted-foreground">{{ t('boot.elapsed', { seconds: elapsedSeconds }) }}</p>
    </div>

    <p v-if="slowNote" class="text-xs text-muted-foreground">{{ slowNote }}</p>

    <div class="flex flex-col items-center gap-2">
      <p v-if="attempts > 0" class="text-xs text-muted-foreground">
        {{ t('boot.attempts', { count: attempts, seconds: retrySeconds }) }}
      </p>
      <Button variant="outline" size="sm" @click="app.retryBoot()">
        <RefreshCw class="h-4 w-4" aria-hidden="true" />
        {{ t('boot.retry') }}
      </Button>
    </div>
  </div>
</template>
