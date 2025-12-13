<script setup lang="ts">
import { computed } from 'vue'
import { useAppStore } from '@/stores/app'
import { 
  NCard, NButton, NEmpty, NProgress, NTag, 
  NSpace, NTooltip, NTabs, NTabPane, useMessage 
} from 'naive-ui'
import { 
  PlayOutline, 
  PauseOutline, 
  CloseOutline,
  FolderOpenOutline,
  TrashOutline,
  CheckmarkCircleOutline,
  AlertCircleOutline,
  CloudDownloadOutline
} from '@vicons/ionicons5'
import type { DownloadTask } from '@/types'
import { OpenFile } from '../../wailsjs/go/main/App'

const store = useAppStore()
const message = useMessage()

const activeTab = computed(() => {
  if (store.pendingDownloads.length > 0) return 'downloading'
  return 'completed'
})

function formatBytes(bytes: number) {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i]
}

function formatSpeed(bytesPerSecond: number) {
  return formatBytes(bytesPerSecond) + '/s'
}

function getStatusType(status: string) {
  switch (status) {
    case 'downloading': return 'info'
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
    case 'completed': return '已完成'
    case 'failed': return '失败'
    case 'paused': return '已暂停'
    case 'cancelled': return '已取消'
    case 'pending': return '等待中'
    default: return status
  }
}

async function handlePause(task: DownloadTask) {
  try {
    await store.pauseDownloadTask(task.id)
  } catch (e: any) {
    message.error(e.message || '暂停失败')
  }
}

async function handleResume(task: DownloadTask) {
  try {
    await store.resumeDownloadTask(task.id)
  } catch (e: any) {
    message.error(e.message || '恢复失败')
  }
}

async function handleCancel(task: DownloadTask) {
  try {
    await store.cancelDownloadTask(task.id)
  } catch (e: any) {
    message.error(e.message || '取消失败')
  }
}

async function handleRemove(task: DownloadTask) {
  try {
    await store.removeDownloadTask(task.id)
  } catch (e: any) {
    message.error(e.message || '删除失败')
  }
}

async function handleOpenFile(task: DownloadTask) {
  if (task.filePath) {
    try {
      await OpenFile(task.filePath)
    } catch (e: any) {
      message.error('打开文件失败')
    }
  }
}

async function openDownloadFolder() {
  await store.openFolder()
}
</script>

