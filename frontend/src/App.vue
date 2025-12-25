<script setup lang="ts">
import { onMounted, onUnmounted, ref, computed, watch } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useAppStore } from '@/stores/app'
import {
  NConfigProvider, NLayout, NLayoutSider, NMenu,
  NButton, NSpace, NTooltip, NSwitch, NSpin,
  NMessageProvider, NDialogProvider,
  darkTheme, lightTheme, zhCN, dateZhCN
} from 'naive-ui'
import type { MenuOption } from 'naive-ui'
import { 
  SearchOutline, 
  CloudDownloadOutline,
  SettingsOutline,
  PlayOutline,
  StopOutline,
  LogoTiktok,
  SunnyOutline,
  MoonOutline,
  BookOutline
} from '@vicons/ionicons5'
import { h } from 'vue'
import WelcomeWizard from '@/components/WelcomeWizard.vue'
import CloseConfirmDialog from '@/components/CloseConfirmDialog.vue'
import BilibiliIcon from '@/components/BilibiliIcon.vue'
import { EventsOn, EventsOff } from '../wailsjs/runtime/runtime'

const router = useRouter()
const route = useRoute()
const store = useAppStore()

const collapsed = ref(false)
const toggling = ref(false)
const showWelcome = ref(false)
const showCloseDialog = ref(false)

const activeKey = computed(() => route.name as string)

// Computed theme for Naive UI
const currentTheme = computed(() => store.theme === 'light' ? lightTheme : darkTheme)

// Show welcome wizard after app init if certificate is not installed
// Requirements 1.1, 1.2, 3.1: Use certInstalled instead of firstRunComplete
watch(
  () => [store.loading, store.certInstalled, store.dontRemindCertWizard] as const,
  ([loading, certInstalled, dontRemind]) => {
    if (!loading && !certInstalled && !dontRemind) {
      showWelcome.value = true
    }
  }
)

const menuOptions: MenuOption[] = [
  {
    label: '视频嗅探',
    key: 'Sniffer',
    icon: () => h(SearchOutline, { class: 'w-5 h-5' })
  },
  {
    label: 'B站下载',
    key: 'Bilibili',
    icon: () => h(BilibiliIcon, { class: 'w-5 h-5' })
  },
  {
    label: '抖音下载',
    key: 'Douyin',
    icon: () => h(LogoTiktok, { class: 'w-5 h-5' })
  },
  {
    label: '小红书下载',
    key: 'Xiaohongshu',
    icon: () => h(BookOutline, { class: 'w-5 h-5' })
  },
  {
    label: '下载管理',
    key: 'Downloads',
    icon: () => h(CloudDownloadOutline, { class: 'w-5 h-5' })
  },
  {
    label: '设置',
    key: 'Settings',
    icon: () => h(SettingsOutline, { class: 'w-5 h-5' })
  }
]

function handleMenuUpdate(key: string) {
  router.push({ name: key })
}

async function toggleProxy() {
  toggling.value = true
  try {
    await store.toggleProxy()
  } catch (e) {
    console.error('Toggle proxy error:', e)
  } finally {
    toggling.value = false
  }
}

async function toggleTheme(event?: MouseEvent) {
  const isDark = store.theme === 'dark'
  const newTheme = isDark ? 'light' : 'dark'
  const isSwitchingToDark = !isDark

  // Use View Transitions API if supported
  if (!(document as any).startViewTransition) {
    store.setAppTheme(newTheme)
    return
  }

  const transition = (document as any).startViewTransition(async () => {
    await store.setAppTheme(newTheme)
  })

  transition.ready.then(() => {
    // White -> Dark: Top-Left to Bottom-Right (Expand from 0,0)
    // Dark -> White: Bottom-Right to Top-Left (Expand from W,H)
    const x = isSwitchingToDark ? 0 : window.innerWidth
    const y = isSwitchingToDark ? 0 : window.innerHeight
    
    const endRadius = Math.hypot(
      Math.max(x, window.innerWidth - x),
      Math.max(y, window.innerHeight - y)
    )

    document.documentElement.animate(
      {
        clipPath: [
          `circle(0px at ${x}px ${y}px)`,
          `circle(${endRadius}px at ${x}px ${y}px)`,
        ],
      },
      {
        duration: 500,
        easing: 'ease-in-out',
        pseudoElement: '::view-transition-new(root)',
      }
    )
  })
}

onMounted(async () => {
  await store.initApp()
  
  // Listen for navigate:settings event from tray menu
  EventsOn('navigate:settings', () => {
    router.push({ name: 'Settings' })
  })
  
  // Listen for app:beforeClose event to show close confirmation dialog
  EventsOn('app:beforeClose', () => {
    showCloseDialog.value = true
  })
})

onUnmounted(() => {
  EventsOff('navigate:settings')
  EventsOff('app:beforeClose')
})

function handleWelcomeComplete() {
  // Requirements 3.2: Do not update firstRunComplete config
  // Certificate installation in wizard updates certInstalled config via InstallCert()
  showWelcome.value = false
}

function handleWelcomeSkip() {
  // Requirements 1.4, 3.2: Skip does not update any config
  // certInstalled remains false, wizard will show again on next startup
  showWelcome.value = false
}

async function handleWelcomeDontRemind() {
  try {
    await store.setDontRemindCertWizard(true)
  } catch (e) {
    console.error('Failed to set dontRemindCertWizard:', e)
  } finally {
    showWelcome.value = false
  }
}
</script>

