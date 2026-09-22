<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { KeyRound, Plus, ShieldAlert, Trash2 } from 'lucide-vue-next'
import Alert from '@/components/ui/Alert.vue'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import Checkbox from '@/components/ui/Checkbox.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import Dialog from '@/components/ui/Dialog.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Select from '@/components/ui/Select.vue'
import Switch from '@/components/ui/Switch.vue'
import { api, APIError } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { formatDateTime } from '@/lib/format'
import { copyToClipboard } from '@/lib/utils'
import { useToastStore } from '@/stores/toast'
import type { APIToken, IPRule } from '@/lib/types'

const { t, locale } = useI18n()
const toasts = useToastStore()

const tokens = ref<APIToken[]>([])
const rules = ref<IPRule[]>([])
const clientIP = ref('')
const bypassed = ref(false)
const tokenDialog = ref(false)
const ruleDialog = ref(false)
const newToken = ref<APIToken | null>(null)
const saving = ref(false)

const tokenForm = ref({ name: '', scopes: ['read'] as string[], expires_in_days: 30 })
const ruleForm = ref<Partial<IPRule>>({ cidr: '', action: 'allow', scope: 'all', note: '', enabled: true })

const confirmOpen = ref(false)
const pendingToken = ref<APIToken | null>(null)

const scopeOptions = computed(() => [
  { value: 'all', label: t('security.scopeAll') },
  { value: 'dashboard', label: t('security.scopeDashboard') },
  { value: 'api', label: t('security.scopeApi') },
  { value: 'public', label: t('security.scopePublic') },
])
const actionOptions = computed(() => [
  { value: 'allow', label: t('security.actionAllow') },
  { value: 'deny', label: t('security.actionDeny') },
])

async function load(): Promise<void> {
  try {
    const [tokenList, ruleResponse] = await Promise.all([api.tokens(), api.ipRules()])
    tokens.value = tokenList
    rules.value = ruleResponse.rules
    clientIP.value = ruleResponse.client_ip
    bypassed.value = ruleResponse.bypassed
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  }
}

async function createToken(): Promise<void> {
  saving.value = true
  try {
    newToken.value = await api.createToken({
      name: tokenForm.value.name,
      scopes: tokenForm.value.scopes,
      expires_in_days: Number(tokenForm.value.expires_in_days) || 0,
    })
    tokenDialog.value = false
    toasts.success(t('common.created'))
    await load()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    saving.value = false
  }
}

async function revokeToken(token: APIToken): Promise<void> {
  try {
    await api.revokeToken(token.id)
    toasts.success(t('security.revoked'))
    await load()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  }
}

async function removeToken(): Promise<void> {
  if (!pendingToken.value) return
  try {
    await api.deleteToken(pendingToken.value.id)
    confirmOpen.value = false
    await load()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  }
}

async function createRule(): Promise<void> {
  saving.value = true
  try {
    await api.createIPRule(ruleForm.value)
    ruleDialog.value = false
    ruleForm.value = { cidr: '', action: 'allow', scope: 'all', note: '', enabled: true }
    await load()
  } catch (error) {
    toasts.error(t('common.error'), error instanceof APIError ? translateError(error) : String(error))
  } finally {
    saving.value = false
  }
}

async function removeRule(rule: IPRule): Promise<void> {
  try {
    await api.deleteIPRule(rule.id)
    await load()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  }
}

async function toggleScope(scope: 'read' | 'write', value: boolean): Promise<void> {
  const set = new Set(tokenForm.value.scopes)
  if (value) set.add(scope)
  else set.delete(scope)
  tokenForm.value.scopes = [...set]
}

async function copy(text: string): Promise<void> {
  if (await copyToClipboard(text)) toasts.success(t('common.copied'))
}

onMounted(load)
</script>


