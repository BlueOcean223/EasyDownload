<script setup lang="ts">
import { ref, computed, reactive } from 'vue'
import { useAppStore } from '@/stores/app'
import { NCard, NButton, NEmpty, NSpin, NTag, NSpace, NTooltip, useMessage, NModal, NInput, NSelect } from 'naive-ui'
import ProxiedImage from '@/components/ProxiedImage.vue'
import { 
  PlayCircleOutline, 
  CloudDownloadOutline, 
  TrashOutline,
  RefreshOutline,
  SearchOutline,
  AlertCircleOutline,
  LockClosedOutline,
  StarOutline
} from '@vicons/ionicons5'
import type { DetectedVideo } from '@/types'

const store = useAppStore()
const message = useMessage()
const searchQuery = ref('')
const downloading = ref<Set<string>>(new Set())

// Selected quality for each video (videoId -> format)
const selectedQualities = reactive<Record<string, string>>({})

// Standard quality levels based on height
const qualityLevels = [
  { height: 2160, label: '4K' },
  { height: 1440, label: '2K' },
  { height: 1080, label: '1080P' },
  { height: 720, label: '720P' },
  { height: 540, label: '540P' },
  { height: 480, label: '480P' },
  { height: 360, label: '360P' },
  { height: 240, label: '240P' },
]

// Get friendly quality name based on video height
function getQualityLabel(height: number): string {
  for (const level of qualityLevels) {
    if (height >= level.height) {
      return level.label
    }
  }
  return `${height}P`
}

// Generate quality options based on specs data (preferred) or fileFormats fallback
// IMPORTANT: WeChat video URLs without X-snsvideoflag parameter return the HIGHEST quality
// Adding X-snsvideoflag parameter requests a SPECIFIC (usually lower) quality
function getQualityOptions(video: DetectedVideo) {
  const options: { label: string; value: string }[] = []
  const seenLabels = new Set<string>()
  
  // Always add "Original" option first - this downloads the highest quality
  // by NOT adding X-snsvideoflag parameter to the URL
  options.push({
    label: '原始画质',
    value: ''  // Empty value means use original URL without X-snsvideoflag
  })
  
  // Use specs data if available (contains actual resolution info)
  if (video.specs && video.specs.length > 0) {
    // Sort specs by height descending (highest quality first)
    const sortedSpecs = [...video.specs].sort((a, b) => b.height - a.height)
    
    for (const spec of sortedSpecs) {
      if (!spec.fileFormat) continue
      
      const label = getQualityLabel(spec.height)
      
      // Deduplicate by label
      if (seenLabels.has(label)) continue
      seenLabels.add(label)
      
      options.push({
        label,
        value: spec.fileFormat
      })
    }
    
    return options
  }
  
  // Fallback: use fileFormats with estimated quality levels
  if (!video.fileFormats || video.fileFormats.length === 0) {
    return options  // Return with just "原始画质" option
  }
  
  const videoHeight = video.height || 720
  const formatCount = video.fileFormats.length
  
  // Find available quality levels up to the video's resolution
  const availableLevels = qualityLevels.filter(level => level.height <= videoHeight)
  const levelsToUse = availableLevels.slice(0, formatCount)
  
  // Map formats to quality levels
  for (let i = 0; i < formatCount; i++) {
    const format = video.fileFormats[i]
    let label: string
    
    if (i < levelsToUse.length) {
      label = levelsToUse[i].label
    } else {
      const estimatedHeight = Math.round(videoHeight * (formatCount - i) / formatCount)
      label = getQualityLabel(estimatedHeight)
    }
    
    // Deduplicate by label
    if (seenLabels.has(label)) continue
    seenLabels.add(label)
    
    options.push({ label, value: format })
  }
  
  return options
}

// Get default (highest) quality for a video
// Returns empty string to use original URL (highest quality)
function getDefaultQuality(video: DetectedVideo): string {
  // Always default to empty string (original quality / highest quality)
  // The original URL without X-snsvideoflag returns the highest quality
  return ''
}

// Get selected quality for a video, or default to highest
function getSelectedQuality(video: DetectedVideo): string {
  if (selectedQualities[video.id]) {
    return selectedQualities[video.id]
  }
  return getDefaultQuality(video)
}

// Keep list order as stored (WeChat newest first); only filter by search query
const filteredVideos = computed(() => {
  let videos = [...store.detectedVideos]
  
  if (!searchQuery.value) {
    return videos
  }
  const query = searchQuery.value.toLowerCase()
  return videos.filter(v => 
    v.title.toLowerCase().includes(query) || 
    v.author?.toLowerCase().includes(query)
  )
})

