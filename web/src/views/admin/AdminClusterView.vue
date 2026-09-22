<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Copy, Eye, EyeOff, Link2, RefreshCw, Save } from 'lucide-vue-next'
import Alert from '@/components/ui/Alert.vue'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Select from '@/components/ui/Select.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { formatDateTime } from '@/lib/format'
import { copyToClipboard } from '@/lib/utils'
import { useToastStore } from '@/stores/toast'
import type { ClusterStatus, FailureStrategy, NodeUnavailableStrategy, NotificationSenderStrategy } from '@/lib/types'

const { t, locale } = useI18n()
const toasts = useToastStore()

const status = ref<ClusterStatus | null>(null)
const privateKey = ref('')
const revealKey = ref(false)
const saving = ref(false)
const joining = ref(false)
const joinForm = ref({ primary_url: '', private_key: '' })
const strategies = ref<{
  failure_strategy: FailureStrategy
  node_unavailable_strategy: NodeUnavailableStrategy
  notification_sender: NotificationSenderStrategy
}>({
  failure_strategy: 'ALL_NODES_FAIL',
  node_unavailable_strategy: 'IGNORE',
  notification_sender: 'ANY_WITH_LOCK',
})

const failureOptions = computed(() => [
  { value: 'ANY_NODE_FAILS', label: t('cluster.failureAny') },
  { value: 'ALL_NODES_FAIL', label: t('cluster.failureAll') },
  { value: 'QUORUM', label: t('cluster.failureQuorum') },
])
const unavailableOptions = computed(() => [
  { value: 'IGNORE', label: t('cluster.unavailableIgnore') },
  { value: 'MARK_DEGRADED', label: t('cluster.unavailableDegraded') },
])
const senderOptions = computed(() => [
  { value: 'PRIMARY_ONLY', label: t('cluster.senderPrimary') },
  { value: 'ANY_WITH_LOCK', label: t('cluster.senderAnyWithLock') },
])
const rolesLabel = computed(() => (status.value?.is_primary ? t('cluster.primary') : t('cluster.secondary')))

async function load(): Promise<void> {
  try {
    status.value = await api.clusterStatus()
    strategies.value = {
      failure_strategy: status.value.settings.failure_strategy,
      node_unavailable_strategy: status.value.settings.node_unavailable_strategy,
      notification_sender: status.value.settings.notification_sender,
    }
    const key = await api.clusterPrivateKey()
    privateKey.value = key.private_key
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  }
}

async function saveStrategies(): Promise<void> {
  saving.value = true
  try {
    await api.updateClusterSettings(strategies.value)
    toasts.success(t('common.saved'))
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    saving.value = false
  }
}

async function regenerateKey(): Promise<void> {
  try {
    const response = await api.regenerateClusterKey()
    privateKey.value = response.private_key
    revealKey.value = true
    toasts.success(response.message)
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  }
}

async function joinCluster(): Promise<void> {
  joining.value = true
  try {
    const response = await api.joinCluster(joinForm.value)
    toasts.success(t('cluster.joinSuccess'), response.message)
    await load()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    joining.value = false
  }
}

async function leaveCluster(): Promise<void> {
  try {
    await api.leaveCluster()
    await load()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  }
}

async function sendHeartbeat(): Promise<void> {
  try {
    await api.clusterHeartbeat()
    toasts.success(t('cluster.heartbeatOk'))
    await load()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  }
}

async function copy(value: string): Promise<void> {
  if (await copyToClipboard(value)) toasts.success(t('common.copied'))
}

onMounted(load)
</script>

