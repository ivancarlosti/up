<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Plus, Trash2 } from 'lucide-vue-next'
import Button from '@/components/ui/Button.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Select from '@/components/ui/Select.vue'
import Switch from '@/components/ui/Switch.vue'
import Textarea from '@/components/ui/Textarea.vue'
import type { Header, MonitorConfig, MonitorType } from '@/lib/types'

/**
 * MonitorConfigFields renders the probe options of a monitor type. It is shared
 * by the monitor form and by the template form (a template is a monitor without
 * a target), which is why `withTarget` exists: a template never carries the URL,
 * the host:port or the hostname.
 *
 * The component mutates `config` in place: the object comes from a reactive form
 * of the parent, so a copy would break the two way binding.
 */
const props = withDefaults(
  defineProps<{
    type: MonitorType
    config: MonitorConfig
    withTarget?: boolean
  }>(),
  { withTarget: true },
)
const { t } = useI18n()

const type = computed(() => props.type)
const config = computed(() => props.config)

const methodOptions = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'].map((value) => ({ value, label: value }))
const encodingOptions = ['json', 'form', 'xml', 'raw'].map((value) => ({ value, label: value }))
const recordTypeOptions = ['A', 'AAAA', 'CNAME', 'MX', 'TXT', 'NS', 'SOA'].map((value) => ({ value, label: value }))
const authOptions = computed(() => [
  { value: 'none', label: t('monitor.authNone') },
  { value: 'basic', label: t('monitor.authBasic') },
  { value: 'bearer', label: t('monitor.authBearer') },
])

function addHeader(): void {
  if (!config.value.headers) config.value.headers = []
  config.value.headers.push({ key: '', value: '' } as Header)
}

function removeHeader(index: number): void {
  config.value.headers?.splice(index, 1)
}
</script>

