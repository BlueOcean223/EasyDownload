<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch } from 'vue'
import { NModal, NButton, NSpin, NTag, NSpace, useMessage } from 'naive-ui'
import { LogOutOutline, RefreshOutline, CheckmarkCircleOutline, PersonCircleOutline } from '@vicons/ionicons5'
import type { BilibiliUserInfo, BilibiliQRCode } from '@/types'
import {
  GetBilibiliQRCode,
  PollBilibiliQRCode,
  GetBilibiliUserInfo,
  BilibiliLogout,
  HasBilibiliSessData
} from '../../wailsjs/go/main/App'
import QRCode from 'qrcode'
import ProxiedImage from '@/components/ProxiedImage.vue'

const props = defineProps<{
  show: boolean
}>()

const emit = defineEmits<{
  (e: 'update:show', value: boolean): void
  (e: 'login', userInfo: BilibiliUserInfo): void
  (e: 'logout'): void
}>()

const message = useMessage()
const loading = ref(false)
const qrCodeData = ref<BilibiliQRCode | null>(null)
const qrCodeImage = ref('')
const userInfo = ref<BilibiliUserInfo | null>(null)
const pollTimer = ref<number | null>(null)
const qrStatus = ref<'loading' | 'ready' | 'scanned' | 'expired' | 'success'>('loading')
const logoutSuccess = ref(false)

// Check if already logged in
async function checkLoginStatus() {
  try {
    // Use HasBilibiliSessData instead of GetBilibiliSessData for security
    // This only checks if credentials exist without exposing the actual value
    const hasSessData = await HasBilibiliSessData()
    if (hasSessData) {
      const info = await GetBilibiliUserInfo()
      if (info && info.isLogin) {
        userInfo.value = info
        return true
      }
    }
  } catch (e) {
    console.error('Failed to check login status:', e)
  }
  return false
}

// Generate QR code
async function generateQRCode() {
  loading.value = true
  qrStatus.value = 'loading'
  qrCodeImage.value = ''
  logoutSuccess.value = false
  
  try {
    const qr = await GetBilibiliQRCode()
    qrCodeData.value = qr
    
    // Generate QR code image
    qrCodeImage.value = await QRCode.toDataURL(qr.url, {
      width: 200,
      margin: 2,
      color: {
        dark: '#000000',
        light: '#ffffff'
      }
    })
    
    qrStatus.value = 'ready'
    
    // Start polling
    startPolling()
  } catch (e: any) {
    message.error(e.message || '生成二维码失败')
    qrStatus.value = 'expired'
  } finally {
    loading.value = false
  }
}

// Poll QR code status
function startPolling() {
  stopPolling()
  
  pollTimer.value = window.setInterval(async () => {
    if (!qrCodeData.value) return
    
    try {
      const status = await PollBilibiliQRCode(qrCodeData.value.qrcodeKey)
      
      switch (status.code) {
        case 0: // Success
          qrStatus.value = 'success'
          stopPolling()
          message.success('登录成功')
          // Fetch user info
          const info = await GetBilibiliUserInfo()
          userInfo.value = info
          emit('login', info)
          break
        case 86090: // Scanned, waiting confirm
          qrStatus.value = 'scanned'
          break
        case 86038: // Expired
          qrStatus.value = 'expired'
          stopPolling()
          break
        case 86101: // Not scanned
          // Keep waiting
          break
      }
    } catch (e) {
      console.error('Poll error:', e)
    }
  }, 2000)
}

function stopPolling() {
  if (pollTimer.value) {
    clearInterval(pollTimer.value)
    pollTimer.value = null
  }
}

async function handleLogout() {
  try {
    await BilibiliLogout()
    userInfo.value = null
    logoutSuccess.value = true
    emit('logout')
  } catch (e: any) {
    message.error(e.message || '退出失败')
  }
}

function close() {
  stopPolling()
  emit('update:show', false)
}

// Watch show prop
watch(() => props.show, async (newVal) => {
  if (newVal) {
    logoutSuccess.value = false
    const isLoggedIn = await checkLoginStatus()
    if (!isLoggedIn) {
      generateQRCode()
    }
  } else {
    stopPolling()
  }
})

onMounted(async () => {
  if (props.show) {
    const isLoggedIn = await checkLoginStatus()
    if (!isLoggedIn) {
      generateQRCode()
    }
  }
})

onUnmounted(() => {
  stopPolling()
})

function getVipLabel(userInfo: BilibiliUserInfo): string {
  // Only show VIP label if actually has active VIP
  if (!userInfo.isVip) {
    return '普通用户'
  }
  switch (userInfo.vipType) {
    case 1: return '月度大会员'
    case 2: return '年度大会员'
    default: return '大会员'
  }
}

</script>

