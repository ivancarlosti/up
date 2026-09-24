import { createRouter, createWebHistory } from 'vue-router'
import { useAppStore } from '@/stores/app'

/**
 * Routes of the SPA. The dashboard routes require a session unless the
 * instance runs with AUTH_METHOD=none, in which case the store reports every
 * request as authenticated.
 */
const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'dashboard', component: () => import('@/views/DashboardView.vue'), meta: { auth: true, app: true } },
    {
      path: '/monitors/:id',
      name: 'monitor-detail',
      component: () => import('@/views/MonitorDetailView.vue'),
      meta: { auth: true, app: true },
    },
    {
      path: '/admin/monitors',
      name: 'admin-monitors',
      component: () => import('@/views/admin/AdminMonitorsView.vue'),
      meta: { auth: true, app: true },
    },
    {
      path: '/admin/monitor-groups',
      name: 'admin-monitor-groups',
      component: () => import('@/views/admin/AdminMonitorGroupsView.vue'),
      meta: { auth: true, app: true },
    },
    {
      path: '/admin/monitor-templates',
      name: 'admin-monitor-templates',
      component: () => import('@/views/admin/AdminMonitorTemplatesView.vue'),
      meta: { auth: true, app: true },
    },
    {
      path: '/admin/notifications',
      name: 'admin-notifications',
      component: () => import('@/views/admin/AdminNotificationsView.vue'),
      meta: { auth: true, app: true },
    },
    {
      path: '/admin/expiry',
      name: 'admin-expiry',
      component: () => import('@/views/admin/AdminExpiryView.vue'),
      meta: { auth: true, app: true },
    },
    {
      path: '/admin/status-pages',
      name: 'admin-status-pages',
      component: () => import('@/views/admin/AdminStatusPagesView.vue'),
      meta: { auth: true, app: true },
    },
    {
      path: '/admin/security',
      name: 'admin-security',
      component: () => import('@/views/admin/AdminSecurityView.vue'),
      meta: { auth: true, app: true },
    },
    {
      path: '/admin/cluster',
      name: 'admin-cluster',
      component: () => import('@/views/admin/AdminClusterView.vue'),
      meta: { auth: true, app: true },
    },
    {
      path: '/admin/settings',
      name: 'admin-settings',
      component: () => import('@/views/admin/AdminSettingsView.vue'),
      meta: { auth: true, app: true },
    },
    {
      path: '/admin/about',
      name: 'admin-about',
      component: () => import('@/views/admin/AdminAboutView.vue'),
      meta: { auth: true, app: true },
    },
    { path: '/login', name: 'login', component: () => import('@/views/LoginView.vue') },
    { path: '/status/:slug', name: 'status-page', component: () => import('@/views/StatusPagePublicView.vue') },
    { path: '/:pathMatch(.*)*', name: 'not-found', component: () => import('@/views/NotFoundView.vue') },
  ],
  scrollBehavior: () => ({ top: 0 }),
})

router.beforeEach(async (to) => {
  const app = useAppStore()
  if (!app.settings) await app.bootstrap()

  if (to.meta.auth && !app.authenticated) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }
  return true
})

export default router
