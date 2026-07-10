<script setup lang="ts">
import { computed } from 'vue'
import { useAppStore } from '@/stores/app'
import {
  NAlert, NButton, NEmpty, NTabs, NTabPane, useMessage
} from 'naive-ui'
import {
  FolderOpenOutline,
  CheckmarkCircleOutline,
  CloudDownloadOutline,
  AlertCircleOutline
} from '@vicons/ionicons5'
import type { DownloadTask } from '@/types'
import { OpenFile } from '../../wailsjs/go/main/App'
import DownloadTaskCard from '@/components/DownloadTaskCard.vue'

const store = useAppStore()
const message = useMessage()

const activeTab = computed(() => {
  if (store.pendingDownloads.length > 0) return 'downloading'
  if (store.problemDownloads.length > 0) return 'problems'
  return 'completed'
})

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

async function handleRetry(task: DownloadTask) {
  try {
    await store.retryDownloadTask(task.id)
  } catch (e: any) {
    message.error(e.message || '重试失败')
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
  const artifact = task.artifacts?.find(item => item.kind === 'final' && item.primary)
    ?? task.artifacts?.find(item => item.kind === 'final')
  const path = artifact?.path || task.outputPolicy?.plannedFinalPath
  if (path) {
    try {
      await OpenFile(path)
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
      <NAlert
        v-if="store.legacyDownloadNotice"
        type="warning"
        title="旧版下载记录未导入"
        class="m-4 mb-0"
      >
        {{ store.legacyDownloadNotice.message }}
        <div class="mt-1 text-xs break-all">
          已保留：{{ store.legacyDownloadNotice.legacyPath }}
        </div>
      </NAlert>
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
              <DownloadTaskCard
                v-for="task in store.pendingDownloads"
                :key="task.id"
                :task="task"
                :stop-operation="store.downloadStopOperations[task.id]"
                mode="downloading"
                @pause="handlePause"
                @resume="handleResume"
                @cancel="handleCancel"
                @remove="handleRemove"
              />
            </div>
          </div>
        </NTabPane>

        <!-- Problems Tab -->
        <NTabPane name="problems" tab="失败/已取消" class="h-full">
          <div class="p-4 h-full overflow-auto">
            <NEmpty
              v-if="store.problemDownloads.length === 0"
              description="暂无失败或已取消的下载"
              class="h-full flex items-center justify-center"
            >
              <template #icon>
                <AlertCircleOutline class="w-12 h-12 text-text-secondary opacity-50" />
              </template>
            </NEmpty>

            <div v-else class="space-y-3">
              <DownloadTaskCard
                v-for="task in store.problemDownloads"
                :key="task.id"
                :task="task"
                :stop-operation="store.downloadStopOperations[task.id]"
                mode="problem"
                @retry="handleRetry"
                @remove="handleRemove"
              />
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
              <DownloadTaskCard
                v-for="task in store.completedDownloads"
                :key="task.id"
                :task="task"
                :stop-operation="store.downloadStopOperations[task.id]"
                mode="completed"
                @open-file="handleOpenFile"
                @remove="handleRemove"
              />
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
