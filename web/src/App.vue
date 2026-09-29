<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AppHeader from '@/components/layout/AppHeader.vue'
import AppSidebar from '@/components/layout/AppSidebar.vue'
import Alert from '@/components/ui/Alert.vue'
import Button from '@/components/ui/Button.vue'
import Toaster from '@/components/ui/Toaster.vue'
import BootView from '@/views/BootView.vue'
import { translateCode } from '@/lib/errors'
import { useAppStore } from '@/stores/app'
import { useMonitorStore } from '@/stores/monitors'

const app = useAppStore()
const monitors = useMonitorStore()
const route = useRoute()
const { t } = useI18n()
const mobileOpen = ref(false)

const useShell = computed(() => route.meta.app === true)

/**
 * The degraded banner reports a failure that happened while the API was already
 * serving (the database went away, the deferred rollup rebuild failed). It is
 * dismissed per failure code: closing it keeps it closed until a different
 * problem shows up, so a running instance does not nag.
 */
const dismissedCode = ref('')
const degradedCode = computed(() => (app.degraded ? app.boot?.code ?? '' : ''))
const showBanner = computed(() => degradedCode.value !== '' && degradedCode.value !== dismissedCode.value)
watch(degradedCode, (code) => {
  if (code === '') dismissedCode.value = ''
})

onMounted(() => {
  if (!app.settings) void app.bootstrap()
})

// The real time channel belongs to the shell, not to a single view: while it was
// opened by the dashboard, leaving that page closed the socket and the "Live"
// badge of the header reported a reconnection that was not happening. It now
// follows the session and stays open on every page (the detail view benefits
// from the heartbeats pushed through it as well).
watch(
  () => app.authenticated,
  (authenticated) => {
    if (authenticated) monitors.connect()
    else monitors.disconnect()
  },
  { immediate: true },
)
</script>

<template>
  <!-- The backend is preparing its database (or cannot reach it): the boot
       screen owns the window, because no route could render anything useful. -->
  <BootView v-if="app.bootPending" />

  <div v-else class="flex min-h-screen flex-col bg-background text-foreground">
    <!-- Phase 4: the API answers, but it lost something. -->
    <Alert v-if="showBanner" variant="warning" class="mx-5 mt-4 flex-wrap lg:mx-8">
      <div class="flex min-w-0 flex-col gap-1">
        <p class="text-sm font-medium">{{ t('boot.degradedTitle') }}</p>
        <p>{{ translateCode(degradedCode, app.boot?.detail ?? '') }}</p>
        <p v-if="app.boot?.detail" class="font-mono text-[11px] break-words opacity-80">
          {{ app.boot.detail }}
        </p>
      </div>
      <Button variant="ghost" size="sm" @click="dismissedCode = degradedCode">
        {{ t('boot.dismiss') }}
      </Button>
    </Alert>

    <template v-if="useShell">
      <AppHeader :mobile-open="mobileOpen" @toggle-mobile="mobileOpen = !mobileOpen" />
      <!-- The shell is a full height column: the row below the header grows to fill
           the rest of the window (never less than its content), which is what keeps
           the sidebar background and its right border reaching the bottom of the
           window on pages with little content. -->
      <div class="flex flex-1">
        <AppSidebar :mobile-open="mobileOpen" @navigate="mobileOpen = false" />
        <main class="min-w-0 flex-1 px-5 py-6 lg:px-8 lg:py-7">
          <div class="mx-auto w-full max-w-[1600px]">
            <RouterView />
          </div>
        </main>
      </div>
    </template>

    <main v-else class="min-h-screen px-5 py-6">
      <div class="mx-auto w-full max-w-[1600px]">
        <RouterView />
      </div>
    </main>

    <Toaster />
  </div>
</template>

