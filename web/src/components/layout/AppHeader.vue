<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ChevronDown, Loader2, LogOut, Menu, UserCircle2, Wifi, WifiOff, X } from 'lucide-vue-next'
import { storeToRefs } from 'pinia'
import Button from '@/components/ui/Button.vue'
import LocaleSwitcher from '@/components/layout/LocaleSwitcher.vue'
import ThemeToggle from '@/components/layout/ThemeToggle.vue'
import { useAppStore } from '@/stores/app'
import { useMonitorStore } from '@/stores/monitors'
import { cn } from '@/lib/utils'

const props = defineProps<{ mobileOpen: boolean }>()
const emit = defineEmits<{ 'toggle-mobile': [] }>()

const { t } = useI18n()
const app = useAppStore()
const monitors = useMonitorStore()
const router = useRouter()
const { identity, authenticated, appName } = storeToRefs(app)
const { connected, state, reason, polling } = storeToRefs(monitors)
const menuOpen = ref(false)

const initial = computed(() => (identity.value?.email ?? '?').charAt(0).toUpperCase())

/**
 * Real time badge: it only claims "reconnecting" while a retry is pending and it
 * disappears while the channel was never started (idle), so the header never
 * reports a connection state that does not exist.
 */
const realtimeKey = computed(() => {
  switch (state.value) {
    case 'open':
      return 'nav.live'
    case 'connecting':
      return 'nav.connecting'
    case 'reconnecting':
      return 'nav.reconnecting'
    case 'unavailable':
      return 'nav.offline'
    default:
      return ''
  }
})

const realtimeBusy = computed(() => state.value === 'connecting' || state.value === 'reconnecting')

/** Tooltip: what the badge means plus, when known, why the channel is down. */
const realtimeHint = computed(() => {
  const parts = [t('nav.realtimeHint')]
  if (reason.value) parts.push(t(`nav.realtimeReason.${reason.value}`))
  if (polling.value) parts.push(t('nav.polling'))
  return parts.join(' - ')
})

async function signOut(): Promise<void> {
  await app.signOut()
  menuOpen.value = false
  await router.push({ name: 'login' })
}
</script>

<template>
  <header class="sticky top-0 z-30 flex h-14 items-center gap-3 border-b border-border bg-background/95 px-4 backdrop-blur">
    <Button
      class="lg:hidden"
      variant="ghost"
      size="icon"
      :aria-label="props.mobileOpen ? 'close menu' : 'open menu'"
      @click="emit('toggle-mobile')"
    >
      <X v-if="props.mobileOpen" class="h-4 w-4" aria-hidden="true" />
      <Menu v-else class="h-4 w-4" aria-hidden="true" />
    </Button>

    <RouterLink to="/" class="flex items-center gap-2">
      <img src="/logo.svg" :alt="appName" class="h-7 w-7" />
      <span class="text-sm font-semibold tracking-tight">{{ appName }}</span>
    </RouterLink>

    <span
      v-if="authenticated && realtimeKey"
      class="hidden items-center gap-1 rounded-full border px-2.5 py-1 text-[11px] sm:inline-flex"
      :class="connected ? 'border-status-up/30 text-status-up' : 'border-border text-muted-foreground'"
      :title="realtimeHint"
    >
      <Loader2 v-if="realtimeBusy" class="h-3 w-3 animate-spin" aria-hidden="true" />
      <Wifi v-else-if="connected" class="h-3 w-3" aria-hidden="true" />
      <WifiOff v-else class="h-3 w-3" aria-hidden="true" />
      {{ $t(realtimeKey) }}
    </span>

    <div class="ml-auto flex items-center gap-2">
      <LocaleSwitcher />
      <ThemeToggle />

      <div v-if="authenticated" class="relative">
        <button
          type="button"
          class="flex items-center gap-1.5 rounded-md border border-border bg-card px-2 py-1 text-xs"
          :aria-expanded="menuOpen"
          @click="menuOpen = !menuOpen"
        >
          <span class="flex h-5 w-5 items-center justify-center rounded-full bg-primary/15 text-[10px] font-semibold text-primary">
            {{ initial }}
          </span>
          <span class="hidden max-w-[12rem] truncate sm:inline">{{ identity?.email }}</span>
          <ChevronDown class="h-3 w-3" aria-hidden="true" />
        </button>

        <div
          v-if="menuOpen"
          class="absolute right-0 mt-2 w-56 rounded-lg border border-border bg-card p-1 shadow-lg"
          @mouseleave="menuOpen = false"
        >
          <div class="flex items-center gap-2 px-2 py-2 text-xs text-muted-foreground">
            <UserCircle2 class="h-4 w-4" aria-hidden="true" />
            <span class="truncate">{{ identity?.email }}</span>
          </div>
          <button
            type="button"
            class="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-xs hover:bg-accent"
            @click="signOut"
          >
            <LogOut class="h-3.5 w-3.5" aria-hidden="true" />
            {{ $t('nav.logout') }}
          </button>
        </div>
      </div>

      <Button v-else variant="outline" size="sm" :class="cn('text-xs')" @click="router.push({ name: 'login' })">
        {{ $t('nav.login') }}
      </Button>
    </div>
  </header>
</template>
