import { defineStore } from 'pinia'
import { ref } from 'vue'

export interface Toast {
  id: number
  type: 'success' | 'error' | 'info'
  title: string
  message?: string
}

let sequence = 0

/**
 * useToastStore renders the transient notifications of the UI (saved, deleted,
 * request failed...). It is intentionally tiny: no external dependency.
 */
export const useToastStore = defineStore('toast', () => {
  const items = ref<Toast[]>([])

  function push(toast: Omit<Toast, 'id'>): void {
    const id = ++sequence
    items.value.push({ ...toast, id })
    setTimeout(() => remove(id), toast.type === 'error' ? 8000 : 4000)
  }

  function success(title: string, message?: string): void {
    push({ type: 'success', title, message })
  }

  function error(title: string, message?: string): void {
    push({ type: 'error', title, message })
  }

  function info(title: string, message?: string): void {
    push({ type: 'info', title, message })
  }

  function remove(id: number): void {
    items.value = items.value.filter((item) => item.id !== id)
  }

  return { items, push, success, error, info, remove }
})
