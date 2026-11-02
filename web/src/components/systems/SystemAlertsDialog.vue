<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { BellRing, Copy, RotateCcw } from 'lucide-vue-next'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Checkbox from '@/components/ui/Checkbox.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import Dialog from '@/components/ui/Dialog.vue'
import Select from '@/components/ui/Select.vue'
import Switch from '@/components/ui/Switch.vue'
import SystemAlertRules from '@/components/systems/SystemAlertRules.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { ruleFormOf, rulePayloadOf, type AlertRuleForm } from '@/lib/system-alert-form'
import type { Notification } from '@/lib/types-platform'
import type { System, SystemAlertRuleConfig, SystemAlertSpec, SystemAlerts, SystemAlertsPayload } from '@/lib/types-systems'
import { useSystemStore } from '@/stores/systems'
import { useToastStore } from '@/stores/toast'

/**
 * SystemAlertsDialog edits the alert configuration of one machine: the rule of
 * every metric (on/off, threshold, sustained minutes) and the channels it
 * notifies, with one toggle per event.
 *
 * The rules themselves are edited by SystemAlertRules, the same component the
 * Admin > Settings card uses: the catalogue, the units and the defaults all come
 * from the API, so the dialog never disagrees with the server about what a rule
 * is.
 *
 * Two shortcuts sit above the rules: "copy from another monitor" and "reset to
 * defaults". Both write immediately (and are confirmed first), because neither
 * can be undone by cancelling the dialog.
 */
const props = defineProps<{
  modelValue: boolean
  system: System | null
}>()

const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()

const { t } = useI18n()
const store = useSystemStore()
const toasts = useToastStore()

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

const rules = ref<AlertRuleForm[]>([])
const channels = ref<ChannelForm[]>([])
const specs = ref<SystemAlertSpec[]>([])
const defaults = ref<SystemAlertRuleConfig[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const copying = ref(false)
const resetting = ref(false)
const copySource = ref('')
const copyOpen = ref(false)
const resetOpen = ref(false)

/** sourceOptions are the machines whose rules can be copied onto this one. */
const sourceOptions = computed(() =>
  store.systems
    .filter((system) => system.id !== props.system?.id)
    .map((system) => ({ value: String(system.id), label: system.name })),
)

/** busy tells whether a rule write is in flight (both are immediate). */
const busy = computed(() => copying.value || resetting.value)

/** applyView replaces the form with what the API answered. */
function applyView(view: SystemAlerts): void {
  rules.value = view.rules.map(ruleFormOf)
  specs.value = view.specs
  defaults.value = view.defaults.rules
}

/** load fetches the alert configuration and the channels of the instance. */
async function load(): Promise<void> {
  const system = props.system
  if (!system) return
  loading.value = true
  error.value = null
  try {
    const [alerts, notifications] = await Promise.all([store.alerts(system.id), api.notifications()])
    applyView(alerts)
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
    rules: rules.value.map(rulePayloadOf),
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

/** copyRules copies the rules of the chosen machine onto this one. */
async function copyRules(): Promise<void> {
  const system = props.system
  const sourceId = Number(copySource.value)
  if (!system || !sourceId) return
  copying.value = true
  try {
    applyView(await store.copyAlerts(system.id, sourceId))
    copyOpen.value = false
    copySource.value = ''
    toasts.success(t('systems.alertsCopied'))
  } catch (cause) {
    toasts.error(t('systems.alertsCopyFailed'), translateError(cause))
  } finally {
    copying.value = false
  }
}

/** resetRules puts the global defaults back on this machine. */
async function resetRules(): Promise<void> {
  const system = props.system
  if (!system) return
  resetting.value = true
  try {
    applyView(await store.resetAlerts(system.id))
    resetOpen.value = false
    toasts.success(t('systems.alertsReset'))
  } catch (cause) {
    toasts.error(t('systems.alertsResetFailed'), translateError(cause))
  } finally {
    resetting.value = false
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
    specs.value = []
    defaults.value = []
    copySource.value = ''
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
        <div class="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h3 class="text-sm font-semibold">{{ t('systems.alertsRules') }}</h3>
            <p class="text-xs text-muted-foreground">{{ t('systems.alertsRulesHelp') }}</p>
          </div>
          <div class="flex flex-wrap items-end gap-2">
            <div class="grid gap-1">
              <label class="text-[11px] text-muted-foreground" for="alerts-copy-source">
                {{ t('systems.alertsCopyFrom') }}
              </label>
              <Select
                id="alerts-copy-source"
                v-model="copySource"
                class="w-44"
                :options="sourceOptions"
                :placeholder="t('systems.alertsCopyPick')"
                :disabled="busy || !sourceOptions.length"
              />
            </div>
            <Button variant="outline" size="sm" :disabled="!copySource || busy" :loading="copying" @click="copyRules">
              <Copy class="h-3.5 w-3.5" aria-hidden="true" />
              {{ t('systems.alertsCopy') }}
            </Button>
            <Button variant="outline" size="sm" :disabled="busy" @click="resetOpen = true">
              <RotateCcw class="h-3.5 w-3.5" aria-hidden="true" />
              {{ t('systems.alertsResetAction') }}
            </Button>
          </div>
        </div>
        <p class="text-xs text-muted-foreground">{{ t('systems.alertsCopyHelp') }}</p>

        <p v-if="loading && !rules.length" class="text-xs text-muted-foreground">{{ t('common.loading') }}</p>
        <SystemAlertRules v-else :rules="rules" :specs="specs" :defaults="defaults" :disabled="busy" />
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
      <Button :loading="store.saving" :disabled="busy" @click="save">
        <BellRing class="h-3.5 w-3.5" aria-hidden="true" />
        {{ t('common.save') }}
      </Button>
    </template>
  </Dialog>

  <!-- The reset is confirmed: it overwrites every rule of the machine. -->
  <ConfirmDialog
    v-model="resetOpen"
    :title="t('systems.alertsResetTitle')"
    :description="t('systems.alertsResetWarning')"
    :confirm-label="t('systems.alertsResetAction')"
    :loading="resetting"
    @confirm="resetRules"
  />
</template>
