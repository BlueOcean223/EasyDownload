<script setup lang="ts">
import { ref, computed } from 'vue'
import { useAppStore } from '@/stores/app'
import { 
  NCard, NButton, NInput, NEmpty, NSpace, NTag, 
  NSelect, NSpin, useMessage, NAlert, NBadge
} from 'naive-ui'
import { 
  SearchOutline, 
  CloudDownloadOutline,
  PlayCircleOutline,
  TimeOutline,
  PersonOutline,
  ListOutline,
  LinkOutline
} from '@vicons/ionicons5'
import type { BilibiliVideo, BilibiliStream } from '@/types'
import { GetBilibiliVideoInfo, DownloadBilibiliVideo, DownloadBilibiliPart } from '../../wailsjs/go/main/App'
import PartSelector from '@/components/PartSelector.vue'
import ProxiedImage from '@/components/ProxiedImage.vue'

const store = useAppStore()
const message = useMessage()

const url = ref('')
const loading = ref(false)
const downloading = ref(false)
const videoInfo = ref<BilibiliVideo | null>(null)
const selectedQuality = ref<number | null>(null)
const error = ref('')
const showPartSelector = ref(false)

const qualityOptions = ref<{ label: string; value: number }[]>([])

// Get estimated file size for selected quality
const selectedStreamSize = computed(() => {
  if (!videoInfo.value || selectedQuality.value === null) return null
  const stream = videoInfo.value.streams.find(s => s.quality === selectedQuality.value)
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

async function fetchVideoInfo() {
  if (!url.value.trim()) {
    message.warning('请输入B站视频链接')
    return
  }

  loading.value = true
  error.value = ''
  videoInfo.value = null

  try {
    const info = await GetBilibiliVideoInfo(url.value) as BilibiliVideo
    videoInfo.value = info
    
    // Build quality options
    qualityOptions.value = info.streams.map(s => ({
      label: s.qualityName,
      value: s.quality
    }))
    
    // Select highest quality by default
    if (info.streams.length > 0) {
      selectedQuality.value = info.streams[0].quality
    }
  } catch (e: any) {
    error.value = e.message || '获取视频信息失败'
    message.error(error.value)
  } finally {
    loading.value = false
  }
}

async function downloadVideo() {
  if (!videoInfo.value || selectedQuality.value === null) return

  // If video has multiple parts, show part selector
  if (hasMultipleParts.value) {
    showPartSelector.value = true
    return
  }

  // Single part video - download directly
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
  if (!videoInfo.value || selectedQuality.value === null) return

  downloading.value = true
  try {
    // Download each selected part
    for (const partIndex of partIndices) {
      await DownloadBilibiliPart(url.value, partIndex, selectedQuality.value)
    }
    message.success(`已添加 ${partIndices.length} 个分P到下载队列`)
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
      <h2 class="text-xl font-semibold mb-4">B站视频下载</h2>
      
      <!-- URL Input -->
      <div class="flex gap-3">
        <NInput 
          v-model:value="url"
          placeholder="请输入B站视频链接 (支持 BV号、av号、完整链接)"
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
        下载高清视频需要 FFmpeg 进行音视频合并，请先安装 FFmpeg 并将其添加到系统 PATH
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
              支持 bilibili.com 和 b23.tv 链接
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
              <h3 class="text-lg font-semibold mb-3">{{ videoInfo.title }}</h3>
              
              <div class="flex items-center gap-4 text-sm text-text-secondary mb-3">
                <span class="flex items-center gap-1">
                  <PersonOutline class="w-4 h-4" />
                  {{ videoInfo.author }}
                </span>
                <span class="flex items-center gap-1">
                  <TimeOutline class="w-4 h-4" />
                  {{ formatDuration(videoInfo.duration) }}
                </span>
                <NTag size="small" type="info">{{ videoInfo.bv }}</NTag>
              </div>
              
              <p class="text-sm text-text-secondary line-clamp-3 mb-4">
                {{ videoInfo.desc || '暂无简介' }}
              </p>
              
              <div class="mt-auto flex items-center gap-4">
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
                  :loading="downloading"
                  :disabled="!selectedQuality"
                  @click="downloadVideo"
                >
                  <template #icon>
                    <CloudDownloadOutline class="w-4 h-4" />
                  </template>
                  {{ hasMultipleParts ? '选择分P下载' : '下载视频' }}
                </NButton>
              </div>
              
              <!-- Multi-part indicator -->
              <div v-if="hasMultipleParts" class="mt-3 flex items-center gap-2">
                <NTag type="info" size="small">
                  <template #icon>
                    <ListOutline class="w-3 h-3" />
                  </template>
                  共 {{ videoInfo.parts.length }} P
                </NTag>
                <span class="text-xs text-text-secondary">点击下载按钮选择要下载的分P</span>
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
      @select="downloadSelectedParts"
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
</style>

