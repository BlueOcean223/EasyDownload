<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useAppStore } from '@/stores/app'
import {
  NCard, NButton, NInput, NSpace, NTag,
  NDivider, NSwitch, useMessage, NAlert, NSelect, useDialog, NModal
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
  HasBilibiliSessData,
  SetBilibiliSessData,
  BilibiliLogout,
  OpenLogDir,
  GetLogDir,
  GetLogSize,
  ClearLogs,
  GetUpstreamProxy,
  SetUpstreamProxy,
  SetUseUpstreamProxy,
  GetProxyDebug,
  SetProxyDebug,
  GetCloseAction,
  CanRestartAsAdmin,
  IsAdmin,
  RestartAsAdmin
} from '../../wailsjs/go/main/App'
import { getErrorMessage, isPermissionError } from '@/utils/errors'

const store = useAppStore()
const message = useMessage()
const dialog = useDialog()

const installing = ref(false)
const uninstalling = ref(false)
const installingFFmpeg = ref(false)
const sessData = ref('')  // Only for new input, not loaded from storage
const hasSessData = ref(false)  // Whether SESSDATA is already set
const savingSessData = ref(false)
const resettingSessData = ref(false)
const logDir = ref('')
const logSize = ref(0)
const clearingLogs = ref(false)
const upstreamProxyInput = ref('')
const proxyDebug = ref(false)
const closeAction = ref<'' | 'exit' | 'minimize'>('')
const showAdminDialog = ref(false)
const restarting = ref(false)
const adminDialogAction = ref<'install' | 'uninstall'>('install')

const closeActionOptions = [
  { label: '每次询问', value: '' },
  { label: '最小化到托盘', value: 'minimize' },
  { label: '退出应用', value: 'exit' }
]

onMounted(async () => {
  // Check if SESSDATA is set (without exposing the actual value for security)
  try {
    hasSessData.value = await HasBilibiliSessData()
  } catch (e) {
    console.error('Failed to check SESSDATA status:', e)
  }
  
  // Load log directory
  try {
    logDir.value = await GetLogDir()
  } catch (e) {
    console.error('Failed to get log dir:', e)
  }

  // Load log size
  try {
    logSize.value = await GetLogSize()
  } catch (e) {
    console.error('Failed to get log size:', e)
  }
  
  // Load upstream proxy
  try {
    upstreamProxyInput.value = await GetUpstreamProxy()
  } catch (e) {
    console.error('Failed to get upstream proxy:', e)
  }

  // Load diagnostics toggles
  try {
    proxyDebug.value = await GetProxyDebug()
  } catch (e) {
    console.error('Failed to get proxy debug:', e)
  }
  
  // Load close action
  try {
    closeAction.value = await GetCloseAction() as '' | 'exit' | 'minimize'
  } catch (e) {
    console.error('Failed to get close action:', e)
  }
})

async function installCert() {
  installing.value = true
  try {
    await store.installCertificate()
    message.success('证书安装成功')
  } catch (e: any) {
    const errorMsg = getErrorMessage(e, '证书安装失败')
    const permissionError = isPermissionError(errorMsg)
    let isAdmin = false
    try {
      isAdmin = await IsAdmin()
    } catch {
      message.error(errorMsg)
      return
    }

    if (permissionError && !isAdmin) {
      let canRestart = false
      try {
        canRestart = await CanRestartAsAdmin()
      } catch {
        message.error(errorMsg)
        return
      }
      if (canRestart) {
        adminDialogAction.value = 'install'
        showAdminDialog.value = true
        return
      }
    }

    message.error(errorMsg)
  } finally {
    installing.value = false
  }
}

async function uninstallCert() {
  uninstalling.value = true
  try {
    await store.uninstallCertificate()
    message.success('证书卸载成功')
  } catch (e: any) {
    const errorMsg = getErrorMessage(e, '证书卸载失败')
    const permissionError = isPermissionError(errorMsg)
    let isAdmin = false
    try {
      isAdmin = await IsAdmin()
    } catch {
      message.error(errorMsg)
      return
    }

    if (permissionError && !isAdmin) {
      let canRestart = false
      try {
        canRestart = await CanRestartAsAdmin()
      } catch {
        message.error(errorMsg)
        return
      }
      if (canRestart) {
        adminDialogAction.value = 'uninstall'
        showAdminDialog.value = true
        return
      }
    }

    message.error(errorMsg)
  } finally {
    uninstalling.value = false
  }
}

