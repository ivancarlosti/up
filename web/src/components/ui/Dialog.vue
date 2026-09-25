<script setup lang="ts">
import { DialogClose, DialogContent, DialogDescription, DialogOverlay, DialogPortal, DialogRoot, DialogTitle } from 'reka-ui'
import { X } from 'lucide-vue-next'
import Button from '@/components/ui/Button.vue'

const props = withDefaults(
  defineProps<{ modelValue: boolean; title: string; description?: string; wide?: boolean }>(),
  { description: '', wide: false },
)
const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()
</script>

<template>
  <DialogRoot :open="props.modelValue" @update:open="emit('update:modelValue', $event)">
    <DialogPortal>
      <DialogOverlay class="fixed inset-0 z-40 bg-black/60 backdrop-blur-sm" />
      <DialogContent
        class="fixed start-1/2 top-1/2 z-50 max-h-[92vh] w-[calc(100vw-2rem)] ltr:-translate-x-1/2 rtl:translate-x-1/2 -translate-y-1/2 overflow-y-auto rounded-xl border border-border bg-card p-5 shadow-xl"
        :class="props.wide ? 'max-w-4xl' : 'max-w-xl'"
      >
        <div class="mb-4 flex items-start justify-between gap-4">
          <div>
            <DialogTitle class="text-base font-semibold">{{ props.title }}</DialogTitle>
            <DialogDescription v-if="props.description" class="mt-1 text-xs text-muted-foreground">
              {{ props.description }}
            </DialogDescription>
          </div>
          <DialogClose as-child>
            <Button variant="ghost" size="icon" :aria-label="$t('common.close')">
              <X class="h-4 w-4" aria-hidden="true" />
            </Button>
          </DialogClose>
        </div>

        <slot />

        <div v-if="$slots.footer" class="mt-5 flex items-center justify-end gap-2">
          <slot name="footer" />
        </div>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>
