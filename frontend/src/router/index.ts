import { createRouter, createWebHashHistory, RouteRecordRaw } from 'vue-router'

const routes: RouteRecordRaw[] = [
  {
    path: '/',
    redirect: '/sniffer'
  },
  {
    path: '/sniffer',
    name: 'Sniffer',
    component: () => import('@/views/Sniffer.vue'),
    meta: { title: '视频嗅探' }
  },
  {
    path: '/bilibili',
    name: 'Bilibili',
    component: () => import('@/views/Bilibili.vue'),
    meta: { title: 'B站下载' }
  },
  {
    path: '/downloads',
    name: 'Downloads',
    component: () => import('@/views/Downloads.vue'),
    meta: { title: '下载管理' }
  },
  {
    path: '/settings',
    name: 'Settings',
    component: () => import('@/views/Settings.vue'),
    meta: { title: '设置' }
  }
]

const router = createRouter({
  history: createWebHashHistory(),
  routes
})

export default router