<template>
  <NConfigProvider
    :theme="currentTheme"
    :locale="zhCN"
    :date-locale="dateZhCN"
  >
    <NMessageProvider>
      <NDialogProvider>
      <div class="app-container h-screen flex bg-primary text-text-primary transition-colors duration-300">
      <!-- Sidebar -->
      <div 
        class="sidebar flex flex-col bg-secondary border-r border-border transition-all duration-300"
        :class="collapsed ? 'w-16' : 'w-56'"
      >
        <!-- Logo -->
        <div class="logo-section h-14 flex items-center justify-center border-b border-border relative overflow-hidden">
          <h1 
            class="text-lg font-bold text-accent absolute transition-opacity duration-300 whitespace-nowrap"
            :class="collapsed ? 'opacity-0 pointer-events-none' : 'opacity-100'"
          >
            EasyDownload
          </h1>
          <span 
            class="text-xl font-bold text-accent absolute transition-opacity duration-300"
            :class="collapsed ? 'opacity-100' : 'opacity-0 pointer-events-none'"
          >
            ED
          </span>
        </div>
        
        <!-- Proxy Control -->
        <div class="proxy-control p-3 border-b border-border">
          <div 
            class="flex items-center gap-3 p-2 rounded-lg bg-tertiary cursor-pointer hover:bg-tertiary/80 transition-colors h-[48px]"
            :class="{ 'justify-center': collapsed }"
            @click="toggleProxy"
          >
            <div 
              class="w-8 h-8 flex-shrink-0 rounded-full flex items-center justify-center transition-colors"
              :class="store.proxyRunning ? 'bg-green-500/20 text-green-500' : 'bg-red-500/20 text-red-500'"
            >
              <NSpin v-if="toggling" :size="16" />
              <PlayOutline v-else-if="!store.proxyRunning" class="w-4 h-4" />
              <StopOutline v-else class="w-4 h-4" />
            </div>
            
            <div 
              class="flex-1 overflow-hidden whitespace-nowrap transition-all duration-300 transform"
              :class="collapsed ? 'opacity-0 translate-x-[-10px] w-0' : 'opacity-100 translate-x-0 w-auto'"
            >
              <p class="text-xs font-medium">
                {{ store.proxyRunning ? '代理运行中' : '代理已停止' }}
              </p>
              <p class="text-[10px] text-text-secondary">
                点击{{ store.proxyRunning ? '停止' : '启动' }}
              </p>
            </div>
          </div>
        </div>
        
        <!-- Navigation Menu -->
        <div class="menu-section flex-1 py-2 overflow-y-auto overflow-x-hidden">
          <NMenu
            :collapsed="collapsed"
            :collapsed-width="64"
            :collapsed-icon-size="22"
            :options="menuOptions"
            :value="activeKey"
            @update:value="handleMenuUpdate"
          />
        </div>
        
        <!-- Theme Toggle -->
        <div class="theme-toggle px-3 pt-3">
          <div 
            class="flex items-center gap-3 p-2 rounded-lg hover:bg-tertiary/50 cursor-pointer transition-colors h-[40px]"
            :class="{ 'justify-center': collapsed }"
            @click="toggleTheme"
          >
            <div class="w-8 h-8 flex-shrink-0 flex items-center justify-center text-text-secondary">
              <MoonOutline v-if="store.theme === 'dark'" class="w-5 h-5" />
              <SunnyOutline v-else class="w-5 h-5" />
            </div>
            
            <div 
              class="flex-1 overflow-hidden whitespace-nowrap transition-all duration-300 transform"
              :class="collapsed ? 'opacity-0 translate-x-[-10px] w-0' : 'opacity-100 translate-x-0 w-auto'"
            >
              <span class="text-sm font-medium text-text-primary">
                {{ store.theme === 'dark' ? '深色模式' : '浅色模式' }}
              </span>
            </div>
          </div>
        </div>

        <!-- Collapse Toggle -->
        <div class="collapse-toggle p-3">
          <NButton 
            quaternary 
            block 
            size="small"
            @click="collapsed = !collapsed"
          >
            {{ collapsed ? '>' : '<' }}
          </NButton>
        </div>
      </div>
      
      <!-- Main Content -->
      <div class="main-content flex-1 overflow-hidden bg-primary">
        <!-- Loading Overlay -->
        <div 
          v-if="store.loading" 
          class="absolute inset-0 bg-primary/80 flex items-center justify-center z-50"
        >
          <NSpin size="large" />
        </div>
        
        <!-- Router View -->
        <router-view v-slot="{ Component }">
          <transition name="fade" mode="out-in">
            <component :is="Component" />
          </transition>
        </router-view>
      </div>
      
      <!-- Welcome Wizard -->
      <WelcomeWizard
        v-model:show="showWelcome"
        @complete="handleWelcomeComplete"
        @skip="handleWelcomeSkip"
        @dont-remind="handleWelcomeDontRemind"
      />
      
      <!-- Close Confirmation Dialog -->
      <CloseConfirmDialog v-model:show="showCloseDialog" />
    </div>
      </NDialogProvider>
    </NMessageProvider>
  </NConfigProvider>
</template>

<style scoped>
.sidebar {
  --wails-draggable: drag;
}

.logo-section {
  --wails-draggable: drag;
}

.menu-section :deep(.n-menu-item) {
  margin: 4px 8px;
  border-radius: 8px;
}

.menu-section :deep(.n-menu-item-content) {
  padding-left: 12px !important;
}

.menu-section :deep(.n-menu-item-content--selected) {
  background-color: rgba(0, 220, 130, 0.1);
}

.menu-section :deep(.n-menu-item-content--selected::before) {
  border-left: 3px solid #00dc82;
}
</style>
