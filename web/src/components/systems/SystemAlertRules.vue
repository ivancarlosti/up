<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Input from '@/components/ui/Input.vue'
import Switch from '@/components/ui/Switch.vue'
import {
  defaultMinutesOf,
  defaultThresholdOf,
  unitSuffix,
  type AlertRuleForm,
} from '@/lib/system-alert-form'
import type { SystemAlertMetric, SystemAlertRuleConfig, SystemAlertSpec } from '@/lib/types-systems'

/**
 * SystemAlertRules is the editor of the alert rules of one machine, shared by
 * the alerts dialog and the "Hardware alert defaults" card of Admin > Settings.
 *
 * Everything that differs between two rules - the unit, the domain of the
 * threshold, whether it has a sustained window at all - comes from the
 * catalogue the API serves (models.SystemAlertSpec), so a rule added on the
 * server shows up here without touching this file. The parent owns the array:
 * this component edits the objects in place.
 */
const props = withDefaults(
  defineProps<{
    rules: AlertRuleForm[]
    specs: SystemAlertSpec[]
    /** The rule set a reset restores: shown as "default: ..." next to each field. */
    defaults?: SystemAlertRuleConfig[]
    disabled?: boolean
  }>(),
  { defaults: () => [], disabled: false },
)

const { t } = useI18n()

/** specOf returns the catalogue entry of one rule. */
function specOf(metric: SystemAlertMetric): SystemAlertSpec | undefined {
  return props.specs.find((spec) => spec.metric === metric)
}

/** metricLabel is the human name of a rule. */
function metricLabel(metric: SystemAlertMetric): string {
  return t(`systems.metric.${metric}`)
}

/** ruleHelp is the extra line some rules need under their name. */
function ruleHelp(metric: SystemAlertMetric): string {
  switch (metric) {
    case 'container':
      return t('systems.alertsContainerHelp')
    case 'smart':
      return t('systems.alertsSmartHelp')
    default:
      return ''
  }
}

/** hasThreshold is false for `down`, which watches silence instead of a number. */
function hasThreshold(metric: SystemAlertMetric): boolean {
  return specOf(metric)?.has_threshold ?? false
}

/** hasWindow is false for the state rules (unhealthy containers, failing disks). */
function hasWindow(metric: SystemAlertMetric): boolean {
  return specOf(metric)?.has_window ?? true
}

function thresholdMin(metric: SystemAlertMetric): number {
  return specOf(metric)?.min ?? 1
}

function thresholdMax(metric: SystemAlertMetric): number {
  return specOf(metric)?.max ?? 100
}

function thresholdStep(metric: SystemAlertMetric): number {
  return specOf(metric)?.step ?? 1
}

/** suffixOf is the symbol shown next to the number ("%", "°C", "MB/s"). */
function suffixOf(metric: SystemAlertMetric): string {
  return unitSuffix(specOf(metric))
}

/** thresholdHint is the threshold a reset would restore ("default: 80%"). */
function thresholdHint(metric: SystemAlertMetric): string {
  const spec = specOf(metric)
  if (!spec || !spec.has_threshold) return ''
  const value = defaultThresholdOf(spec, props.defaults)
  return t('systems.alertsDefault', { value: `${value}${suffixOf(metric)}` })
}

/** minutesHint is the window a reset would restore ("default: 5 min"). */
function minutesHint(metric: SystemAlertMetric): string {
  const spec = specOf(metric)
  if (!spec || !spec.has_window) return ''
  return t('systems.alertsDefaultMinutes', { value: defaultMinutesOf(spec, props.defaults) })
}
</script>

<template>
  <div class="space-y-2">
    <div
      v-for="rule in rules"
      :key="rule.metric"
      class="grid items-end gap-2 rounded-md border border-border p-3 sm:grid-cols-[1fr_9rem_9rem]"
    >
      <div class="space-y-1">
        <p class="text-sm font-medium">{{ metricLabel(rule.metric) }}</p>
        <Switch v-model="rule.enabled" :disabled="disabled">{{ t('systems.alertsEnabled') }}</Switch>
        <p v-if="ruleHelp(rule.metric)" class="text-[11px] text-muted-foreground">
          {{ ruleHelp(rule.metric) }}
        </p>
      </div>

      <div v-if="hasThreshold(rule.metric)" class="grid gap-1">
        <label class="text-[11px] text-muted-foreground" :for="`alert-threshold-${rule.metric}`">
          {{ t('systems.alertsThreshold') }}
          <span v-if="suffixOf(rule.metric)">({{ suffixOf(rule.metric) }})</span>
        </label>
        <Input
          :id="`alert-threshold-${rule.metric}`"
          v-model="rule.threshold"
          type="number"
          :min="thresholdMin(rule.metric)"
          :max="thresholdMax(rule.metric)"
          :step="thresholdStep(rule.metric)"
          :disabled="disabled || !rule.enabled"
        />
        <p v-if="thresholdHint(rule.metric)" class="text-[11px] text-muted-foreground">
          {{ thresholdHint(rule.metric) }}
        </p>
      </div>
      <div v-else class="text-[11px] text-muted-foreground">
        {{ t('systems.alertsDownHelp') }}
      </div>

      <div v-if="hasWindow(rule.metric)" class="grid gap-1">
        <label class="text-[11px] text-muted-foreground" :for="`alert-minutes-${rule.metric}`">
          {{ t('systems.alertsMinutes') }}
        </label>
        <Input
          :id="`alert-minutes-${rule.metric}`"
          v-model="rule.minutes"
          type="number"
          min="1"
          max="1440"
          :disabled="disabled || !rule.enabled"
        />
        <p v-if="minutesHint(rule.metric)" class="text-[11px] text-muted-foreground">
          {{ minutesHint(rule.metric) }}
        </p>
      </div>
      <div v-else class="text-[11px] text-muted-foreground">
        {{ t('systems.alertsNoWindow') }}
      </div>
    </div>
  </div>
</template>