// Check if a video is the current one
function isCurrentVideo(video: DetectedVideo) {
  return !!video.isCurrentVideo
}

async function downloadVideo(video: DetectedVideo) {
  if (downloading.value.has(video.id)) return
  
  downloading.value.add(video.id)
  try {
    // Get selected quality format
    const selectedFormat = getSelectedQuality(video)
    console.log('[Sniffer] Download video:', video.title)
    console.log('[Sniffer] Video specs:', video.specs)
    console.log('[Sniffer] Video fileFormats:', video.fileFormats)
    console.log('[Sniffer] Quality options:', getQualityOptions(video))
    console.log('[Sniffer] Selected format:', selectedFormat)
    // Pass the selected quality to download
    await store.downloadDetectedVideo(video, selectedFormat)
    message.success('已添加到下载队列')
  } catch (error: any) {
    message.error(error.message || '下载失败')
  } finally {
    downloading.value.delete(video.id)
  }
}

function formatTime(timestamp: number) {
  const date = new Date(timestamp)
  return date.toLocaleTimeString('zh-CN', { 
    hour: '2-digit', 
    minute: '2-digit' 
  })
}

function formatFileSize(bytes?: number) {
  if (!bytes || bytes <= 0) return ''
  const units = ['B', 'KB', 'MB', 'GB']
  let unitIndex = 0
  let size = bytes
  while (size >= 1024 && unitIndex < units.length - 1) {
    size /= 1024
    unitIndex++
  }
  return `${size.toFixed(1)} ${units[unitIndex]}`
}