async function installFFmpeg() {
  installingFFmpeg.value = true
  try {
    const path = await store.installFFmpeg()
    await store.initApp()
    message.success(path ? `FFmpeg 已就绪: ${path}` : 'FFmpeg 已就绪')
  } catch (e: any) {
    message.error(getErrorMessage(e, 'FFmpeg 安装失败'))
  } finally {
    installingFFmpeg.value = false
  }
}

async function restartAsAdmin() {
  restarting.value = true
  try {
    await RestartAsAdmin()
    // App will quit automatically after launching elevated process
  } catch (e: any) {
    message.error('无法以管理员身份重启: ' + getErrorMessage(e, '未知错误'))
    showAdminDialog.value = false
  } finally {
    restarting.value = false
  }
}

function cancelAdminRestart() {
  showAdminDialog.value = false
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
  if (!sessData.value.trim()) {
    message.warning('请输入 SESSDATA')
    return
  }
  savingSessData.value = true
  try {
    await SetBilibiliSessData(sessData.value)
    hasSessData.value = true  // Update status after successful save
    sessData.value = ''  // Clear input for security
    message.success('SESSDATA 已保存到安全存储')
  } catch (e: any) {
    message.error(e.message || '保存失败')
  } finally {
    savingSessData.value = false
  }
}

async function resetSessData() {
  resettingSessData.value = true
  try {
    await BilibiliLogout()
    hasSessData.value = false
    sessData.value = ''
    message.success('SESSDATA 已清除')
  } catch (e: any) {
    message.error(e.message || '清除失败')
  } finally {
    resettingSessData.value = false
  }
}

async function openLogDirectory() {
  try {
    await OpenLogDir()
  } catch (e: any) {
    message.error(e.message || '打开日志目录失败')
  }
}

async function toggleShowNotification(value: boolean) {
  try {
    await store.setShowNotification(value)
  } catch (e: any) {
    message.error(e.message || '设置失败')
  }
}