<template>
  <div class="flex flex-col gap-4">
    <header>
      <h1 class="text-lg font-semibold">{{ t('cluster.title') }}</h1>
      <p class="text-xs text-muted-foreground">{{ t('cluster.subtitle') }}</p>
    </header>

    <Alert v-if="status && !status.enabled" variant="warning">
      <p class="font-medium">{{ t('cluster.disabled') }}</p>
      <p class="mt-1">{{ t('cluster.disabledHint') }}</p>
    </Alert>

    <Card :title="t('cluster.thisNode')">
      <template #actions>
        <Button variant="outline" size="sm" @click="sendHeartbeat">
          <RefreshCw class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('cluster.lastHeartbeat') }}
        </Button>
      </template>

      <dl class="grid gap-3 text-xs sm:grid-cols-3">
        <div>
          <dt class="text-muted-foreground">{{ t('cluster.nodeId') }}</dt>
          <dd class="mt-0.5 font-mono">{{ status?.node_id }}</dd>
        </div>
        <div>
          <dt class="text-muted-foreground">{{ t('cluster.nodeName') }}</dt>
          <dd class="mt-0.5">{{ status?.node_name }}</dd>
        </div>
        <div>
          <dt class="text-muted-foreground">{{ t('cluster.role') }}</dt>
          <dd class="mt-0.5">
            <Badge :variant="status?.is_primary ? 'success' : 'secondary'">{{ rolesLabel }}</Badge>
          </dd>
        </div>
      </dl>

      <div class="mt-4 grid gap-2">
        <Label :help="t('cluster.privateKeyHelp')">{{ t('cluster.privateKey') }}</Label>
        <div class="flex flex-wrap items-center gap-2">
          <code class="flex-1 overflow-x-auto rounded bg-muted px-2 py-1 text-xs">
            {{ revealKey ? privateKey : '••••••••••••••••••••••••••••••••' }}
          </code>
          <Button variant="outline" size="sm" @click="revealKey = !revealKey">
            <EyeOff v-if="revealKey" class="h-3.5 w-3.5" aria-hidden="true" />
            <Eye v-else class="h-3.5 w-3.5" aria-hidden="true" />
            {{ revealKey ? t('cluster.hideKey') : t('cluster.revealKey') }}
          </Button>
          <Button variant="outline" size="sm" @click="copy(privateKey)">
            <Copy class="h-3.5 w-3.5" aria-hidden="true" />
            {{ t('common.copy') }}
          </Button>
          <Button variant="destructive" size="sm" @click="regenerateKey">
            {{ t('cluster.regenerateKey') }}
          </Button>
        </div>
        <p class="text-[11px] text-muted-foreground">{{ t('cluster.regenerateWarning') }}</p>
      </div>
    </Card>

    <Card :title="t('cluster.join')" :description="t('cluster.joinHelp')">
      <div class="grid gap-3 sm:grid-cols-2">
        <div class="grid gap-1">
          <Label for="join-url" required>{{ t('cluster.primaryUrl') }}</Label>
          <Input id="join-url" v-model="joinForm.primary_url" placeholder="http://up-node-1:3000" />
        </div>
        <div class="grid gap-1">
          <Label for="join-key" required>{{ t('cluster.privateKey') }}</Label>
          <Input id="join-key" v-model="joinForm.private_key" type="password" />
        </div>
      </div>

      <template #footer>
        <Button variant="ghost" size="sm" class="text-status-down" @click="leaveCluster">
          {{ t('cluster.leave') }}
        </Button>
        <Button size="sm" :loading="joining" @click="joinCluster">
          <Link2 class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('cluster.joinButton') }}
        </Button>
      </template>
    </Card>

    <Card :title="t('cluster.nodes')">
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ t('common.name') }}</th>
            <th>{{ t('cluster.apiUrl') }}</th>
            <th>{{ t('common.status') }}</th>
            <th>{{ t('cluster.lastHeartbeat') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="node in status?.nodes ?? []" :key="node.node_id">
            <td>
              <span class="flex items-center gap-2">
                {{ node.name }}
                <Badge v-if="node.is_primary" variant="success">{{ t('cluster.primary') }}</Badge>
                <Badge v-if="node.is_self" variant="outline">{{ t('cluster.self') }}</Badge>
              </span>
            </td>
            <td class="font-mono">{{ node.api_url }}</td>
            <td>
              <Badge :variant="node.status === 'online' ? 'success' : node.status === 'degraded' ? 'warning' : 'danger'">
                {{ node.status === 'online' ? t('cluster.online') : t('cluster.offline') }}
              </Badge>
            </td>
            <td class="text-muted-foreground">{{ formatDateTime(node.last_heartbeat, locale) }}</td>
          </tr>
        </tbody>
      </table>
    </Card>

    <Card :title="t('cluster.strategies')">
      <div class="grid gap-3 sm:grid-cols-3">
        <div class="grid gap-1">
          <Label for="strategy-failure" :help="t('cluster.failureAnyHelp')">{{ t('cluster.failureStrategy') }}</Label>
          <Select id="strategy-failure" v-model="strategies.failure_strategy" :options="failureOptions" />
        </div>
        <div class="grid gap-1">
          <Label for="strategy-node" :help="t('cluster.nodeUnavailableHelp')">{{ t('cluster.nodeUnavailable') }}</Label>
          <Select id="strategy-node" v-model="strategies.node_unavailable_strategy" :options="unavailableOptions" />
        </div>
        <div class="grid gap-1">
          <Label for="strategy-sender" :help="t('cluster.notificationSenderHelp')">
            {{ t('cluster.notificationSender') }}
          </Label>
          <Select id="strategy-sender" v-model="strategies.notification_sender" :options="senderOptions" />
        </div>
      </div>

      <template #footer>
        <Button size="sm" :loading="saving" @click="saveStrategies">
          <Save class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('cluster.saveStrategies') }}
        </Button>
      </template>
    </Card>
  </div>
</template>

