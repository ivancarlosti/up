<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import AppHeader from '@/components/layout/AppHeader.vue'
import AppSidebar from '@/components/layout/AppSidebar.vue'
import Toaster from '@/components/ui/Toaster.vue'
import { useAppStore } from '@/stores/app'

const app = useAppStore()
const route = useRoute()
const mobileOpen = ref(false)

const useShell = computed(() => route.meta.app === true)

onMounted(() => {
  if (!app.settings) void app.bootstrap()
})
</script>

<template>
  <div class="min-h-screen bg-background text-foreground">
    <template v-if="useShell">
      <AppHeader :mobile-open="mobileOpen" @toggle-mobile="mobileOpen = !mobileOpen" />
      <div class="flex">
        <AppSidebar :mobile-open="mobileOpen" @navigate="mobileOpen = false" />
        <main class="min-w-0 flex-1 px-4 py-5 lg:px-6">
          <RouterView />
        </main>
      </div>
    </template>

    <main v-else class="min-h-screen px-4">
      <RouterView />
    </main>

    <Toaster />
  </div>
</template>
