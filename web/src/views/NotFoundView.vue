<script setup lang="ts">
import { useRouter } from 'vue-router'
import Button from '@/components/ui/Button.vue'
import EmptyState from '@/components/ui/EmptyState.vue'

const router = useRouter()
</script>

<template>
  <div class="py-16">
    <EmptyState title="404" description="ERR_ROUTE_NOT_FOUND">
      <Button variant="outline" size="sm" @click="router.push({ name: 'dashboard' })">
        {{ $t('login.goToDashboard') }}
      </Button>
    </EmptyState>
  </div>
</template>
