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
    /**
     * The monitors page was absorbed by the dashboard (2026-09-27): the old path
     * stays as a redirect so a bookmark, the browser history and the e2e scripts
     * that land on it keep working instead of hitting "not found".
     */
    { path: '/admin/monitors', redirect: { name: 'dashboard' } },
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

  // The backend is starting (or its database is down): park this navigation
  // until it answers. The shell shows the boot screen meanwhile, so the visitor
  // keeps the URL they asked for instead of landing on the login page of an API
  // that cannot serve anything yet.
  if (app.bootPending) await app.waitForBoot()

  if (to.meta.auth && !app.authenticated) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }
  return true
})

export default router