<template>
  <NModal
    :show="show"
    preset="card"
    style="width: 400px"
    title="B站账号"
    :mask-closable="true"
    @update:show="emit('update:show', $event)"
  >
    <div class="bilibili-login">
      <!-- Logged in state -->
      <div v-if="userInfo && userInfo.isLogin" class="logged-in text-center w-full">
        <!-- Avatar with fallback using ProxiedImage -->
        <div class="avatar-wrapper mb-4">
          <div class="avatar-container">
            <ProxiedImage 
              v-if="userInfo.face"
              :src="userInfo.face"
              alt="avatar"
              class="avatar-img-wrapper"
            >
              <template #placeholder>
                <PersonCircleOutline class="fallback-icon" />
              </template>
            </ProxiedImage>
            <div v-else class="avatar-fallback">
              <PersonCircleOutline class="fallback-icon" />
            </div>
          </div>
        </div>
        
        <h3 class="text-lg font-semibold mb-2">{{ userInfo.username }}</h3>
        <NSpace justify="center" class="mb-4">
          <NTag :type="userInfo.isVip ? 'warning' : 'default'" size="small">
            {{ getVipLabel(userInfo) }}
          </NTag>
          <NTag type="info" size="small">UID: {{ userInfo.uid }}</NTag>
        </NSpace>
        <p class="text-sm text-text-secondary mb-4">
          {{ userInfo.isVip ? '已解锁全部画质' : '登录后可下载1080P，大会员可下载更高画质' }}
        </p>
      </div>

      <!-- Logout success state -->
      <div v-else-if="logoutSuccess" class="logout-success text-center">
        <div class="success-icon mb-4">
          <CheckmarkCircleOutline class="w-16 h-16 text-success" />
        </div>
        <h3 class="text-lg font-semibold mb-2">退出登录成功</h3>
        <p class="text-sm text-text-secondary mb-4">
          您已成功退出B站账号
        </p>
        <NButton type="primary" @click="generateQRCode">
          重新登录
        </NButton>
      </div>

      <!-- QR code login -->
      <div v-else class="qr-login text-center">
        <p class="text-sm text-text-secondary mb-4">
          使用B站APP扫码登录，解锁高清画质
        </p>
        
        <!-- QR Code -->
        <div class="qr-container relative inline-block mb-4">
          <div v-if="loading" class="qr-placeholder w-[200px] h-[200px] flex items-center justify-center bg-tertiary rounded">
            <NSpin size="large" />
          </div>
          <template v-else>
            <img 
              v-if="qrCodeImage" 
              :src="qrCodeImage" 
              alt="QR Code"
              class="rounded"
              :class="{ 'opacity-30': qrStatus === 'expired' || qrStatus === 'scanned' }"
            />
            
            <!-- Overlay for expired/scanned -->
            <div 
              v-if="qrStatus === 'expired'" 
              class="absolute inset-0 flex flex-col items-center justify-center bg-black/50 rounded"
            >
              <p class="text-white mb-2">二维码已过期</p>
              <NButton size="small" @click="generateQRCode">
                <template #icon>
                  <RefreshOutline class="w-4 h-4" />
                </template>
                刷新
              </NButton>
            </div>
            
            <div 
              v-if="qrStatus === 'scanned'" 
              class="absolute inset-0 flex items-center justify-center bg-black/50 rounded"
            >
              <p class="text-white">请在手机上确认登录</p>
            </div>
          </template>
        </div>

        <!-- Status text -->
        <p class="text-sm text-text-secondary">
          <template v-if="qrStatus === 'ready'">
            打开B站APP扫描二维码
          </template>
          <template v-else-if="qrStatus === 'scanned'">
            扫描成功，请在手机上确认
          </template>
          <template v-else-if="qrStatus === 'expired'">
            二维码已过期，请刷新
          </template>
          <template v-else-if="qrStatus === 'success'">
            登录成功！
          </template>
        </p>
        
        <!-- Disclaimer -->
        <p class="text-xs text-text-tertiary mt-4 px-4">
          画质限制为 B站官方策略，与本应用无关。
        </p>
      </div>
    </div>

    <template #footer>
      <div class="flex justify-between items-center">
        <!-- Logout button on the left (only when logged in) -->
        <div>
          <NButton 
            v-if="userInfo && userInfo.isLogin" 
            type="error" 
            text
            @click="handleLogout"
          >
            <template #icon>
              <LogOutOutline class="w-4 h-4" />
            </template>
            退出登录
          </NButton>
        </div>
        
        <!-- Close button on the right -->
        <NButton @click="close">关闭</NButton>
      </div>
    </template>
  </NModal>
</template>

<style scoped>
.bilibili-login {
  min-height: 250px;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
}

.qr-container {
  border: 1px solid var(--border-color);
  border-radius: 8px;
  padding: 8px;
  background: white;
}

.text-success {
  color: #18a058;
}

.avatar-wrapper {
  display: flex;
  justify-content: center;
}

.avatar-container {
  width: 80px;
  height: 80px;
  border-radius: 50%;
  overflow: hidden;
  background: var(--bg-tertiary, #3a3a3a);
}

.avatar-img-wrapper {
  width: 100%;
  height: 100%;
}

.avatar-img-wrapper :deep(img) {
  border-radius: 50%;
}

.avatar-fallback {
  width: 100%;
  height: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--bg-tertiary, #3a3a3a);
}

.fallback-icon {
  width: 48px;
  height: 48px;
  color: var(--text-secondary, #999);
}
</style>
