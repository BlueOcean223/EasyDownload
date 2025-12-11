<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useAppStore } from '@/stores/app'
import { 
  NCard, NButton, NInput, NSpace, NTag, 
  NDivider, NSwitch, useMessage, NAlert 
} from 'naive-ui'
import { 
  FolderOpenOutline, 
  ShieldCheckmarkOutline,
  ServerOutline,
  InformationCircleOutline,
  DocumentTextOutline,
  DesktopOutline,
  SaveOutline
} from '@vicons/ionicons5'
import { 
  GetBilibiliSessData, 
  SetBilibiliSessData,
  OpenLogDir,
  GetLogDir,
  GetUpstreamProxy,
  SetUpstreamProxy,
  SetUseUpstreamProxy
} from '../../wailsjs/go/main/App'

const store = useAppStore()
const message = useMessage()

const installing = ref(false)
const sessData = ref('')
const savingSessData = ref(false)
const logDir = ref('')
const upstreamProxyInput = ref('')

onMounted(async () => {
  // Load SESSDATA
  try {
    sessData.value = await GetBilibiliSessData()
  } catch (e) {
    console.error('Failed to load SESSDATA:', e)
  }
  
  // Load log directory
  try {
    logDir.value = await GetLogDir()
  } catch (e) {
    console.error('Failed to get log dir:', e)
  }
  
  // Load upstream proxy
  try {
    upstreamProxyInput.value = await GetUpstreamProxy()
  } catch (e) {
    console.error('Failed to get upstream proxy:', e)
  }
})

async function installCert() {
  installing.value = true
  try {
    await store.installCertificate()
    message.success('证书安装成功')
  } catch (e: any) {
    message.error(e.message || '证书安装失败，请以管理员身份运行')
  } finally {
    installing.value = false
  }
}

async function selectFolder() {
  try {
    const dir = await store.selectFolder()
    if (dir) {
      message.success('下载目录已更新')
    }
  } catch (e: any) {
    message.error(e.message || '选择目录失败')
  }
}

async function saveSessData() {
  savingSessData.value = true
  try {
    await SetBilibiliSessData(sessData.value)
    message.success('SESSDATA 已保存')
  } catch (e: any) {
    message.error(e.message || '保存失败')
  } finally {
    savingSessData.value = false
  }
}

async function openLogDirectory() {
  try {
    await OpenLogDir()
  } catch (e: any) {
    message.error(e.message || '打开日志目录失败')
  }
}

async function toggleMinimizeToTray(value: boolean) {
  try {
    await store.setMinimizeToTray(value)
  } catch (e: any) {
    message.error(e.message || '设置失败')
  }
}

async function toggleShowNotification(value: boolean) {
  try {
    await store.setShowNotification(value)
  } catch (e: any) {
    message.error(e.message || '设置失败')
  }
}

async function toggleUseUpstreamProxy(value: boolean) {
  try {
    await SetUseUpstreamProxy(value)
    await store.initApp() // Refresh app info
  } catch (e: any) {
    message.error(e.message || '设置失败')
  }
}

async function saveUpstreamProxy() {
  try {
    await SetUpstreamProxy(upstreamProxyInput.value)
    message.success('上游代理已保存')
  } catch (e: any) {
    message.error(e.message || '保存失败')
  }
}
</script>