<template>
  <!-- HTTP(s) / Keyword -->
  <section v-if="type === 'http' || type === 'keyword'" class="grid gap-3 border-t border-border pt-4 sm:grid-cols-2">
    <div v-if="withTarget" class="grid gap-1 sm:col-span-2">
      <Label for="monitor-url" required>{{ t('monitor.url') }}</Label>
      <Input id="monitor-url" v-model="config.url" placeholder="https://example.com/health" />
    </div>
    <div class="grid gap-1">
      <Label for="monitor-method">{{ t('monitor.method') }}</Label>
      <Select id="monitor-method" v-model="config.method" :options="methodOptions" />
    </div>
    <div class="grid gap-1">
      <Label for="monitor-encoding">{{ t('monitor.encoding') }}</Label>
      <Select id="monitor-encoding" v-model="config.encoding" :options="encodingOptions" />
    </div>
    <div class="grid gap-1 sm:col-span-2">
      <Label for="monitor-body">{{ t('monitor.body') }}</Label>
      <Textarea id="monitor-body" v-model="config.body" :rows="3" />
    </div>

    <div class="grid gap-2 sm:col-span-2">
      <Label>{{ t('monitor.headers') }}</Label>
      <div v-for="(header, index) in config.headers ?? []" :key="index" class="flex items-center gap-2">
        <Input v-model="header.key" :placeholder="t('monitor.headerKey')" />
        <Input v-model="header.value" :placeholder="t('monitor.headerValue')" />
        <Button variant="ghost" size="icon" @click="removeHeader(index)">
          <Trash2 class="h-3.5 w-3.5" aria-hidden="true" />
        </Button>
      </div>
      <Button variant="outline" size="sm" @click="addHeader">
        <Plus class="h-3.5 w-3.5" aria-hidden="true" />
        {{ t('monitor.addHeader') }}
      </Button>
    </div>

    <div class="grid gap-1">
      <Label for="monitor-auth">{{ t('monitor.authentication') }}</Label>
      <Select id="monitor-auth" v-model="config.auth_type" :options="authOptions" />
    </div>
    <div class="grid gap-1">
      <Label for="monitor-codes">{{ t('monitor.acceptedCodes') }}</Label>
      <Input id="monitor-codes" v-model="config.accepted_status_codes" placeholder="200-299" />
      <span class="text-[11px] text-muted-foreground">{{ t('monitor.acceptedCodesHelp') }}</span>
    </div>
    <div v-if="config.auth_type === 'basic'" class="grid gap-1">
      <Label for="monitor-user">{{ t('monitor.username') }}</Label>
      <Input id="monitor-user" v-model="config.basic_user" />
    </div>
    <div v-if="config.auth_type === 'basic'" class="grid gap-1">
      <Label for="monitor-pass">{{ t('monitor.password') }}</Label>
      <Input id="monitor-pass" v-model="config.basic_pass" type="password" />
    </div>
    <div v-if="config.auth_type === 'bearer'" class="grid gap-1 sm:col-span-2">
      <Label for="monitor-token">{{ t('monitor.token') }}</Label>
      <Input id="monitor-token" v-model="config.bearer_token" type="password" />
    </div>

    <div v-if="type === 'keyword'" class="grid gap-1 sm:col-span-2">
      <Label for="monitor-keyword" required>{{ t('monitor.keyword') }}</Label>
      <Input id="monitor-keyword" v-model="config.keyword" :placeholder="t('monitor.keywordPlaceholder')" />
    </div>
    <div v-if="type === 'keyword'" class="flex flex-wrap items-center gap-4 sm:col-span-2">
      <Switch v-model="config.invert_keyword as boolean">{{ t('monitor.invertKeyword') }}</Switch>
      <Switch v-model="config.case_sensitive as boolean">{{ t('monitor.caseSensitive') }}</Switch>
    </div>

    <div class="flex flex-wrap items-center gap-4 sm:col-span-2">
      <Switch v-model="config.ignore_tls as boolean">{{ t('monitor.ignoreTls') }}</Switch>
      <div class="grid gap-1">
        <Label for="monitor-redirects">{{ t('monitor.maxRedirects') }}</Label>
        <Input id="monitor-redirects" v-model="config.max_redirects" type="number" min="0" max="20" class="w-24" />
      </div>
    </div>
  </section>

  <!-- TCP -->
  <section v-else-if="type === 'tcp'" class="grid gap-3 border-t border-border pt-4 sm:grid-cols-2">
    <div v-if="withTarget" class="grid gap-1">
      <Label for="monitor-host" required>{{ t('monitor.tcpHost') }}</Label>
      <Input id="monitor-host" v-model="config.host" placeholder="db.internal" />
    </div>
    <div class="grid gap-1">
      <Label for="monitor-port" :required="withTarget">{{ t('monitor.tcpPort') }}</Label>
      <Input id="monitor-port" v-model="config.port" type="number" min="1" max="65535" />
    </div>
    <div class="grid gap-1">
      <Label for="monitor-send">{{ t('monitor.tcpSend') }}</Label>
      <Input id="monitor-send" v-model="config.send" />
    </div>
    <div class="grid gap-1">
      <Label for="monitor-expect">{{ t('monitor.tcpExpect') }}</Label>
      <Input id="monitor-expect" v-model="config.expect" />
    </div>
    <p class="text-[11px] text-muted-foreground sm:col-span-2">{{ t('monitor.tcpHelp') }}</p>
  </section>

  <!-- DNS -->
  <section v-else class="grid gap-3 border-t border-border pt-4 sm:grid-cols-2">
    <div v-if="withTarget" class="grid gap-1">
      <Label for="monitor-hostname" required>{{ t('monitor.dnsHostname') }}</Label>
      <Input id="monitor-hostname" v-model="config.hostname" placeholder="example.com" />
    </div>
    <div class="grid gap-1">
      <Label for="monitor-resolver">{{ t('monitor.dnsResolver') }}</Label>
      <Input id="monitor-resolver" v-model="config.resolver_server" placeholder="1.1.1.1" />
    </div>
    <div class="grid gap-1">
      <Label for="monitor-record">{{ t('monitor.dnsRecordType') }}</Label>
      <Select id="monitor-record" v-model="config.record_type" :options="recordTypeOptions" />
    </div>
    <div class="grid gap-1">
      <Label for="monitor-expected">{{ t('monitor.dnsExpected') }}</Label>
      <Input id="monitor-expected" v-model="config.expected_value" />
    </div>
    <div class="sm:col-span-2">
      <Switch v-model="config.invert_check as boolean">{{ t('monitor.dnsInvert') }}</Switch>
      <p class="mt-1 text-[11px] text-muted-foreground">{{ t('monitor.dnsInvertHelp') }}</p>
    </div>
  </section>
</template>
