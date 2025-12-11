<script setup lang="ts">
import { ref, computed } from 'vue'
import { useAppStore } from '@/stores/app'
import { NCard, NButton, NEmpty, NSpin, NTag, NSpace, NTooltip, useMessage, NModal, NInput } from 'naive-ui'
import { 
  PlayCircleOutline, 
  CloudDownloadOutline, 
  TrashOutline,
  RefreshOutline,
  SearchOutline
} from '@vicons/ionicons5'
import type { DetectedVideo } from '@/types'

const store = useAppStore()
const message = useMessage()
const searchQuery = ref('')
const downloading = ref<Set<string>>(new Set())

const filteredVideos = computed(() => {
  if (!searchQuery.value) {
    return store.detectedVideos
  }
  const query = searchQuery.value.toLowerCase()
  return store.detectedVideos.filter(v => 
    v.title.toLowerCase().includes(query) || 
    v.author?.toLowerCase().includes(query)
  )
})

async function downloadVideo(video: DetectedVideo) {
  if (downloading.value.has(video.id)) return
  
  downloading.value.add(video.id)
  try {
    await store.downloadDetectedVideo(video)
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

function clearAll() {
  store.clearVideos()
  message.success('已清空视频列表')
}
</script>

<template>
  <div class="sniffer-page h-full flex flex-col">
    <!-- Header -->
    <div class="header flex items-center justify-between p-4 border-b border-dark-300">
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
            <SearchOutline class="w-4 h-4 text-gray-400" />
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
      <!-- Empty State -->
      <div v-if="!store.proxyRunning" class="h-full flex items-center justify-center">
        <NEmpty description="代理服务未启动">
          <template #extra>
            <p class="text-gray-500 text-sm mb-4">请先在侧边栏启动代理服务，然后打开微信PC端浏览视频</p>
          </template>
        </NEmpty>
      </div>
      
      <div v-else-if="filteredVideos.length === 0" class="h-full flex items-center justify-center">
        <NEmpty description="暂无检测到的视频">
          <template #extra>
            <p class="text-gray-500 text-sm">打开微信PC端，浏览视频号内容即可自动检测</p>
          </template>
        </NEmpty>
      </div>
      
      <!-- Video Grid -->
      <div v-else class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-4">
        <NCard 
          v-for="video in filteredVideos" 
          :key="video.id"
          class="video-card cursor-pointer"
          :bordered="false"
          content-style="padding: 0"
          hoverable
        >
          <!-- Thumbnail -->
          <div class="relative aspect-video bg-dark-300 overflow-hidden rounded-t-lg">
            <img 
              v-if="video.cover" 
              :src="video.cover" 
              :alt="video.title"
              class="w-full h-full object-cover"
              loading="lazy"
            />
            <div v-else class="w-full h-full flex items-center justify-center">
              <PlayCircleOutline class="w-12 h-12 text-gray-600" />
            </div>
            
            <!-- Source Badge -->
            <div class="absolute top-2 left-2">
              <NTag 
                :type="video.source === 'wechat' ? 'success' : 'info'" 
                size="tiny"
              >
                {{ video.source === 'wechat' ? '视频号' : 'B站' }}
              </NTag>
            </div>
            
            <!-- Time Badge -->
            <div class="absolute bottom-2 right-2">
              <span class="text-xs bg-black/70 px-2 py-1 rounded">
                {{ formatTime(video.timestamp) }}
              </span>
            </div>
          </div>
          
          <!-- Info -->
          <div class="p-3">
            <h3 class="text-sm font-medium line-clamp-2 mb-2" :title="video.title">
              {{ video.title || '未知标题' }}
            </h3>
            
            <div class="flex items-center justify-between">
              <span class="text-xs text-gray-500">
                {{ video.author || '未知作者' }}
              </span>
              
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
  -webkit-box-orient: vertical;
  overflow: hidden;
}
</style>

