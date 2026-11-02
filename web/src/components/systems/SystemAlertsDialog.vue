<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { BellRing } from 'lucide-vue-next'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Checkbox from '@/components/ui/Checkbox.vue'
import Dialog from '@/components/ui/Dialog.vue'
import Input from '@/components/ui/Input.vue'
import Switch from '@/components/ui/Switch.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import type { System, SystemAlertMetric, SystemAlertsPayload } from '@/lib/types-systems'
import { useSystemStore } from '@/stores/systems'
import { useToastStore } from '@/stores/toast'
import type { Notification } from '@/lib/types-platform'

/**
 * SystemAlertsDialog edits the alert configuration of one machine: the rule of
 * every metric (on/off, threshold, sustained minutes) and the channels it
 * notifies, with one toggle per event.
 *
 * The four toggles are the reason the dialog is wider than a form: a machine has
 * four edges (down, up, threshold, recovered) and an operator who wants the disk
 * warning but not the recovery mail must be able to say so. A channel with no
 * event selected is not linked at all.
 */
const props = defineProps<{
  modelValue: boolean
  system: System | null
}>()

const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()

const { t } = useI18n()
const store = useSystemStore()
const toasts = useToastStore()

/** One rule as the form holds it (the numbers are strings until the save). */
interface RuleForm {
  metric: SystemAlertMetric
  enabled: boolean
  threshold: string
  minutes: string
}

/** One channel of the instance, with its link state. */
interface ChannelForm {
  id: number
  name: string
  type: string
  linked: boolean
  on_down: boolean
  on_up: boolean
  on_threshold: boolean
  on_recovered: boolean
}

const rules = ref<RuleForm[]>([])
const channels = ref<ChannelForm[]>([])
const loading = ref(false)
const error = ref<string | null>(null)

/** metricLabel is the human name of a rule. */
function metricLabel(metric: SystemAlertMetric): string {
  return t(`systems.metric.${metric}`)
}

/** numeric reports whether a rule has a percentage (the down rule has none). */
function numeric(metric: SystemAlertMetric): boolean {
  return metric !== 'down'
}

/** load fetches the alert configuration and the channels of the instance. */
async function load(): Promise<void> {
  const system = props.system
  if (!system) return
  loading.value = true
  error.value = null
  try {
    const [alerts, notifications] = await Promise.all([store.alerts(system.id), api.notifications()])
    rules.value = alerts.rules.map((rule) => ({
      metric: rule.metric,
      enabled: rule.enabled,
      threshold: String(Math.round(rule.threshold)),
      minutes: String(rule.min_minutes),
    }))
    const links = new Map(alerts.channels.map((link) => [link.notification_id, link]))
    channels.value = notifications.map((channel: Notification) => {
      const link = links.get(channel.id)
      return {
        id: channel.id,
        name: channel.name,
        type: channel.type,
        linked: Boolean(link),
        on_down: link?.on_down ?? true,
        on_up: link?.on_up ?? true,
        on_threshold: link?.on_threshold ?? true,
        on_recovered: link?.on_recovered ?? true,
      }
    })
  } catch (cause) {
    error.value = translateError(cause)
  } finally {
    loading.value = false
  }
}

/** toggleLink links or unlinks one channel (a newly linked one asks for all four). */
function toggleLink(row: ChannelForm, linked: boolean): void {
  row.linked = linked
  if (!linked) return
  if (!row.on_down && !row.on_up && !row.on_threshold && !row.on_recovered) {
    row.on_down = true
    row.on_up = true
    row.on_threshold = true
    row.on_recovered = true
  }
}

/** anyEvent tells whether a linked channel would receive anything at all. */
function anyEvent(row: ChannelForm): boolean {
  return row.on_down || row.on_up || row.on_threshold || row.on_recovered
}

/** save stores every rule and the links that have at least one event. */
async function save(): Promise<void> {
  const system = props.system
  if (!system) return
  const payload: SystemAlertsPayload = {
    rules: rules.value.map((rule) => ({
      metric: rule.metric,
      enabled: rule.enabled,
      threshold: Number(rule.threshold) || 0,
      min_minutes: Number(rule.minutes) || 0,
    })),
    channels: channels.value
      .filter((row) => row.linked && anyEvent(row))
      .map((row) => ({
        notification_id: row.id,
        on_down: row.on_down,
        on_up: row.on_up,
        on_threshold: row.on_threshold,
        on_recovered: row.on_recovered,
      })),
  }
  try {
    await store.saveAlerts(system.id, payload)
    toasts.success(t('systems.alertsSaved'))
    emit('update:modelValue', false)
  } catch (cause) {
    toasts.error(t('systems.alertsSaveFailed'), translateError(cause))
  }
}

