<script setup lang="ts">
import { storeToRefs } from 'pinia'
import { AlertTriangle, CheckCircle2, Info, X } from 'lucide-vue-next'
import { useToastStore } from '@/stores/toast'
import { cn } from '@/lib/utils'

const toasts = useToastStore()
const { items } = storeToRefs(toasts)

function classesFor(type: string): string {
  return cn('flex items-start gap-3 rounded-lg border px-3 py-2 text-xs shadow-lg', {
    'border-status-up/40 bg-card': type === 'success',
    'border-status-down/40 bg-card': type === 'error',
    'border-border bg-card': type === 'info',
  })
}
</script>

<template>
  <div class="pointer-events-none fixed bottom-4 end-4 z-[60] flex w-80 flex-col gap-2">
    <div v-for="toast in items" :key="toast.id" :class="classesFor(toast.type)" class="pointer-events-auto">
      <CheckCircle2 v-if="toast.type === 'success'" class="mt-0.5 h-4 w-4 text-status-up" aria-hidden="true" />
      <AlertTriangle v-else-if="toast.type === 'error'" class="mt-0.5 h-4 w-4 text-status-down" aria-hidden="true" />
      <Info v-else class="mt-0.5 h-4 w-4 text-primary" aria-hidden="true" />

      <div class="min-w-0 flex-1">
        <p class="font-medium">{{ toast.title }}</p>
        <p v-if="toast.message" class="mt-0.5 break-words text-muted-foreground">{{ toast.message }}</p>
      </div>

      <button class="text-muted-foreground hover:text-foreground" @click="toasts.remove(toast.id)">
        <X class="h-3.5 w-3.5" aria-hidden="true" />
      </button>
    </div>
  </div>
</template>
