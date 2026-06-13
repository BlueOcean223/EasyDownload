<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useAppStore } from '@/stores/app'
import { 
  NCard, NButton, NInput, NEmpty, NSpace, NTag, 
  NSelect, NSpin, useMessage, NAlert, NTooltip
} from 'naive-ui'
import { 
  SearchOutline, 
  CloudDownloadOutline,
  PlayCircleOutline,
  TimeOutline,
  PersonOutline,
  ListOutline,
  LinkOutline,
  LogInOutline,
  PersonCircleOutline
} from '@vicons/ionicons5'
import type { BilibiliVideo, BilibiliStream, BilibiliUserInfo } from '@/types'
import { GetBilibiliVideoInfo, GetBilibiliVideoInfoWithAllParts, DownloadBilibiliVideo, DownloadBilibiliPart, GetBilibiliUserInfo, HasBilibiliSessData } from '../../wailsjs/go/main/App'
import PartSelector from '@/components/PartSelector.vue'
import ProxiedImage from '@/components/ProxiedImage.vue'
import BilibiliIcon from '@/components/BilibiliIcon.vue'
import BilibiliLogin from '@/components/BilibiliLogin.vue'

const store = useAppStore()
const message = useMessage()

const url = ref('')
const loading = ref(false)
const downloading = ref(false)
const videoInfo = ref<BilibiliVideo | null>(null)
const selectedQuality = ref<number | undefined>(undefined)
const error = ref('')
const showPartSelector = ref(false)
const showLoginModal = ref(false)
const userInfo = ref<BilibiliUserInfo | null>(null)
let fetchVideoInfoRequestId = 0

// Check login status on mount
onMounted(async () => {
  try {
    // Use HasBilibiliSessData instead of GetBilibiliSessData for security
    const hasSessData = await HasBilibiliSessData()
    if (hasSessData) {
      const info = await GetBilibiliUserInfo()
      if (info && info.isLogin) {
        userInfo.value = info
      }
    }
  } catch (e) {
    console.error('Failed to check login status:', e)
  }
})

async function handleLogin(info: BilibiliUserInfo) {
  userInfo.value = info
  showLoginModal.value = false

  // 如果已有视频信息，自动重新解析以获取更高画质
  if (videoInfo.value && url.value.trim()) {
    message.info('正在重新解析以获取更高画质...')
    await fetchVideoInfo()
  }
}

function handleLogout() {
  userInfo.value = null
}

const qualityOptions = ref<{ label: string; value: number }[]>([])

// Get estimated file size for selected quality
const selectedStreamSize = computed(() => {
  if (!videoInfo.value || selectedQuality.value === undefined) return null
  const stream = (videoInfo.value.streams || []).find(s => s.quality === selectedQuality.value)
  return stream?.size || null
})

// Format file size to human readable
function formatFileSize(bytes?: number | null) {
  if (!bytes || bytes <= 0) return ''
  const units = ['B', 'KB', 'MB', 'GB']
  let unitIndex = 0
  let size = bytes
  while (size >= 1024 && unitIndex < units.length - 1) {
    size /= 1024
    unitIndex++
  }
  return `≈ ${size.toFixed(1)} ${units[unitIndex]}`
}

// Check if video has multiple parts
const hasMultipleParts = computed(() => {
  return videoInfo.value && videoInfo.value.parts && videoInfo.value.parts.length > 1
})

const isBangumi = computed(() => !!videoInfo.value?.isBangumi)

const currentPart = computed(() => {
  if (!videoInfo.value || !videoInfo.value.parts || videoInfo.value.parts.length === 0) return null
  const index = videoInfo.value.currentPartIndex ?? 0
  return videoInfo.value.parts[index] || videoInfo.value.parts[0]
})

const totalEpisodes = computed(() => videoInfo.value?.totalEps || videoInfo.value?.parts?.length || 0)

function seasonTypeName(type?: number) {
  const map: Record<number, string> = {
    1: '番剧',
    2: '电影',
    3: '纪录片',
    4: '国创',
    5: '电视剧',
    7: '综艺'
  }
  return type ? (map[type] || '番剧') : '番剧'
}

function badgeTagType(badge?: string): 'default' | 'info' | 'success' | 'warning' {
  if (badge === '会员') return 'warning'
  if (badge === '限免') return 'success'
  if (badge === '预告') return 'info'
  return 'default'
}

