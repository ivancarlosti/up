<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { BookOpen, Github, Globe2, HeartPulse, Network, ShieldCheck } from 'lucide-vue-next'
import Card from '@/components/ui/Card.vue'
import { useAppStore } from '@/stores/app'

const { t } = useI18n()
const app = useAppStore()
const info = ref({ version: 'dev', commit: 'unknown' })

const features = [
  { icon: HeartPulse, key: 'monitors' },
  { icon: Network, key: 'cluster' },
  { icon: ShieldCheck, key: 'security' },
  { icon: Globe2, key: 'statusPages' },
]

onMounted(async () => {
  try {
    info.value = await fetch('/api/version').then((response) => response.json())
  } catch {
    /* the version endpoint is public and optional here */
  }
})
</script>

<template>
  <div class="flex flex-col gap-4">
    <header class="flex items-center gap-3">
      <img src="/logo.svg" alt="Up" class="h-10 w-10" />
      <div>
        <h1 class="text-lg font-semibold">{{ t('about.title') }}</h1>
        <p class="text-xs text-muted-foreground">{{ t('app.tagline') }}</p>
      </div>
    </header>

    <Card>
      <p class="text-sm">{{ t('about.description') }}</p>
      <dl class="mt-4 grid gap-3 text-xs sm:grid-cols-2">
        <div>
          <dt class="text-muted-foreground">{{ t('app.version') }}</dt>
          <dd class="mt-0.5 font-mono">{{ info.version }} ({{ info.commit }})</dd>
        </div>
        <div>
          <dt class="text-muted-foreground">{{ t('app.instance') }}</dt>
          <dd class="mt-0.5 font-mono">{{ app.settings?.node_id }}</dd>
        </div>
        <div>
          <dt class="text-muted-foreground">{{ t('about.license') }}</dt>
          <dd class="mt-0.5">
            <a class="text-primary hover:underline" href="https://github.com/ivancarlosti/up/blob/main/LICENSE" target="_blank" rel="noreferrer">
              MIT
            </a>
          </dd>
        </div>
        <div>
          <dt class="text-muted-foreground">{{ t('about.repository') }}</dt>
          <dd class="mt-0.5">
            <a class="inline-flex items-center gap-1 text-primary hover:underline" href="https://github.com/ivancarlosti/up" target="_blank" rel="noreferrer">
              <Github class="h-3.5 w-3.5" aria-hidden="true" />
              ivancarlosti/up
            </a>
          </dd>
        </div>
        <div class="sm:col-span-2">
          <dt class="text-muted-foreground">{{ t('about.documentation') }}</dt>
          <dd class="mt-0.5">
            <a class="inline-flex items-center gap-1 text-primary hover:underline" href="https://github.com/ivancarlosti/up/tree/main/docs" target="_blank" rel="noreferrer">
              <BookOpen class="h-3.5 w-3.5" aria-hidden="true" />
              /docs
            </a>
          </dd>
        </div>
      </dl>
    </Card>

    <Card :title="t('about.features')">
      <ul class="grid gap-2 text-xs sm:grid-cols-2">
        <li v-for="feature in features" :key="feature.key" class="flex items-center gap-2">
          <component :is="feature.icon" class="h-4 w-4 text-primary" aria-hidden="true" />
          {{ t(`nav.${feature.key}`) }}
        </li>
      </ul>
    </Card>
  </div>
</template>
