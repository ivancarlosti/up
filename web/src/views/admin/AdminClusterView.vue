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
import type { ClusterStatus, FailureStrategy, NodeUnavailableStrategy, NotificationSenderStrategy, PeerStatusReport } from '@/lib/types'

const { t, locale } = useI18n()
const toasts = useToastStore()

const status = ref<ClusterStatus | null>(null)
/** peers is the local synchronisation view: it is what shows a stalled or diverging
 * peer, which the registry view above cannot (a peer stays "online" while nothing
 * flows). It is loaded separately, so a node with the synchronisation disabled still
 * renders the rest of the page. */
const peers = ref<PeerStatusReport | null>(null)
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
  try {
    peers.value = await api.peerStatus()
  } catch {
    // The synchronisation endpoints refuse when the mode does not publish: that is a
    // normal state, not a failure of the page.
    peers.value = null
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

    <Card :title="t('cluster.syncTitle')">
      <div class="grid gap-3 sm:grid-cols-4">
        <div>
          <p class="text-xs text-muted-foreground">{{ t('cluster.pendingLinks') }}</p>
          <p class="text-lg font-semibold">
            {{ peers?.sync.pending_links ?? 0 }}
            <span v-if="(peers?.sync.pending_links_given_up ?? 0) > 0" class="text-xs text-status-down">
              ({{ peers?.sync.pending_links_given_up }} {{ t('cluster.givenUp') }})
            </span>
          </p>
        </div>
        <div>
          <p class="text-xs text-muted-foreground">{{ t('cluster.conflicts') }}</p>
          <p class="text-lg font-semibold">{{ peers?.sync.conflicts ?? 0 }}</p>
        </div>
        <div>
          <p class="text-xs text-muted-foreground">{{ t('cluster.deadLetters') }}</p>
          <p class="text-lg font-semibold">
            {{ peers?.sync.dead_letters ?? 0 }}
            <span v-if="(peers?.sync.dead_letters_skipped ?? 0) > 0" class="text-xs text-status-down">
              ({{ peers?.sync.dead_letters_skipped }} {{ t('cluster.skipped') }})
            </span>
          </p>
        </div>
        <div>
          <p class="text-xs text-muted-foreground">{{ t('cluster.quorum') }}</p>
          <p class="text-lg font-semibold">
            {{ peers?.sync.quorum_reachable ?? 0 }}/{{ peers?.sync.quorum_known ?? 0 }}
          </p>
          <p class="text-xs text-muted-foreground">
            {{ peers?.sync.quorum_settled ?? 0 }} {{ t('cluster.quorumSettled') }}
          </p>
        </div>
      </div>

      <Alert
        v-if="(peers?.sync.quorum_reachable ?? 0) < (peers?.sync.quorum_known ?? 0)"
        variant="warning"
        class="mt-3"
      >
        {{ t('cluster.quorumHint') }}
      </Alert>

      <table class="data-table mt-4">
        <thead>
          <tr>
            <th>{{ t('cluster.syncPeer') }}</th>
            <th>{{ t('cluster.syncCursor') }}</th>
            <th>{{ t('cluster.syncChecksums') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="peer in peers?.peers ?? []" :key="peer.peer_node_id">
            <td>{{ peer.peer_name || peer.peer_node_id }}</td>
            <td class="font-mono">{{ peer.last_change_id }}</td>
            <td>
              <Badge v-if="!peer.last_manifest_at" variant="outline">{{ t('cluster.syncNever') }}</Badge>
              <Badge v-else-if="peer.last_manifest_ok" variant="success">{{ t('cluster.syncAgree') }}</Badge>
              <Badge v-else variant="danger">{{ t('cluster.syncDisagree') }}</Badge>
            </td>
          </tr>
        </tbody>
      </table>

      <div v-if="(peers?.recent_conflicts?.length ?? 0) > 0" class="mt-4">
        <p class="mb-1 text-sm font-medium">{{ t('cluster.recentConflicts') }}</p>
        <table class="data-table">
          <thead>
            <tr>
              <th>{{ t('cluster.entity') }}</th>
              <th>{{ t('cluster.uuid') }}</th>
              <th>{{ t('cluster.kept') }}</th>
              <th>{{ t('cluster.lost') }}</th>
              <th>{{ t('cluster.detectedAt') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="conflict in peers?.recent_conflicts ?? []" :key="conflict.uuid + conflict.detected_at">
              <td>{{ conflict.entity }}</td>
              <td class="font-mono text-xs">{{ conflict.uuid }}</td>
              <td class="font-mono">{{ conflict.kept_origin }} r{{ conflict.kept_revision }}</td>
              <td class="font-mono">{{ conflict.lost_origin }} r{{ conflict.lost_revision }}</td>
              <td class="text-muted-foreground">{{ formatDateTime(conflict.detected_at, locale) }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-if="(peers?.recent_dead_letters?.length ?? 0) > 0" class="mt-4">
        <p class="mb-1 text-sm font-medium">{{ t('cluster.recentDeadLetters') }}</p>
        <table class="data-table">
          <thead>
            <tr>
              <th>{{ t('cluster.changeId') }}</th>
              <th>{{ t('cluster.entity') }}</th>
              <th>{{ t('cluster.attempts') }}</th>
              <th>{{ t('common.error') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="letter in peers?.recent_dead_letters ?? []" :key="letter.change_id">
              <td class="font-mono">{{ letter.change_id }}</td>
              <td>{{ letter.entity }}</td>
              <td>
                {{ letter.attempts }}
                <Badge v-if="letter.skipped" variant="danger">{{ t('cluster.skipped') }}</Badge>
              </td>
              <td class="text-xs text-muted-foreground">{{ letter.last_error }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <p v-if="!peers" class="text-sm text-muted-foreground">{{ t('cluster.syncUnavailable') }}</p>
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