async function fetchVideoInfo() {
  const urlSnapshot = url.value.trim()
  if (!urlSnapshot) {
    message.warning('请输入B站视频链接')
    return
  }

  const requestId = ++fetchVideoInfoRequestId
  loading.value = true
  error.value = ''
  videoInfo.value = null
  selectedQuality.value = undefined
  qualityOptions.value = []
  showPartSelector.value = false

  try {
    const info = await GetBilibiliVideoInfo(urlSnapshot) as BilibiliVideo
    // Check if URL changed during fetch - discard stale result
    if (requestId !== fetchVideoInfoRequestId) return

    videoInfo.value = info

    // Build quality options
    const streams = info.streams || []
    qualityOptions.value = streams.map(s => ({
      label: s.qualityName,
      value: s.quality
    }))

    // Select highest quality by default. If a bangumi current episode has no
    // stream list (for example a VIP episode without permission), keep an
    // automatic quality option so users can still expand the season and queue
    // other playable episodes; the backend will re-fetch fresh streams per episode.
    if (streams.length > 0) {
      selectedQuality.value = streams[0].quality
    } else if (info.isBangumi) {
      qualityOptions.value = [{ label: '自动/最高可用', value: 127 }]
      selectedQuality.value = 127
    } else {
      selectedQuality.value = undefined
    }
  } catch (e: any) {
    // Check if URL changed during fetch - discard stale error
    if (requestId !== fetchVideoInfoRequestId) return
    error.value = e.message || '获取视频信息失败'
    message.error(error.value)
  } finally {
    if (requestId === fetchVideoInfoRequestId) {
      loading.value = false
    }
  }
}

const loadingParts = ref(false)

async function openPartSelector() {
  if (!videoInfo.value) return

  // Ordinary multi-P videos fetch all part streams for size estimates. Bangumi seasons can
  // contain hundreds of episodes, so keep stream loading lazy and fetch only at download time.
  if (!isBangumi.value) {
    const hasPartStreams = videoInfo.value.parts.some(p => p.streams && p.streams.length > 0)
    if (!hasPartStreams) {
      loadingParts.value = true
      try {
        const fullInfo = await GetBilibiliVideoInfoWithAllParts(url.value) as BilibiliVideo
        videoInfo.value = fullInfo
      } catch (e: any) {
        console.error('Failed to load parts info:', e)
        // Continue anyway, just won't have size estimates
      } finally {
        loadingParts.value = false
      }
    }
  }

  showPartSelector.value = true
}

async function downloadVideo() {
  if (!videoInfo.value) return
  if (selectedQuality.value === undefined) {
    message.warning('当前内容没有可用清晰度，可能需要登录或大会员权限')
    return
  }

  // Ordinary multi-P videos keep the existing selector-first behavior.
  if (hasMultipleParts.value && !isBangumi.value) {
    await openPartSelector()
    return
  }

  // Single ordinary video or current bangumi episode - download directly.
  downloading.value = true
  try {
    await DownloadBilibiliVideo(url.value, selectedQuality.value)
    message.success('已添加到下载队列')
  } catch (e: any) {
    message.error(e.message || '下载失败')
  } finally {
    downloading.value = false
  }
}

async function downloadSelectedParts(partIndices: number[]) {
  if (!videoInfo.value || selectedQuality.value === undefined) return

  downloading.value = true
  try {
    // Download each selected part
    for (const partIndex of partIndices) {
      await DownloadBilibiliPart(url.value, partIndex, selectedQuality.value)
    }
    message.success(`已添加 ${partIndices.length} 个${isBangumi.value ? '剧集' : '分P'}到下载队列`)
  } catch (e: any) {
    message.error(e.message || '下载失败')
  } finally {
    downloading.value = false
  }
}

function formatDuration(seconds: number) {
  const mins = Math.floor(seconds / 60)
  const secs = seconds % 60
  return `${mins}:${secs.toString().padStart(2, '0')}`
}

function handleKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter') {
    fetchVideoInfo()
  }
}
</script>

