<script setup lang="ts">
import { computed } from 'vue'
import { NCard, NButton, NProgress, NTag, NTooltip } from 'naive-ui'
import {
  PlayOutline,
  PauseOutline,
  CloseOutline,
  FolderOpenOutline,
  TrashOutline,
  CheckmarkCircleOutline,
  CloudDownloadOutline
} from '@vicons/ionicons5'
import type { DownloadTask } from '@/types'
import ProxiedImage from '@/components/ProxiedImage.vue'

const props = defineProps<{
  task: DownloadTask
  mode?: 'downloading' | 'completed' | 'problem'
}>()

const emit = defineEmits<{
  (e: 'pause', task: DownloadTask): void
  (e: 'resume', task: DownloadTask): void
  (e: 'cancel', task: DownloadTask): void
  (e: 'open-file', task: DownloadTask): void
  (e: 'remove', task: DownloadTask): void
}>()

const isDownloading = computed(() => props.mode === 'downloading' || props.task.status === 'downloading' || props.task.status === 'retrying' || props.task.status === 'pending' || props.task.status === 'paused')
const isCompleted = computed(() => props.mode === 'completed' || props.task.status === 'completed')
const isProblem = computed(() => props.mode === 'problem' || props.task.status === 'failed' || props.task.status === 'cancelled')

function formatBytes(bytes: number) {
  if (bytes === 0) return '0 B'
  if (bytes < 0) return '--'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i]
}

function formatSpeed(bytesPerSecond: number) {
  return formatBytes(bytesPerSecond) + '/s'
}

// Format progress text for album or regular download
const progressText = computed(() => {
  const task = props.task
  if (task.isAlbum) {
    const completed = task.albumCompleted ?? 0
    const total = task.albumTotal ?? 0
    const sizeText = task.downloaded > 0 ? formatBytes(task.downloaded) : '--'
    return `${completed}/${total}张 (${sizeText})`
  }
  return `${formatBytes(task.downloaded)} / ${formatBytes(task.fileSize)}`
})

// Format completed size text for album
const completedSizeText = computed(() => {
  const task = props.task
  if (task.isAlbum) {
    const total = task.albumTotal ?? 0
    const sizeText = task.downloaded > 0 ? formatBytes(task.downloaded) : formatBytes(task.fileSize)
    return `${total}张 · ${sizeText}`
  }
  return formatBytes(task.fileSize)
})

function getStatusType(status: string) {
  switch (status) {
    case 'downloading': return 'info'
    case 'retrying': return 'warning'
    case 'completed': return 'success'
    case 'failed': return 'error'
    case 'paused': return 'warning'
    case 'cancelled': return 'default'
    default: return 'default'
  }
}

function getStatusText(status: string) {
  switch (status) {
    case 'downloading': return '下载中'
    case 'retrying': return '重试中'
    case 'completed': return '已完成'
    case 'failed': return '失败'
    case 'paused': return '已暂停'
    case 'cancelled': return '已取消'
    case 'pending': return '等待中'
    default: return status
  }
}
</script>

<template>
  <NCard 
    :bordered="false"
    class="bg-secondary"
    size="small"
  >
    <div class="flex items-center gap-4">
      <!-- Cover -->
      <div class="w-24 h-14 bg-tertiary rounded overflow-hidden flex-shrink-0">
        <ProxiedImage 
          v-if="task.cover" 
          :src="task.cover" 
          :alt="task.title"
          class="w-full h-full"
        >
          <template #placeholder>
            <component 
              :is="isCompleted ? CheckmarkCircleOutline : CloudDownloadOutline" 
              :class="['w-6 h-6', isCompleted ? 'text-green-500' : 'text-text-secondary opacity-50']" 
            />
          </template>
        </ProxiedImage>
        <div v-else class="w-full h-full flex items-center justify-center">
          <component 
            :is="isCompleted ? CheckmarkCircleOutline : CloudDownloadOutline" 
            :class="['w-6 h-6', isCompleted ? 'text-green-500' : 'text-text-secondary opacity-50']" 
          />
        </div>
      </div>
      
      <!-- Info -->
      <div class="flex-1 min-w-0">
        <div class="flex items-center gap-2 mb-1">
          <span class="text-sm font-medium truncate">{{ task.title }}</span>
          <NTag v-if="isDownloading || isProblem" :type="getStatusType(task.status)" size="tiny">
            {{ getStatusText(task.status) }}
          </NTag>
          <NTag v-else type="success" size="tiny">已完成</NTag>
        </div>
        
        <!-- Progress bar for downloading -->
        <template v-if="isDownloading">
          <NProgress
            type="line"
            :percentage="task.progress"
            :show-indicator="false"
            :height="4"
            :border-radius="2"
            class="mb-1"
          />

          <div class="flex items-center gap-4 text-xs text-text-secondary">
            <span>{{ progressText }}</span>
            <span v-if="task.status === 'downloading' && task.speed > 0">{{ formatSpeed(task.speed) }}</span>
            <span>{{ task.progress.toFixed(1) }}%</span>
          </div>
        </template>

        <!-- Info for completed/problem -->
        <template v-else>
          <div class="text-xs text-text-secondary">
            <template v-if="isProblem">
              <span>{{ task.error || task.lastError || (task.status === 'cancelled' ? '任务已取消' : '下载失败') }}</span>
              <span v-if="task.fileName" class="mx-2">·</span>
              <span v-if="task.fileName">{{ task.fileName }}</span>
            </template>
            <template v-else>
              <span>{{ completedSizeText }}</span>
              <span class="mx-2">·</span>
              <span>{{ task.fileName }}</span>
            </template>
          </div>
        </template>
      </div>
      
      <!-- Actions for downloading -->
      <div v-if="isDownloading" class="flex items-center gap-1 flex-shrink-0">
        <NTooltip v-if="task.status === 'downloading'">
          <template #trigger>
            <NButton size="tiny" quaternary circle @click="emit('pause', task)">
              <PauseOutline class="w-4 h-4" />
            </NButton>
          </template>
          暂停
        </NTooltip>
        
        <NTooltip v-if="task.status === 'paused'">
          <template #trigger>
            <NButton size="tiny" quaternary circle @click="emit('resume', task)">
              <PlayOutline class="w-4 h-4" />
            </NButton>
          </template>
          继续
        </NTooltip>
        
        <NTooltip>
          <template #trigger>
            <NButton size="tiny" quaternary circle @click="emit('cancel', task)">
              <CloseOutline class="w-4 h-4" />
            </NButton>
          </template>
          取消
        </NTooltip>
      </div>
      
      <!-- Actions for completed -->
      <div v-else-if="isCompleted" class="flex items-center gap-1 flex-shrink-0">
        <NTooltip>
          <template #trigger>
            <NButton size="tiny" quaternary circle @click="emit('open-file', task)">
              <FolderOpenOutline class="w-4 h-4" />
            </NButton>
          </template>
          打开文件
        </NTooltip>
        
        <NTooltip>
          <template #trigger>
            <NButton size="tiny" quaternary circle @click="emit('remove', task)">
              <TrashOutline class="w-4 h-4" />
            </NButton>
          </template>
          删除记录
        </NTooltip>
      </div>

      <!-- Actions for failed/cancelled -->
      <div v-else class="flex items-center gap-1 flex-shrink-0">
        <NTooltip>
          <template #trigger>
            <NButton size="tiny" quaternary circle @click="emit('remove', task)">
              <TrashOutline class="w-4 h-4" />
            </NButton>
          </template>
          删除记录
        </NTooltip>
      </div>
    </div>
  </NCard>
</template>