/** close restores nothing: the dialog reloads its state on the next open. */
function close(): void {
  emit('update:modelValue', false)
}

watch(
  () => props.modelValue,
  (open) => {
    if (!open) return
    rules.value = []
    channels.value = []
    void load()
  },
)

const linkedCount = computed(() => channels.value.filter((row) => row.linked && anyEvent(row)).length)
</script>

<template>
  <Dialog
    :model-value="modelValue"
    :title="t('systems.alertsTitle', { name: system?.name ?? '' })"
    :description="t('systems.alertsDescription')"
    wide
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div class="space-y-5">
      <p v-if="error" class="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive">
        {{ error }}
      </p>

      <!-- Rules --------------------------------------------------------- -->
      <section class="space-y-2">
        <h3 class="text-sm font-semibold">{{ t('systems.alertsRules') }}</h3>
        <p class="text-xs text-muted-foreground">{{ t('systems.alertsRulesHelp') }}</p>
        <div class="space-y-2">
          <div
            v-for="rule in rules"
            :key="rule.metric"
            class="grid items-end gap-2 rounded-md border border-border p-3 sm:grid-cols-[1fr_7rem_9rem]"
          >
            <div class="space-y-1">
              <p class="text-sm font-medium">{{ metricLabel(rule.metric) }}</p>
              <Switch v-model="rule.enabled">{{ t('systems.alertsEnabled') }}</Switch>
            </div>
            <div v-if="numeric(rule.metric)" class="grid gap-1">
              <label class="text-[11px] text-muted-foreground" :for="`alert-threshold-${rule.metric}`">
                {{ t('systems.alertsThreshold') }}
              </label>
              <Input
                :id="`alert-threshold-${rule.metric}`"
                v-model="rule.threshold"
                type="number"
                min="1"
                max="100"
                :disabled="!rule.enabled"
              />
            </div>
            <div v-else class="text-[11px] text-muted-foreground">
              {{ t('systems.alertsDownHelp') }}
            </div>
            <div class="grid gap-1">
              <label class="text-[11px] text-muted-foreground" :for="`alert-minutes-${rule.metric}`">
                {{ t('systems.alertsMinutes') }}
              </label>
              <Input
                :id="`alert-minutes-${rule.metric}`"
                v-model="rule.minutes"
                type="number"
                min="1"
                max="1440"
                :disabled="!rule.enabled"
              />
            </div>
          </div>
        </div>
      </section>

      <!-- Channels ------------------------------------------------------ -->
      <section class="space-y-2 border-t border-border pt-4">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <h3 class="text-sm font-semibold">{{ t('systems.alertsChannels') }}</h3>
          <Badge variant="secondary">{{ t('systems.alertsLinked', { count: linkedCount }) }}</Badge>
        </div>
        <p class="text-xs text-muted-foreground">{{ t('systems.alertsChannelsHelp') }}</p>

        <p v-if="!loading && !channels.length" class="text-xs text-muted-foreground">
          {{ t('systems.alertsNoChannels') }}
        </p>

        <div v-else class="space-y-2">
          <div
            v-for="row in channels"
            :key="row.id"
            class="rounded-md border border-border p-3"
          >
            <div class="flex flex-wrap items-center justify-between gap-2">
              <div class="flex items-center gap-2">
                <BellRing class="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
                <p class="text-sm font-medium">{{ row.name }}</p>
                <Badge variant="secondary">{{ row.type }}</Badge>
              </div>
              <Switch :model-value="row.linked" @update:model-value="toggleLink(row, $event)">
                {{ t('systems.alertsLinkedSwitch') }}
              </Switch>
            </div>
            <div v-if="row.linked" class="mt-2 flex flex-wrap gap-4">
              <Checkbox v-model="row.on_down">{{ t('systems.alertsOnDown') }}</Checkbox>
              <Checkbox v-model="row.on_up">{{ t('systems.alertsOnUp') }}</Checkbox>
              <Checkbox v-model="row.on_threshold">{{ t('systems.alertsOnThreshold') }}</Checkbox>
              <Checkbox v-model="row.on_recovered">{{ t('systems.alertsOnRecovered') }}</Checkbox>
            </div>
          </div>
        </div>
      </section>
    </div>

    <template #footer>
      <Button variant="ghost" @click="close">{{ t('common.cancel') }}</Button>
      <Button :loading="store.saving" @click="save">
        <BellRing class="h-3.5 w-3.5" aria-hidden="true" />
        {{ t('common.save') }}
      </Button>
    </template>
  </Dialog>
</template>

