<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Button from '@/components/ui/Button.vue'
import Dialog from '@/components/ui/Dialog.vue'

const props = withDefaults(
  defineProps<{ modelValue: boolean; title: string; description?: string; confirmLabel?: string; loading?: boolean }>(),
  { description: '', confirmLabel: '', loading: false },
)
const emit = defineEmits<{ 'update:modelValue': [value: boolean]; confirm: [] }>()
const { t } = useI18n()
</script>

<template>
  <Dialog
    :model-value="props.modelValue"
    :title="props.title"
    :description="props.description"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <slot />

    <template #footer>
      <Button variant="outline" @click="emit('update:modelValue', false)">{{ t('common.cancel') }}</Button>
      <Button variant="destructive" :loading="props.loading" @click="emit('confirm')">
        {{ props.confirmLabel || t('common.confirm') }}
      </Button>
    </template>
  </Dialog>
</template>