<template>
  <div class="bilibili-page h-full flex flex-col">
    <!-- Header -->
    <div class="header p-4 border-b border-border">
      <div class="flex items-center justify-between mb-4">
        <h2 class="text-xl font-semibold flex items-center gap-2">
          <BilibiliIcon class="w-6 h-6" />
          B站视频下载
        </h2>
        
        <!-- Login status / button -->
        <div class="flex items-center gap-2">
          <template v-if="userInfo && userInfo.isLogin">
            <NTooltip>
              <template #trigger>
                <div 
                  class="flex items-center gap-2 px-3 py-1.5 rounded-full bg-tertiary cursor-pointer hover:bg-opacity-80"
                  @click="showLoginModal = true"
                >
                  <div class="header-avatar">
                    <ProxiedImage 
                      v-if="userInfo.face"
                      :src="userInfo.face"
                      alt="avatar"
                      class="header-avatar-img"
                    >
                      <template #placeholder>
                        <PersonCircleOutline class="header-avatar-fallback" />
                      </template>
                    </ProxiedImage>
                    <PersonCircleOutline v-else class="header-avatar-fallback" />
                  </div>
                  <span class="text-sm">{{ userInfo.username }}</span>
                  <NTag v-if="userInfo.isVip" type="warning" size="tiny">大会员</NTag>
                </div>
              </template>
              点击管理账号
            </NTooltip>
          </template>
          <template v-else>
            <NButton size="small" @click="showLoginModal = true">
              <template #icon>
                <LogInOutline class="w-4 h-4" />
              </template>
              登录解锁高清
            </NButton>
          </template>
        </div>
      </div>
      
      <!-- URL Input -->
      <div class="flex gap-3">
        <NInput 
          v-model:value="url"
          placeholder="请输入B站视频链接 (支持 BV号、av号、番剧 ep/ss/md 链接)"
          clearable
          size="large"
          class="flex-1"
          @keydown="handleKeydown"
        >
          <template #prefix>
            <SearchOutline class="w-5 h-5 text-text-secondary" />
          </template>
        </NInput>
        
        <NButton 
          type="primary" 
          size="large"
          :loading="loading"
          @click="fetchVideoInfo"
        >
          解析
        </NButton>
      </div>
      
      <!-- FFmpeg Warning -->
      <NAlert 
        v-if="!store.ffmpegAvailable" 
        type="warning" 
        class="mt-3"
        title="FFmpeg 未安装"
      >
        下载高清视频需要 FFmpeg 进行音视频合并。可在「设置」页自动安装，或手动安装后重启应用。
      </NAlert>
    </div>
    
    <!-- Content -->
    <div class="content flex-1 overflow-auto p-4">
      <!-- Loading -->
      <div v-if="loading" class="h-full flex items-center justify-center">
        <NSpin size="large" />
      </div>
      
      <!-- Empty State -->
      <div v-else-if="!videoInfo" class="h-full flex items-center justify-center">
        <NEmpty description="输入视频链接开始解析">
          <template #icon>
            <LinkOutline class="w-12 h-12 text-text-secondary opacity-50" />
          </template>
          <template #extra>
            <p class="text-text-secondary text-sm mt-2">
              支持普通视频和番剧/影视链接（ep、ss、md）
            </p>
          </template>
        </NEmpty>
      </div>
      
      <!-- Video Info -->
      <div v-else class="max-w-4xl mx-auto">
        <NCard :bordered="false" class="bg-secondary">
          <div class="flex gap-6">
            <!-- Cover -->
            <div class="w-80 flex-shrink-0">
              <div class="aspect-video bg-tertiary rounded-lg overflow-hidden">
                <ProxiedImage 
                  v-if="videoInfo.cover"
                  :src="videoInfo.cover" 
                  :alt="videoInfo.title"
                  class="w-full h-full"
                >
                  <template #placeholder>
                    <PlayCircleOutline class="w-16 h-16 text-text-secondary opacity-50" />
                  </template>
                </ProxiedImage>
                <div v-else class="w-full h-full flex items-center justify-center">
                  <PlayCircleOutline class="w-16 h-16 text-text-secondary opacity-50" />
                </div>
              </div>
            </div>
            
            <!-- Info -->
            <div class="flex-1 flex flex-col">
              <h3 class="text-lg font-semibold mb-2">{{ videoInfo.title }}</h3>
              <div v-if="isBangumi && currentPart" class="text-sm text-text-secondary mb-3">
                当前：{{ currentPart.partName }}
              </div>
              
              <div class="flex items-center gap-4 text-sm text-text-secondary mb-3 flex-wrap">
                <span class="flex items-center gap-1">
                  <PersonOutline class="w-4 h-4" />
                  {{ videoInfo.author }}
                </span>
                <span class="flex items-center gap-1">
                  <TimeOutline class="w-4 h-4" />
                  {{ formatDuration(videoInfo.duration) }}
                </span>
                <NTag v-if="isBangumi" size="small" type="primary">
                  {{ seasonTypeName(videoInfo.seasonType) }}
                </NTag>
                <NTag v-if="videoInfo.bv" size="small" type="info">{{ videoInfo.bv }}</NTag>
                <NTag v-if="isBangumi && currentPart?.badge" size="small" :type="badgeTagType(currentPart.badge)">
                  {{ currentPart.badge }}
                </NTag>
              </div>
              
              <p class="text-sm text-text-secondary line-clamp-3 mb-4">
                {{ videoInfo.desc || '暂无简介' }}
              </p>

              <NAlert
                v-if="(videoInfo.streams || []).length === 0"
                type="warning"
                class="mb-4"
                title="暂无可用清晰度"
              >
                当前集可能需要登录、开通大会员，或暂不可播放；也可以展开整季选择其他可播放剧集。
              </NAlert>
              
              <div class="mt-auto flex items-center gap-4 flex-wrap">
                <div class="flex items-center gap-2">
                  <span class="text-sm text-text-secondary">画质:</span>
                  <NSelect 
                    v-model:value="selectedQuality"
                    :options="qualityOptions"
                    size="small"
                    style="width: 150px"
                  />
                  <span v-if="selectedStreamSize" class="text-sm text-text-secondary">
                    {{ formatFileSize(selectedStreamSize) }}
                  </span>
                </div>
                
                <NButton 
                  type="primary"
                  :loading="downloading || loadingParts"
                  :disabled="!selectedQuality"
                  @click="downloadVideo"
                >
                  <template #icon>
                    <CloudDownloadOutline class="w-4 h-4" />
                  </template>
                  {{ isBangumi ? '下载当前集' : (loadingParts ? '加载分P信息...' : (hasMultipleParts ? '选择分P下载' : '下载视频')) }}
                </NButton>

                <NButton
                  v-if="isBangumi && hasMultipleParts"
                  :loading="loadingParts"
                  @click="openPartSelector"
                >
                  展开全部 {{ totalEpisodes }} 集
                </NButton>
              </div>
              
              <!-- Multi-part / bangumi indicator -->
              <div v-if="hasMultipleParts" class="mt-3 flex items-center gap-2 flex-wrap">
                <NTag type="info" size="small">
                  <template #icon>
                    <ListOutline class="w-3 h-3" />
                  </template>
                  {{ isBangumi ? `共 ${totalEpisodes} 集` : `共 ${videoInfo.parts.length} P` }}
                </NTag>
                <span class="text-xs text-text-secondary">
                  {{ isBangumi ? '默认下载当前集，也可展开整季多选' : '点击下载按钮选择要下载的分P' }}
                </span>
              </div>
            </div>
          </div>
        </NCard>
      </div>
    </div>
    
    <!-- Part Selector Modal -->
    <PartSelector
      v-if="videoInfo"
      v-model:show="showPartSelector"
      :parts="videoInfo.parts || []"
      :video-title="videoInfo.title"
      :streams="videoInfo.streams || []"
      :is-bangumi="videoInfo.isBangumi"
      :current-part-index="videoInfo.currentPartIndex"
      v-model:selected-quality="selectedQuality"
      @select="downloadSelectedParts"
    />
    
    <!-- Login Modal -->
    <BilibiliLogin
      v-model:show="showLoginModal"
      @login="handleLogin"
      @logout="handleLogout"
    />
  </div>
</template>

<style scoped>
.line-clamp-3 {
  display: -webkit-box;
  -webkit-line-clamp: 3;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.header-avatar {
  width: 24px;
  height: 24px;
  border-radius: 50%;
  overflow: hidden;
  background: var(--bg-tertiary, #3a3a3a);
  flex-shrink: 0;
}

.header-avatar-img {
  width: 100%;
  height: 100%;
}

.header-avatar-img :deep(img) {
  border-radius: 50%;
}

.header-avatar-fallback {
  width: 100%;
  height: 100%;
  color: var(--text-secondary, #999);
}
</style>
