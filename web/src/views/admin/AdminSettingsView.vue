<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Save } from 'lucide-vue-next'
import Alert from '@/components/ui/Alert.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Select from '@/components/ui/Select.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { useAppStore } from '@/stores/app'
import { useToastStore } from '@/stores/toast'
import type { AdminSettings } from '@/lib/types'

const { t } = useI18n()
const toast = useToastStore()
const app = useAppStore()

const settings = ref<AdminSettings | null>(null)
const form = ref({ default_locale: 'en-US', default_theme: 'system', time_format: 'auto', app_name: '' })
const saving = ref(false)

const localeOptions = computed(() =>
  (settings.value?.supported_locales ?? []).map((value) => ({ value, label: value })),
)
const themeOptions = computed(() => [
  { value: 'system', label: t('settings.themeSystem') },
  { value: 'light', label: t('settings.themeLight') },
  { value: 'dark', label: t('settings.themeDark') },
])
const timeFormatLabels: Record<string, string> = {
  auto: 'settings.timeFormatAuto',
  '12h': 'settings.timeFormat12h',
  '24h': 'settings.timeFormat24h',
}
const timeFormatOptions = computed(() =>
  (settings.value?.supported_time_formats ?? ['auto', '12h', '24h']).map((value) => ({
    value,
    label: t(timeFormatLabels[value] ?? 'settings.timeFormatAuto'),
  })),
)

async function load(): Promise<void> {
  try {
    settings.value = await api.adminSettings()
    form.value = {
      default_locale: settings.value.default_locale,
      default_theme: settings.value.default_theme,
      time_format: settings.value.time_format ?? 'auto',
      app_name: settings.value.app_name,
    }
  } catch (error) {
    toast.error(t('common.error'), translateError(error))
  }
}

async function save(): Promise<void> {
  saving.value = true
  try {
    await api.updateAdminSettings(form.value)
    toast.success(t('common.saved'))
    await app.bootstrap()
  } catch (error) {
    toast.error(t('common.error'), translateError(error))
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="flex flex-col gap-4">
    <header>
      <h1 class="text-lg font-semibold">{{ t('settings.title') }}</h1>
      <p class="text-xs text-muted-foreground">{{ t('settings.subtitle') }}</p>
    </header>

    <Card :title="t('settings.defaultsHelp')">
      <div class="grid gap-3 sm:grid-cols-3">
        <div class="grid gap-1">
          <Label for="settings-locale">{{ t('settings.defaultLocale') }}</Label>
          <Select id="settings-locale" v-model="form.default_locale" :options="localeOptions" />
        </div>
        <div class="grid gap-1">
          <Label for="settings-theme">{{ t('settings.defaultTheme') }}</Label>
          <Select id="settings-theme" v-model="form.default_theme" :options="themeOptions" />
        </div>
        <div class="grid gap-1">
          <Label for="settings-name">{{ t('settings.appName') }}</Label>
          <Input id="settings-name" v-model="form.app_name" />
        </div>
        <div class="grid gap-1">
          <Label for="settings-time-format" :help="t('settings.timeFormatHelp')">
            {{ t('settings.timeFormat') }}
          </Label>
          <Select id="settings-time-format" v-model="form.time_format" :options="timeFormatOptions" />
        </div>
      </div>

      <template #footer>
        <Button size="sm" :loading="saving" @click="save">
          <Save class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('common.save') }}
        </Button>
      </template>
    </Card>

    <Card :title="t('settings.instanceInfo')">
      <dl class="grid gap-3 text-xs sm:grid-cols-2">
        <div>
          <dt class="text-muted-foreground">{{ t('app.instance') }}</dt>
          <dd class="mt-0.5 font-mono">{{ settings?.app_url }}</dd>
        </div>
        <div>
          <dt class="text-muted-foreground">{{ t('settings.authMethod') }}</dt>
          <dd class="mt-0.5">{{ settings?.auth_method }}</dd>
        </div>
        <div>
          <dt class="text-muted-foreground">{{ t('settings.clusterEnabled') }}</dt>
          <dd class="mt-0.5">{{ settings?.cluster_enabled ? t('common.yes') : t('common.no') }}</dd>
        </div>
        <div>
          <dt class="text-muted-foreground">{{ t('app.version') }}</dt>
          <dd class="mt-0.5">{{ settings?.version }}</dd>
        </div>
      </dl>
      <Alert variant="info" class="mt-3">{{ t('settings.defaultsHelp') }}</Alert>
    </Card>
  </div>
</template>
