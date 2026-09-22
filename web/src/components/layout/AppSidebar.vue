<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import {
  Bell,
  Boxes,
  Info,
  LayoutDashboard,
  Network,
  ShieldCheck,
  SlidersHorizontal,
  Globe,
} from 'lucide-vue-next'
import { storeToRefs } from 'pinia'
import { useAppStore } from '@/stores/app'
import { cn } from '@/lib/utils'

const props = defineProps<{ mobileOpen: boolean }>()
defineEmits<{ navigate: [] }>()

const app = useAppStore()
const { settings } = storeToRefs(app)
const route = useRoute()

const items = computed(() => [
  { name: 'dashboard', to: { name: 'dashboard' }, icon: LayoutDashboard, label: 'nav.dashboard' },
  { name: 'monitors', to: { name: 'admin-monitors' }, icon: Boxes, label: 'nav.monitors' },
  { name: 'notifications', to: { name: 'admin-notifications' }, icon: Bell, label: 'nav.notifications' },
  { name: 'status-pages', to: { name: 'admin-status-pages' }, icon: Globe, label: 'nav.statusPages' },
  { name: 'security', to: { name: 'admin-security' }, icon: ShieldCheck, label: 'nav.security' },
  { name: 'cluster', to: { name: 'admin-cluster' }, icon: Network, label: 'nav.cluster' },
  { name: 'settings', to: { name: 'admin-settings' }, icon: SlidersHorizontal, label: 'nav.settings' },
  { name: 'about', to: { name: 'admin-about' }, icon: Info, label: 'nav.about' },
])

function isActive(name: string): boolean {
  return route.name === name
}
</script>

<template>
  <aside
    :class="
      cn(
        'fixed inset-y-14 left-0 z-20 w-60 shrink-0 overflow-y-auto border-r border-border bg-card px-3 py-4 transition-transform lg:static lg:translate-x-0',
        props.mobileOpen ? 'translate-x-0' : '-translate-x-full',
      )
    "
  >
    <nav class="flex flex-col gap-1">
      <RouterLink
        v-for="item in items"
        :key="item.name"
        :to="item.to"
        :class="
          cn(
            'flex items-center gap-2.5 rounded-md px-3 py-2.5 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-foreground',
            isActive(item.name) && 'bg-accent font-medium text-foreground',
          )
        "
        @click="$emit('navigate')"
      >
        <component :is="item.icon" class="h-4 w-4" aria-hidden="true" />
        {{ $t(item.label) }}
      </RouterLink>
    </nav>

    <div class="mt-4 rounded-lg border border-border/60 bg-background/40 p-2.5 text-[11px] text-muted-foreground">
      <p class="truncate">{{ settings?.node_name || settings?.node_id }}</p>
      <p class="mt-1 flex items-center gap-1">
        <span class="truncate">{{ $t('app.version') }} {{ settings?.version }}</span>
      </p>
    </div>
  </aside>
</template>