<template>
  <div class="flex flex-col gap-4">
    <header>
      <h1 class="text-lg font-semibold">{{ t('security.title') }}</h1>
      <p class="text-xs text-muted-foreground">{{ t('security.subtitle') }}</p>
    </header>

    <Alert v-if="bypassed" variant="warning">{{ t('security.bypassWarning') }}</Alert>

    <Alert v-if="newToken" variant="success">
      <p class="font-medium">{{ t('security.tokenCreated') }}</p>
      <div class="mt-2 flex items-center gap-2">
        <code class="flex-1 overflow-x-auto rounded bg-background px-2 py-1">{{ newToken.token }}</code>
        <Button size="sm" variant="outline" @click="copy(newToken.token ?? '')">{{ t('common.copy') }}</Button>
      </div>
    </Alert>

    <Card :title="t('security.tokens')" :description="t('security.tokensHelp')">
      <template #actions>
        <Button size="sm" @click="tokenDialog = true">
          <Plus class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('security.newToken') }}
        </Button>
      </template>

      <table class="data-table">
        <thead>
          <tr>
            <th>{{ t('security.tokenName') }}</th>
            <th>{{ t('security.tokenPrefix') }}</th>
            <th>{{ t('security.tokenScopes') }}</th>
            <th>{{ t('security.lastUsed') }}</th>
            <th>{{ t('common.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="token in tokens" :key="token.id">
            <td>
              <span class="flex items-center gap-2">
                <KeyRound class="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
                {{ token.name }}
              </span>
            </td>
            <td class="font-mono">{{ token.prefix }}</td>
            <td>{{ token.scopes.join(', ') }}</td>
            <td class="text-muted-foreground">
              {{ token.last_used_at ? formatDateTime(token.last_used_at, locale) : t('security.neverUsed') }}
              <span v-if="token.last_used_ip"> · {{ token.last_used_ip }}</span>
            </td>
            <td>
              <div class="flex items-center gap-1">
                <Badge v-if="token.revoked_at" variant="danger">{{ t('security.revoked') }}</Badge>
                <Button v-else variant="ghost" size="sm" @click="revokeToken(token)">{{ t('security.revoke') }}</Button>
                <Button
                  variant="ghost"
                  size="sm"
                  class="text-status-down"
                  @click="(pendingToken = token), (confirmOpen = true)"
                >
                  <Trash2 class="h-3.5 w-3.5" aria-hidden="true" />
                </Button>
              </div>
            </td>
          </tr>
          <tr v-if="!tokens.length">
            <td class="py-2 text-muted-foreground" colspan="5">{{ t('common.none') }}</td>
          </tr>
        </tbody>
      </table>
    </Card>

    <Card :title="t('security.ipRules')" :description="t('security.ipRulesHelp')">
      <template #actions>
        <Button size="sm" @click="ruleDialog = true">
          <Plus class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('security.newRule') }}
        </Button>
      </template>

      <Alert variant="warning" class="mb-3">
        <span class="inline-flex items-center gap-2">
          <ShieldAlert class="h-4 w-4" aria-hidden="true" />
          {{ t('security.lockoutWarning') }}
        </span>
        <p class="mt-1">{{ t('security.yourIp') }}: <code>{{ clientIP }}</code></p>
      </Alert>

      <table class="data-table">
        <thead>
          <tr>
            <th>{{ t('security.ruleCidr') }}</th>
            <th>{{ t('security.ruleAction') }}</th>
            <th>{{ t('security.ruleScope') }}</th>
            <th>{{ t('security.note') }}</th>
            <th>{{ t('common.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="rule in rules" :key="rule.id">
            <td class="font-mono">{{ rule.cidr }}</td>
            <td>
              <Badge :variant="rule.action === 'allow' ? 'success' : 'danger'">
                {{ rule.action === 'allow' ? t('security.actionAllow') : t('security.actionDeny') }}
              </Badge>
            </td>
            <td>{{ rule.scope }}</td>
            <td class="text-muted-foreground">{{ rule.note }}</td>
            <td>
              <Button variant="ghost" size="sm" class="text-status-down" @click="removeRule(rule)">
                <Trash2 class="h-3.5 w-3.5" aria-hidden="true" />
              </Button>
            </td>
          </tr>
          <tr v-if="!rules.length">
            <td class="py-2 text-muted-foreground" colspan="5">{{ t('common.none') }}</td>
          </tr>
        </tbody>
      </table>
    </Card>

    <Dialog v-model="tokenDialog" :title="t('security.newToken')">
      <div class="grid gap-3">
        <div class="grid gap-1">
          <Label for="token-name" required>{{ t('security.tokenName') }}</Label>
          <Input id="token-name" v-model="tokenForm.name" placeholder="ci" />
        </div>
        <div class="grid gap-1">
          <Label for="token-days" :help="t('security.tokenExpiryHelp')">{{ t('security.tokenExpiry') }}</Label>
          <Input id="token-days" v-model="tokenForm.expires_in_days" type="number" min="0" />
        </div>
        <div class="flex flex-wrap items-center gap-4">
          <Checkbox :model-value="tokenForm.scopes.includes('read')" @update:model-value="toggleScope('read', $event)">
            {{ t('security.scopeRead') }}
          </Checkbox>
          <Checkbox :model-value="tokenForm.scopes.includes('write')" @update:model-value="toggleScope('write', $event)">
            {{ t('security.scopeWrite') }}
          </Checkbox>
        </div>
      </div>

      <template #footer>
        <Button variant="outline" @click="tokenDialog = false">{{ t('common.cancel') }}</Button>
        <Button :loading="saving" @click="createToken">{{ t('common.create') }}</Button>
      </template>
    </Dialog>

    <Dialog v-model="ruleDialog" :title="t('security.newRule')">
      <div class="grid gap-3">
        <div class="grid gap-1">
          <Label for="rule-cidr" required>{{ t('security.ruleCidr') }}</Label>
          <Input id="rule-cidr" v-model="ruleForm.cidr" placeholder="203.0.113.0/24" />
        </div>
        <div class="grid gap-1">
          <Label for="rule-action">{{ t('security.ruleAction') }}</Label>
          <Select id="rule-action" v-model="ruleForm.action" :options="actionOptions" />
        </div>
        <div class="grid gap-1">
          <Label for="rule-scope">{{ t('security.ruleScope') }}</Label>
          <Select id="rule-scope" v-model="ruleForm.scope" :options="scopeOptions" />
        </div>
        <div class="grid gap-1">
          <Label for="rule-note">{{ t('security.note') }}</Label>
          <Input id="rule-note" v-model="ruleForm.note" />
        </div>
        <Switch v-model="ruleForm.enabled as boolean">{{ t('common.enabled') }}</Switch>
      </div>

      <template #footer>
        <Button variant="outline" @click="ruleDialog = false">{{ t('common.cancel') }}</Button>
        <Button :loading="saving" @click="createRule">{{ t('common.create') }}</Button>
      </template>
    </Dialog>

    <ConfirmDialog
      v-model="confirmOpen"
      :title="t('common.delete')"
      :confirm-label="t('common.delete')"
      @confirm="removeToken"
    />
  </div>
</template>