function formatDuration(ms?: number) {
  if (!ms || ms <= 0) return ''
  const totalSeconds = Math.floor(ms / 1000)
  const hours = Math.floor(totalSeconds / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  
  if (hours > 0) {
    return `${hours}:${minutes.toString().padStart(2, '0')}:${seconds.toString().padStart(2, '0')}`
  }
  return `${minutes}:${seconds.toString().padStart(2, '0')}`
}

function clearAll() {
  store.clearVideos()
  message.success('已清空视频列表')
}
</script>

<template>
  <div class="sniffer-page h-full flex flex-col">
    <!-- Header -->
    <div class="header flex items-center justify-between p-4 border-b border-border">
      <div class="flex items-center gap-4">
        <h2 class="text-xl font-semibold">视频嗅探</h2>
        <NTag :type="store.proxyRunning ? 'success' : 'error'" size="small">
          <span class="flex items-center gap-1">
            <span class="status-dot" :class="store.proxyRunning ? 'running' : 'stopped'"></span>
            {{ store.proxyRunning ? '运行中' : '已停止' }}
          </span>
        </NTag>
      </div>
      
      <div class="flex items-center gap-3">
        <NInput 
          v-model:value="searchQuery"
          placeholder="搜索视频..."
          clearable
          size="small"
          style="width: 200px"
        >
          <template #prefix>
            <SearchOutline class="w-4 h-4 text-text-secondary" />
          </template>
        </NInput>
        
        <NTooltip>
          <template #trigger>
            <NButton 
              size="small" 
              quaternary 
              circle
              @click="clearAll"
              :disabled="store.detectedVideos.length === 0"
            >
              <TrashOutline class="w-4 h-4" />
            </NButton>
          </template>
          清空列表
        </NTooltip>
      </div>
    </div>
    
    <!-- Content -->
    <div class="content flex-1 overflow-auto p-4">
      <!-- Empty State: only show when no videos detected -->
      <div v-if="filteredVideos.length === 0" class="h-full flex items-center justify-center">
        <NEmpty :description="!store.proxyRunning ? '代理服务未启动' : '暂无检测到的视频'">
          <template #icon>
            <component 
              :is="!store.proxyRunning ? AlertCircleOutline : PlayCircleOutline" 
              class="w-12 h-12 text-text-secondary opacity-50" 
            />
          </template>
          <template #extra>
            <p class="text-text-secondary text-sm mb-4">
              {{ !store.proxyRunning ? '请先在侧边栏启动代理服务，然后打开微信PC端浏览视频' : '打开微信PC端，浏览视频号内容即可自动检测' }}
            </p>
          </template>
        </NEmpty>
      </div>
      
      <!-- Video Grid -->
      <div v-else class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-4">
        <NCard 
          v-for="video in filteredVideos" 
          :key="video.id"
          class="video-card cursor-pointer bg-secondary"
          :class="{ 'current-video': isCurrentVideo(video) }"
          :bordered="false"
          content-style="padding: 0"
          hoverable
        >
          <!-- Thumbnail -->
          <div class="relative aspect-video bg-tertiary overflow-hidden rounded-t-lg">
            <ProxiedImage
              v-if="video.cover"
              :src="video.cover"
              :alt="video.title"
              class="w-full h-full"
            />
            <div v-else class="w-full h-full flex items-center justify-center">
              <PlayCircleOutline class="w-12 h-12 text-text-secondary opacity-50" />
            </div>
            
            <!-- Source Badge -->
            <div class="absolute top-2 left-2 flex gap-1">
              <NTag 
                :type="video.source === 'wechat' ? 'success' : 'info'" 
                size="tiny"
              >
                {{ video.source === 'wechat' ? '视频号' : 'B站' }}
              </NTag>
              <!-- Current Video Badge -->
              <NTooltip v-if="isCurrentVideo(video)">
                <template #trigger>
                  <NTag type="primary" size="tiny">
                    <template #icon>
                      <StarOutline class="w-3 h-3" />
                    </template>
                    当前
                  </NTag>
                </template>
                当前正在播放的视频
              </NTooltip>
              <!-- Encrypted Badge -->
              <NTooltip v-if="video.decodeKey">
                <template #trigger>
                  <NTag type="warning" size="tiny">
                    <template #icon>
                      <LockClosedOutline class="w-3 h-3" />
                    </template>
                    加密
                  </NTag>
                </template>
                视频已加密，下载时将自动解密
              </NTooltip>
            </div>
            
            <!-- Duration Badge -->
            <div v-if="video.duration" class="absolute bottom-2 left-2">
              <span class="text-xs bg-black/70 text-white px-2 py-1 rounded">
                {{ formatDuration(video.duration) }}
              </span>
            </div>
          
          </div>
          
          <!-- Info -->
          <div class="p-3">
            <h3 class="text-sm font-medium line-clamp-2 mb-2" :title="video.title">
              {{ video.title || '未知标题' }}
            </h3>
            
            <div class="flex items-center justify-between mb-2">
              <span class="text-xs text-text-secondary">
                {{ video.author || '未知作者' }}
              </span>
              <div class="flex items-center gap-2">
                <span v-if="video.width && video.height" class="text-xs text-text-secondary opacity-80">
                  {{ video.width }}×{{ video.height }}
                </span>
                <span v-if="video.fileSize" class="text-xs text-text-secondary opacity-80">
                  {{ formatFileSize(video.fileSize) }}
                </span>
              </div>
            </div>
            
            <div class="flex items-center justify-between">
              <!-- Quality selector for videos with multiple formats (more than just "原始画质") -->
              <div v-if="getQualityOptions(video).length > 2" class="flex items-center gap-1">
                <span class="text-xs text-text-secondary">画质:</span>
                <NSelect 
                  :value="getSelectedQuality(video)"
                  :options="getQualityOptions(video)"
                  size="tiny"
                  style="width: 100px"
                  @update:value="(val: string) => selectedQualities[video.id] = val"
                />
              </div>
              <div v-else class="flex items-center">
                <NTag size="tiny" type="info">原始画质</NTag>
              </div>
              
              <NButton 
                type="primary" 
                size="tiny"
                :loading="downloading.has(video.id)"
                @click.stop="downloadVideo(video)"
              >
                <template #icon>
                  <CloudDownloadOutline class="w-4 h-4" />
                </template>
                下载
              </NButton>
            </div>
          </div>
        </NCard>
      </div>
    </div>
  </div>
</template>

<style scoped>
.line-clamp-2 {
  display: -webkit-box;
  -webkit-line-clamp: 2;
  line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

/* Current video highlight */
.current-video {
  border: 2px solid var(--primary-color, #18a058) !important;
  box-shadow: 0 0 12px rgba(24, 160, 88, 0.3);
}

.current-video:hover {
  box-shadow: 0 0 16px rgba(24, 160, 88, 0.4);
}

.status-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
}

.status-dot.running {
  background-color: #18a058;
  animation: pulse 2s infinite;
}

.status-dot.stopped {
  background-color: #d03050;
}

@keyframes pulse {
  0%, 100% {
    opacity: 1;
  }
  50% {
    opacity: 0.5;
  }
}
</style>