async function handleCloseActionChange(action: '' | 'exit' | 'minimize') {
  try {
    await store.setCloseAction(action)
    closeAction.value = action
    // If user selects a specific action, also set dontAskOnClose
    await store.setDontAskOnClose(action !== '')
    message.success('关闭行为已更新')
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

async function toggleProxyDebug(value: boolean) {
  try {
    await SetProxyDebug(value)
    proxyDebug.value = value
    message.success(value ? '代理调试已开启（请复现一次问题后导出日志）' : '代理调试已关闭')
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

function formatFileSize(bytes: number): string {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i]
}

async function clearLogs() {
  dialog.warning({
    title: '确认清空日志',
    content: '此操作将删除所有日志文件，无法恢复。确定要继续吗？',
    positiveText: '确定清空',
    negativeText: '取消',
    onPositiveClick: async () => {
      clearingLogs.value = true
      try {
        await ClearLogs()
        logSize.value = await GetLogSize()
        message.success('日志已清空')
      } catch (e: any) {
        message.error(e.message || '清空日志失败')
      } finally {
        clearingLogs.value = false
      }
    }
  })
}
</script>

<template>
  <div class="settings-page h-full overflow-auto p-4 bg-primary text-text-primary">
    <h2 class="text-xl font-semibold mb-6">设置</h2>
    
    <div class="max-w-2xl space-y-4">
      <!-- Proxy Settings -->
      <NCard title="代理服务" :bordered="false" class="bg-secondary rounded-xl shadow-sm">
        <template #header-extra>
          <NTag :type="store.proxyRunning ? 'success' : 'default'" size="small">
            {{ store.proxyRunning ? '运行中' : '已停止' }}
          </NTag>
        </template>
        
        <div class="space-y-4">
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm font-medium">代理端口</p>
              <p class="text-xs text-text-secondary">MITM 代理服务监听端口</p>
            </div>
            <span class="text-sm text-text-secondary">{{ store.appInfo?.proxyPort || 8899 }}</span>
          </div>
          
          <NDivider class="my-2" />
          
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm font-medium">API 端口</p>
              <p class="text-xs text-text-secondary">内部通信端口</p>
            </div>
            <span class="text-sm text-text-secondary">{{ store.appInfo?.apiPort || 18899 }}</span>
          </div>
          
          <NDivider class="my-2" />
          
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm font-medium">启用上游代理</p>
              <p class="text-xs text-text-secondary">将流量转发到另一个代理服务器（如 VPN 代理）</p>
            </div>
            <NSwitch 
              :value="store.useUpstreamProxy"
              @update:value="toggleUseUpstreamProxy"
            />
          </div>

          <NDivider class="my-2" />

          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm font-medium">代理调试（诊断）</p>
              <p class="text-xs text-text-secondary">记录 CONNECT/编码/改写信息到日志，用于定位视频号卡加载</p>
            </div>
            <NSwitch :value="proxyDebug" @update:value="toggleProxyDebug" />
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
      <NCard title="证书管理" :bordered="false" class="bg-secondary rounded-xl shadow-sm">
        <template #header-extra>
          <NTag :type="store.certInstalled ? 'success' : 'warning'" size="small">
            {{ store.certInstalled ? '已安装' : '未安装' }}
          </NTag>
        </template>
        
        <div class="space-y-4">
          <NAlert type="info" :bordered="false" class="rounded-lg">
            <template #icon>
              <ShieldCheckmarkOutline class="w-5 h-5" />
            </template>
            为了能够嗅探 HTTPS 流量，需要安装 CA 根证书到系统信任存储。Windows 下通常需要管理员权限；macOS 下会弹出系统授权提示。
          </NAlert>
          
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm font-medium">CA 证书</p>
              <p class="text-xs text-text-secondary truncate max-w-md" :title="store.appInfo?.certPath">
                {{ store.appInfo?.certPath }}
              </p>
            </div>
            <NSpace>
              <NButton 
                type="primary" 
                size="small"
                :loading="installing"
                :disabled="store.certInstalled"
                @click="installCert"
              >
                {{ store.certInstalled ? '已安装' : '安装证书' }}
              </NButton>
              <NButton 
                type="error" 
                size="small"
                :loading="uninstalling"
                :disabled="!store.certInstalled"
                @click="uninstallCert"
              >
                卸载证书
              </NButton>
            </NSpace>
          </div>
        </div>
      </NCard>
      
      <!-- Download Settings -->
      <NCard title="下载设置" :bordered="false" class="bg-secondary rounded-xl shadow-sm">
        <div class="space-y-4">
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm font-medium">下载目录</p>
              <p class="text-xs text-text-secondary truncate max-w-md" :title="store.downloadDir">
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
              <p class="text-xs text-text-secondary">用于合并音视频流（B站高清视频需要）</p>
              <p
                v-if="store.appInfo?.ffmpegPath"
                class="text-xs text-text-secondary truncate max-w-md"
                :title="store.appInfo?.ffmpegPath"
              >
                {{ store.appInfo?.ffmpegPath }}
              </p>
            </div>
            <NSpace align="center">
              <NTag :type="store.ffmpegAvailable ? 'success' : 'warning'" size="small">
                {{ store.ffmpegAvailable ? '已安装' : '未检测到' }}
              </NTag>
              <NButton
                size="small"
                type="primary"
                secondary
                :loading="installingFFmpeg"
                :disabled="store.ffmpegAvailable"
                @click="installFFmpeg"
              >
                {{ store.ffmpegAvailable ? '已就绪' : '安装 FFmpeg' }}
              </NButton>
            </NSpace>
          </div>

          <NAlert v-if="!store.ffmpegAvailable" type="info" :bordered="false" class="rounded-lg">
            macOS 下会优先使用应用内置或系统现有的 FFmpeg；若都不存在，程序会尝试自动安装。
          </NAlert>
        </div>
      </NCard>
      
      <!-- Bilibili Settings -->
      <NCard title="B站设置" :bordered="false" class="bg-secondary rounded-xl shadow-sm">
        <div class="space-y-4">
          <NAlert type="info" :bordered="false" class="rounded-lg">
            <template #icon>
              <InformationCircleOutline class="w-5 h-5" />
            </template>
            <div>
              <p>B站对未登录用户限制画质（最高480P），这是B站官方策略。</p>
              <p class="mt-1">获取 SESSDATA 的方式：</p>
              <ul class="list-disc list-inside mt-1 text-xs">
                <li>推荐：在「B站下载」页面点击右上角登录按钮，扫码自动获取</li>
                <li>手动：在浏览器登录B站后，通过开发者工具获取 Cookie 中的 SESSDATA</li>
              </ul>
            </div>
          </NAlert>
          
          <div>
            <p class="text-sm font-medium mb-2">
              SESSDATA (可选)
              <NTag v-if="hasSessData" type="success" size="small" class="ml-2">已设置</NTag>
              <NTag v-else type="default" size="small" class="ml-2">未设置</NTag>
            </p>
            <div class="flex gap-2">
              <NInput
                v-model:value="sessData"
                type="password"
                :placeholder="hasSessData ? '输入新的 SESSDATA 以覆盖' : '输入 SESSDATA Cookie 以解锁高画质'"
                show-password-on="click"
                autocomplete="off"
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
              <NButton
                v-if="hasSessData"
                type="error"
                size="small"
                :loading="resettingSessData"
                @click="resetSessData"
              >
                重置
              </NButton>
            </div>
            <p class="text-xs text-text-tertiary mt-2">
              提示：扫码登录后会自动填充此项，无需手动输入。凭据已安全存储在系统凭据管理器中。
            </p>
          </div>
        </div>
      </NCard>
      

      <!-- System Tray Settings -->
      <NCard title="系统托盘" :bordered="false" class="bg-secondary rounded-xl shadow-sm">
        <div class="space-y-4">
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm font-medium">关闭窗口时的行为</p>
              <p class="text-xs text-text-secondary">选择关闭窗口时的默认操作</p>
            </div>
            <NSelect
              :value="closeAction"
              :options="closeActionOptions"
              style="width: 160px"
              @update:value="handleCloseActionChange"
            />
          </div>
          
          <NDivider class="my-2" />
          
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm font-medium">下载完成通知</p>
              <p class="text-xs text-text-secondary">下载完成时显示系统通知</p>
            </div>
            <NSwitch 
              :value="store.showNotification"
              @update:value="toggleShowNotification"
            />
          </div>
        </div>
      </NCard>
      
      <!-- Log Settings -->
      <NCard title="日志" :bordered="false" class="bg-secondary rounded-xl shadow-sm">
        <div class="space-y-4">
          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm font-medium">日志目录</p>
              <p class="text-xs text-text-secondary truncate max-w-md" :title="logDir">
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

          <NDivider class="my-2" />

          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm font-medium">日志文件大小</p>
              <p class="text-xs text-text-secondary">当前日志文件占用空间</p>
            </div>
            <span class="text-sm text-text-secondary">{{ formatFileSize(logSize) }}</span>
          </div>

          <NDivider class="my-2" />

          <div class="flex items-center justify-between">
            <div>
              <p class="text-sm font-medium">清空日志</p>
              <p class="text-xs text-text-secondary">删除所有日志文件以释放空间</p>
            </div>
            <NButton
              type="error"
              size="small"
              :loading="clearingLogs"
              @click="clearLogs"
            >
              清空日志
            </NButton>
          </div>
        </div>
      </NCard>
      
      <!-- About -->
      <NCard title="关于" :bordered="false" class="bg-secondary rounded-xl shadow-sm">
        <div class="space-y-2">
          <div class="flex items-center justify-between">
            <span class="text-sm text-text-secondary">版本</span>
            <span class="text-sm">{{ store.appInfo?.version || '1.0.0' }}</span>
          </div>
          <div class="flex items-center justify-between">
            <span class="text-sm text-text-secondary">技术栈</span>
            <span class="text-sm">Wails v2 + Vue 3 + Go</span>
          </div>
        </div>
      </NCard>
    </div>

    <!-- Admin Restart Dialog -->
    <NModal
      v-model:show="showAdminDialog"
      preset="dialog"
      title="需要管理员权限"
      positive-text="以管理员身份重启"
      negative-text="取消"
      :positive-button-props="{ loading: restarting }"
      @positive-click="restartAsAdmin"
      @negative-click="cancelAdminRestart"
    >
      <p>{{ adminDialogAction === 'install' ? '安装' : '卸载' }}证书需要管理员权限，是否以管理员身份重启？</p>
      <p class="text-text-secondary text-sm mt-2">
        系统会弹出 UAC 提示。
      </p>
    </NModal>
  </div>
</template>