<template>
  <div class="settings-page h-full overflow-auto p-4">
    <h2 class="text-xl font-semibold mb-6">设置</h2>
    
    <div class="max-w-2xl space-y-4">
      <!-- Proxy Settings -->
      <NCard title="代理服务" :bordered="false" class="bg-dark-200">
        <template #header-extra>
          <NTag :type="store.proxyRunning ? 'success' : 'default'" size="small">
            {{ store.proxyRunning ? '运行中' : '已停止' }}
          </NTag>
        </template>
        
        <div class="space-y-4">
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm font-medium">代理端口</p>
              <p class="text-xs text-gray-500">MITM 代理服务监听端口</p>
            </div>
            <span class="text-sm text-gray-400">{{ store.appInfo?.proxyPort || 8899 }}</span>
          </div>
          
          <NDivider class="my-2" />
          
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm font-medium">API 端口</p>
              <p class="text-xs text-gray-500">内部通信端口</p>
            </div>
            <span class="text-sm text-gray-400">{{ store.appInfo?.apiPort || 18899 }}</span>
          </div>
          
          <NDivider class="my-2" />
          
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm font-medium">启用上游代理</p>
              <p class="text-xs text-gray-500">将流量转发到另一个代理服务器（如 VPN 代理）</p>
            </div>
            <NSwitch 
              :value="store.useUpstreamProxy"
              @update:value="toggleUseUpstreamProxy"
            />
          </div>
          
          <div v-if="store.useUpstreamProxy">
            <p class="text-sm font-medium mb-2">上游代理地址</p>
            <div class="flex gap-2">
              <NInput 
                v-model:value="upstreamProxyInput"
                placeholder="http://127.0.0.1:7890"
                class="flex-1"
              />
              <NButton 
                type="primary" 
                size="small"
                @click="saveUpstreamProxy"
              >
                保存
              </NButton>
            </div>
          </div>
        </div>
      </NCard>
      
      <!-- Certificate Settings -->
      <NCard title="证书管理" :bordered="false" class="bg-dark-200">
        <template #header-extra>
          <NTag :type="store.certInstalled ? 'success' : 'warning'" size="small">
            {{ store.certInstalled ? '已安装' : '未安装' }}
          </NTag>
        </template>
        
        <div class="space-y-4">
          <NAlert type="info" :bordered="false">
            <template #icon>
              <ShieldCheckmarkOutline class="w-5 h-5" />
            </template>
            为了能够嗅探 HTTPS 流量，需要安装 CA 根证书到系统信任存储。此操作需要管理员权限。
          </NAlert>
          
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm font-medium">CA 证书</p>
              <p class="text-xs text-gray-500 truncate max-w-md" :title="store.appInfo?.certPath">
                {{ store.appInfo?.certPath }}
              </p>
            </div>
            <NButton 
              type="primary" 
              size="small"
              :loading="installing"
              :disabled="store.certInstalled"
              @click="installCert"
            >
              {{ store.certInstalled ? '已安装' : '安装证书' }}
            </NButton>
          </div>
        </div>
      </NCard>
      
      <!-- Download Settings -->
      <NCard title="下载设置" :bordered="false" class="bg-dark-200">
        <div class="space-y-4">
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm font-medium">下载目录</p>
              <p class="text-xs text-gray-500 truncate max-w-md" :title="store.downloadDir">
                {{ store.downloadDir }}
              </p>
            </div>
            <NButton size="small" @click="selectFolder">
              <template #icon>
                <FolderOpenOutline class="w-4 h-4" />
              </template>
              选择目录
            </NButton>
          </div>
          
          <NDivider class="my-2" />
          
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm font-medium">FFmpeg</p>
              <p class="text-xs text-gray-500">用于合并音视频流（B站高清视频需要）</p>
            </div>
            <NTag :type="store.ffmpegAvailable ? 'success' : 'warning'" size="small">
              {{ store.ffmpegAvailable ? '已安装' : '未检测到' }}
            </NTag>
          </div>
        </div>
      </NCard>
      
      <!-- Bilibili Settings -->
      <NCard title="B站设置" :bordered="false" class="bg-dark-200">
        <div class="space-y-4">
          <NAlert type="info" :bordered="false">
            <template #icon>
              <InformationCircleOutline class="w-5 h-5" />
            </template>
            登录B站账号后，在浏览器开发者工具中获取 SESSDATA Cookie，可解锁更高画质。
          </NAlert>
          
          <div>
            <p class="text-sm font-medium mb-2">SESSDATA (可选)</p>
            <div class="flex gap-2">
              <NInput 
                v-model:value="sessData"
                type="password"
                placeholder="输入 SESSDATA Cookie 以解锁高画质"
                show-password-on="click"
                class="flex-1"
              />
              <NButton 
                type="primary" 
                size="small"
                :loading="savingSessData"
                @click="saveSessData"
              >
                <template #icon>
                  <SaveOutline class="w-4 h-4" />
                </template>
                保存
              </NButton>
            </div>
          </div>
        </div>
      </NCard>
      
      <!-- Appearance Settings -->
      <NCard title="外观设置" :bordered="false" class="bg-dark-200">
        <div class="space-y-4">
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm font-medium">深色模式</p>
              <p class="text-xs text-gray-500">使用深色界面主题</p>
            </div>
            <NSwitch 
              :value="store.theme === 'dark'"
              @update:value="(v: boolean) => store.setAppTheme(v ? 'dark' : 'light')"
            />
          </div>
        </div>
      </NCard>
      
      <!-- System Tray Settings -->
      <NCard title="系统托盘" :bordered="false" class="bg-dark-200">
        <div class="space-y-4">
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm font-medium">最小化到托盘</p>
              <p class="text-xs text-gray-500">关闭窗口时最小化到系统托盘而不是退出</p>
            </div>
            <NSwitch 
              :value="store.minimizeToTray"
              @update:value="toggleMinimizeToTray"
            />
          </div>
          
          <NDivider class="my-2" />
          
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm font-medium">下载完成通知</p>
              <p class="text-xs text-gray-500">下载完成时显示系统通知</p>
            </div>
            <NSwitch 
              :value="store.showNotification"
              @update:value="toggleShowNotification"
            />
          </div>
        </div>
      </NCard>
      
      <!-- Log Settings -->
      <NCard title="日志" :bordered="false" class="bg-dark-200">
        <div class="space-y-4">
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm font-medium">日志目录</p>
              <p class="text-xs text-gray-500 truncate max-w-md" :title="logDir">
                {{ logDir }}
              </p>
            </div>
            <NButton size="small" @click="openLogDirectory">
              <template #icon>
                <DocumentTextOutline class="w-4 h-4" />
              </template>
              打开目录
            </NButton>
          </div>
        </div>
      </NCard>
      
      <!-- About -->
      <NCard title="关于" :bordered="false" class="bg-dark-200">
        <div class="space-y-2">
          <div class="flex items-center justify-between">
            <span class="text-sm text-gray-400">版本</span>
            <span class="text-sm">{{ store.appInfo?.version || '1.0.0' }}</span>
          </div>
          <div class="flex items-center justify-between">
            <span class="text-sm text-gray-400">技术栈</span>
            <span class="text-sm">Wails v2 + Vue 3 + Go</span>
          </div>
        </div>
      </NCard>
    </div>
  </div>
</template>
