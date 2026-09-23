<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import AppHeader from '@/components/layout/AppHeader.vue'
import AppSidebar from '@/components/layout/AppSidebar.vue'
import Toaster from '@/components/ui/Toaster.vue'
import { useAppStore } from '@/stores/app'
import { useMonitorStore } from '@/stores/monitors'

const app = useAppStore()
const monitors = useMonitorStore()
const route = useRoute()
const mobileOpen = ref(false)

const useShell = computed(() => route.meta.app === true)

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
  <div class="flex min-h-screen flex-col bg-background text-foreground">
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
