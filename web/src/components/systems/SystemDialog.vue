<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Copy, KeyRound, Loader2 } from 'lucide-vue-next'
import Button from '@/components/ui/Button.vue'
import Dialog from '@/components/ui/Dialog.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Switch from '@/components/ui/Switch.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { useSystemStore } from '@/stores/systems'
import { useToastStore } from '@/stores/toast'
import type { AgentKeyInfo, System, SystemPayload } from '@/lib/types-systems'

/**
 * SystemDialog creates or edits a machine.
 *
 * Creation and editing are the same dialog on purpose: the fields are the same,
 * and the only difference is what it shows after saving - a new system answers
 * with its token and the docker line to paste on the machine, which is the one
 * moment that credential exists in clear.
 */
const props = defineProps<{ modelValue: boolean; system?: System | null }>()
const emit = defineEmits<{ 'update:modelValue': [value: boolean]; saved: [system: System] }>()

const { t } = useI18n()
const store = useSystemStore()
const toasts = useToastStore()

const form = reactive<SystemPayload>({ name: '', host: '', enabled: true })
const error = ref<string | null>(null)
const created = ref<System | null>(null)
const agentKey = ref<AgentKeyInfo | null>(null)
const keyError = ref<string | null>(null)

const editing = computed(() => Boolean(props.system))

watch(
  () => props.modelValue,
  (open) => {
    if (!open) return
    error.value = null
    created.value = null
    form.name = props.system?.name ?? ''
    form.host = props.system?.host ?? ''
    form.enabled = props.system?.enabled ?? true
    void loadAgentKey()
  },
)

/** loadAgentKey fetches the KEY the operator pastes into the agent. */
async function loadAgentKey(): Promise<void> {
  keyError.value = null
  try {
    agentKey.value = await api.agentKey()
  } catch (cause) {
    keyError.value = translateError(cause)
  }
}

/** copy writes a value to the clipboard and says so. */
async function copy(value: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(value)
    toasts.success(t('common.copied'))
  } catch {
    toasts.error(t('common.copyFailed'))
  }
}

/** finish closes the dialog once there is nothing left to submit. */
function finish(): void {
  emit('update:modelValue', false)
}

async function save(): Promise<void> {
  error.value = null
  // A creation already went through and the dialog is showing the one-time
  // token: the footer button (or Enter in a field) only closes it. Without this
  // guard the second submit POSTs the same system again and the API answers
  // ERR_SYSTEM_NAME_TAKEN, which reads as if the creation had failed.
  if (created.value) {
    finish()
    return
  }
  if (!form.name.trim()) {
    error.value = t('systems.nameRequired')
    return
  }
  try {
    const saved =
      editing.value && props.system
        ? await store.update(props.system.id, { ...form })
        : await store.create({ ...form })
    emit('saved', saved)
    if (!editing.value) {
      // A new system stays open: the token is shown once and the operator has to
      // copy it (or the docker line) before closing.
      created.value = saved
      return
    }
    emit('update:modelValue', false)
    toasts.success(t('systems.saved'))
  } catch (cause) {
    error.value = translateError(cause)
  }
}
</script>

<template>
  <Dialog
    :model-value="props.modelValue"
    :title="editing ? t('systems.editTitle') : t('systems.createTitle')"
    :description="editing ? t('systems.editDescription') : t('systems.createDescription')"
    wide
    @update:model-value="emit('update:modelValue', $event)"
  >
    <form class="space-y-4" @submit.prevent="save">
      <div class="grid gap-1.5">
        <Label required>{{ t('systems.name') }}</Label>
        <Input v-model="form.name" :placeholder="t('systems.namePlaceholder')" autocomplete="off" />
      </div>
      <div class="grid gap-1.5">
        <Label>{{ t('systems.host') }}</Label>
        <Input v-model="form.host" :placeholder="t('systems.hostPlaceholder')" autocomplete="off" />
        <p class="text-[11px] text-muted-foreground">{{ t('systems.hostHelp') }}</p>
      </div>

      <div>
        <Switch v-model="form.enabled" :label="t('systems.enabled')" />
        <p class="mt-1 text-[11px] text-muted-foreground">{{ t('systems.enabledHelp') }}</p>
      </div>

      <p v-if="error" class="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive">
        {{ error }}
      </p>

      <!-- The KEY is the same for the whole installation: it is derived from the
           cluster key, so a peer can take an agent over without the operator
           pasting anything twice. -->
      <div class="rounded-lg border border-border/70 bg-background/40 p-3">
        <p class="text-xs font-medium">{{ t('systems.agentKey') }}</p>
        <p class="mt-1 text-[11px] text-muted-foreground">
          {{ t('systems.agentKeyHelp', { version: agentKey?.minimum_version ?? '' }) }}
        </p>
        <p v-if="keyError" class="mt-2 text-xs text-destructive">{{ keyError }}</p>
        <div v-else-if="agentKey" class="mt-2 flex items-start gap-2">
          <code class="flex-1 break-all rounded bg-muted px-2 py-1 font-mono text-[11px]">{{ agentKey.key }}</code>
          <Button type="button" variant="outline" size="icon" :aria-label="t('common.copy')" @click="copy(agentKey.key)">
            <Copy class="h-3.5 w-3.5" aria-hidden="true" />
          </Button>
        </div>
        <p v-else class="mt-2 flex items-center gap-2 text-xs text-muted-foreground">
          <Loader2 class="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
          {{ t('common.loading') }}
        </p>
      </div>

      <!-- The one time answer of a creation: the token and the command. -->
      <div v-if="created" class="rounded-lg border border-primary/40 bg-primary/5 p-3">
        <p class="flex items-center gap-1.5 text-xs font-medium">
          <KeyRound class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('systems.tokenOnce') }}
        </p>
        <div class="mt-2 flex items-start gap-2">
          <code class="flex-1 break-all rounded bg-card px-2 py-1 font-mono text-[11px]">{{ created.token }}</code>
          <Button
            type="button"
            variant="outline"
            size="icon"
            :aria-label="t('common.copy')"
            @click="copy(created.token ?? '')"
          >
            <Copy class="h-3.5 w-3.5" aria-hidden="true" />
          </Button>
        </div>
        <p class="mt-3 text-[11px] font-medium">{{ t('systems.installCommand') }}</p>
        <div class="mt-1 flex items-start gap-2">
          <pre class="max-h-40 flex-1 overflow-auto rounded bg-card px-2 py-1 font-mono text-[10px] leading-relaxed">{{ created.install_command }}</pre>
          <Button
            type="button"
            variant="outline"
            size="icon"
            :aria-label="t('common.copy')"
            @click="copy(created.install_command ?? '')"
          >
            <Copy class="h-3.5 w-3.5" aria-hidden="true" />
          </Button>
        </div>
        <p class="mt-2 text-[11px] text-muted-foreground">{{ t('systems.tokenWarning') }}</p>
      </div>

      <div class="flex items-center justify-end gap-2 pt-1">
        <Button type="button" variant="ghost" @click="finish">{{ t('common.cancel') }}</Button>
        <!-- After a creation the button no longer submits: the record is stored
             and the only remaining action is to close the token panel. -->
        <Button v-if="created" type="button" @click="finish">{{ t('common.done') }}</Button>
        <Button v-else type="submit" :loading="store.saving">{{ t('common.save') }}</Button>
      </div>
    </form>
  </Dialog>
</template>