<template>
  <div class="downloads-page h-full flex flex-col">
    <!-- Header -->
    <div class="header flex items-center justify-between p-4 border-b border-border">
      <h2 class="text-xl font-semibold">下载管理</h2>
      
      <NButton size="small" @click="openDownloadFolder">
        <template #icon>
          <FolderOpenOutline class="w-4 h-4" />
        </template>
        打开下载目录
      </NButton>
    </div>
    
    <!-- Content -->
    <div class="content flex-1 overflow-auto">
      <NTabs type="line" :default-value="activeTab" class="downloads-tabs h-full" pane-class="h-full">
        <!-- Downloading Tab -->
        <NTabPane name="downloading" tab="下载中" class="h-full">
          <div class="p-4 h-full overflow-auto">
            <NEmpty 
              v-if="store.pendingDownloads.length === 0" 
              description="暂无进行中的下载"
              class="h-full flex items-center justify-center"
            >
              <template #icon>
                <CloudDownloadOutline class="w-12 h-12 text-text-secondary opacity-50" />
              </template>
            </NEmpty>
            
            <div v-else class="space-y-3">
              <NCard 
                v-for="task in store.pendingDownloads" 
                :key="task.id"
                :bordered="false"
                class="bg-secondary"
                size="small"
              >
                <div class="flex items-center gap-4">
                  <!-- Cover -->
                  <div class="w-24 h-14 bg-tertiary rounded overflow-hidden flex-shrink-0">
                    <img 
                      v-if="task.cover" 
                      :src="task.cover" 
                      :alt="task.title"
                      class="w-full h-full object-cover"
                    />
                    <div v-else class="w-full h-full flex items-center justify-center">
                      <CloudDownloadOutline class="w-6 h-6 text-text-secondary opacity-50" />
                    </div>
                  </div>
                  
                  <!-- Info -->
                  <div class="flex-1 min-w-0">
                    <div class="flex items-center gap-2 mb-1">
                      <span class="text-sm font-medium truncate">{{ task.title }}</span>
                      <NTag :type="getStatusType(task.status)" size="tiny">
                        {{ getStatusText(task.status) }}
                      </NTag>
                    </div>
                    
                    <NProgress 
                      type="line" 
                      :percentage="task.progress"
                      :show-indicator="false"
                      :height="4"
                      :border-radius="2"
                      class="mb-1"
                    />
                    
                    <div class="flex items-center gap-4 text-xs text-text-secondary">
                      <span>{{ formatBytes(task.downloaded) }} / {{ formatBytes(task.fileSize) }}</span>
                      <span v-if="task.status === 'downloading'">{{ formatSpeed(task.speed) }}</span>
                      <span>{{ task.progress.toFixed(1) }}%</span>
                    </div>
                  </div>
                  
                  <!-- Actions -->
                  <div class="flex items-center gap-1 flex-shrink-0">
                    <NTooltip v-if="task.status === 'downloading'">
                      <template #trigger>
                        <NButton size="tiny" quaternary circle @click="handlePause(task)">
                          <PauseOutline class="w-4 h-4" />
                        </NButton>
                      </template>
                      暂停
                    </NTooltip>
                    
                    <NTooltip v-if="task.status === 'paused'">
                      <template #trigger>
                        <NButton size="tiny" quaternary circle @click="handleResume(task)">
                          <PlayOutline class="w-4 h-4" />
                        </NButton>
                      </template>
                      继续
                    </NTooltip>
                    
                    <NTooltip>
                      <template #trigger>
                        <NButton size="tiny" quaternary circle @click="handleCancel(task)">
                          <CloseOutline class="w-4 h-4" />
                        </NButton>
                      </template>
                      取消
                    </NTooltip>
                  </div>
                </div>
              </NCard>
            </div>
          </div>
        </NTabPane>
        
        <!-- Completed Tab -->
        <NTabPane name="completed" tab="已完成" class="h-full">
          <div class="p-4 h-full overflow-auto">
            <NEmpty 
              v-if="store.completedDownloads.length === 0" 
              description="暂无已完成的下载"
              class="h-full flex items-center justify-center"
            >
              <template #icon>
                <CheckmarkCircleOutline class="w-12 h-12 text-text-secondary opacity-50" />
              </template>
            </NEmpty>
            
            <div v-else class="space-y-3">
              <NCard 
                v-for="task in store.completedDownloads" 
                :key="task.id"
                :bordered="false"
                class="bg-secondary"
                size="small"
              >
                <div class="flex items-center gap-4">
                  <!-- Cover -->
                  <div class="w-24 h-14 bg-tertiary rounded overflow-hidden flex-shrink-0">
                    <img 
                      v-if="task.cover" 
                      :src="task.cover" 
                      :alt="task.title"
                      class="w-full h-full object-cover"
                    />
                    <div v-else class="w-full h-full flex items-center justify-center">
                      <CheckmarkCircleOutline class="w-6 h-6 text-green-500" />
                    </div>
                  </div>
                  
                  <!-- Info -->
                  <div class="flex-1 min-w-0">
                    <div class="flex items-center gap-2 mb-1">
                      <span class="text-sm font-medium truncate">{{ task.title }}</span>
                      <NTag type="success" size="tiny">已完成</NTag>
                    </div>
                    
                    <div class="text-xs text-text-secondary">
                      <span>{{ formatBytes(task.fileSize) }}</span>
                      <span class="mx-2">·</span>
                      <span>{{ task.fileName }}</span>
                    </div>
                  </div>
                  
                  <!-- Actions -->
                  <div class="flex items-center gap-1 flex-shrink-0">
                    <NTooltip>
                      <template #trigger>
                        <NButton size="tiny" quaternary circle @click="handleOpenFile(task)">
                          <FolderOpenOutline class="w-4 h-4" />
                        </NButton>
                      </template>
                      打开文件
                    </NTooltip>
                    
                    <NTooltip>
                      <template #trigger>
                        <NButton size="tiny" quaternary circle @click="handleRemove(task)">
                          <TrashOutline class="w-4 h-4" />
                        </NButton>
                      </template>
                      删除记录
                    </NTooltip>
                  </div>
                </div>
              </NCard>
            </div>
          </div>
        </NTabPane>
      </NTabs>
    </div>
  </div>
</template>

<style scoped>
.downloads-tabs :deep(.n-tabs-nav) {
  padding-left: 1rem;
  padding-right: 1rem;
}
</style>

