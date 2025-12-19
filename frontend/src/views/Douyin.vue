<script setup lang="ts">
import { computed, ref } from 'vue'
import { NAlert, NButton, NCard, NEmpty, NInput, NSelect, NSpin, NTag, useMessage } from 'naive-ui'
import {
  CloudDownloadOutline,
  ImagesOutline,
  LinkOutline,
  LogoTiktok,
  PersonOutline,
  PlayCircleOutline,
  SearchOutline,
  TimeOutline,
  ListOutline
} from '@vicons/ionicons5'
import type { DouyinItem } from '@/types'
import { GetDouyinVideoInfo, DownloadDouyinVideo, DownloadDouyinAlbumPartial } from '../../wailsjs/go/main/App'
import ProxiedImage from '@/components/ProxiedImage.vue'
import ImageSelector from '@/components/ImageSelector.vue'

const message = useMessage()

const url = ref('')
const loading = ref(false)
const downloading = ref(false)
const downloadingPartial = ref(false)
const showImageSelector = ref(false)
const douyinItem = ref<DouyinItem | null>(null)
const selectedQuality = ref<string | null>(null)
const error = ref('')

const qualityOptions = ref<{ label: string; value: string }[]>([])

const isVideo = computed(() => douyinItem.value?.Type === 'video')
const isAlbum = computed(() => douyinItem.value?.Type === 'album')
const canDownload = computed(() => {
  if (!douyinItem.value) return false
  return isVideo.value ? !!selectedQuality.value : true
})

// Get estimated file size for selected quality
const selectedStreamSize = computed(() => {
  if (!douyinItem.value || !selectedQuality.value) return null
  const stream = douyinItem.value.Streams.find(s => s.QualityKey === selectedQuality.value)
  return stream?.Size || null
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

function formatDuration(seconds: number) {
  const mins = Math.floor(seconds / 60)
  const secs = seconds % 60
  return `${mins}:${secs.toString().padStart(2, '0')}`
}

async function fetchDouyinInfo() {
  if (!url.value.trim()) {
    message.warning('请输入抖音分享链接或文本')
    return
  }

  loading.value = true
  error.value = ''
  douyinItem.value = null
  selectedQuality.value = null
  qualityOptions.value = []

  try {
    const info = (await GetDouyinVideoInfo(url.value)) as DouyinItem
    douyinItem.value = info

    if (info.Type === 'video') {
      qualityOptions.value = info.Streams.map((s) => ({
        label: `${s.QualityName} (${s.Width}x${s.Height})`,
        value: s.QualityKey
      }))
      selectedQuality.value = info.Streams[0]?.QualityKey || null
    }
  } catch (e: any) {
    error.value = e.message || '获取抖音信息失败'
    message.error(error.value)
  } finally {
    loading.value = false
  }
}

async function downloadDouyin() {
  if (!douyinItem.value) return
  const qualityKey = isVideo.value ? selectedQuality.value || '' : ''
  if (isVideo.value && !qualityKey) {
    message.warning('请选择清晰度')
    return
  }

  downloading.value = true
  try {
    await DownloadDouyinVideo(url.value, qualityKey)
    message.success('已添加到下载队列')
  } catch (e: any) {
    message.error(e.message || '下载失败')
  } finally {
    downloading.value = false
  }
}

async function downloadPartialAlbum(indices: number[]) {
  if (!douyinItem.value || indices.length === 0) return
  downloadingPartial.value = true
  try {
    await DownloadDouyinAlbumPartial(url.value, indices)
    message.success('已添加到下载队列')
  } catch (e: any) {
    message.error(e.message || '下载失败')
  } finally {
    downloadingPartial.value = false
  }
}

function handleKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter') {
    fetchDouyinInfo()
  }
}
</script>

<template>
  <div class="douyin-page h-full flex flex-col">
    <!-- Header -->
    <div class="header p-4 border-b border-border">
      <h2 class="text-xl font-semibold mb-4 flex items-center gap-2">
        <LogoTiktok class="w-6 h-6" />
        抖音视频下载
      </h2>

      <!-- URL Input -->
      <div class="flex gap-3">
        <NInput
          v-model:value="url"
          placeholder="粘贴抖音分享链接/文本，回车快速解析"
          clearable
          size="large"
          class="flex-1"
          @keydown="handleKeydown"
        >
          <template #prefix>
            <SearchOutline class="w-5 h-5 text-text-secondary" />
          </template>
        </NInput>

        <NButton type="primary" size="large" :loading="loading" @click="fetchDouyinInfo">
          解析
        </NButton>
      </div>

      <NAlert v-if="error" type="error" class="mt-3" :show-icon="true">
        {{ error }}
      </NAlert>
    </div>

    <!-- Content -->
    <div class="content flex-1 overflow-auto p-4">
      <div v-if="loading" class="h-full flex items-center justify-center">
        <NSpin size="large" />
      </div>

      <div v-else-if="!douyinItem" class="h-full flex items-center justify-center">
        <NEmpty description="输入抖音链接开始解析">
          <template #icon>
            <LinkOutline class="w-12 h-12 text-text-secondary opacity-50" />
          </template>
          <template #extra>
            <p class="text-text-secondary text-sm mt-2">
              支持抖音分享文本或短链接，例如：<br />
              复制口令或 https://v.douyin.com/xxxx/
            </p>
          </template>
        </NEmpty>
      </div>

      <div v-else class="max-w-4xl mx-auto">
        <NCard :bordered="false" class="bg-secondary">
          <div class="flex gap-6 flex-col md:flex-row">
            <!-- Cover -->
            <div class="w-60 flex-shrink-0">
              <div class="aspect-[3/4] bg-tertiary rounded-lg overflow-hidden relative">
                <ProxiedImage
                  v-if="douyinItem.Cover"
                  :src="douyinItem.Cover"
                  :alt="douyinItem.Title"
                  class="w-full h-full object-cover"
                >
                  <template #placeholder>
                    <div class="w-full h-full flex items-center justify-center">
                      <LogoTiktok class="w-16 h-16 text-text-secondary opacity-50" />
                    </div>
                  </template>
                </ProxiedImage>
                <div v-else class="w-full h-full flex items-center justify-center">
                  <LogoTiktok class="w-16 h-16 text-text-secondary opacity-50" />
                </div>

                <!-- Type Badge -->
                <div class="absolute top-2 right-2">
                  <NTag v-if="isVideo" type="success" size="small">
                    <template #icon><PlayCircleOutline /></template>
                    视频
                  </NTag>
                  <NTag v-else type="warning" size="small">
                    <template #icon><ImagesOutline /></template>
                    图集
                  </NTag>
                </div>
              </div>
            </div>

            <!-- Info -->
            <div class="flex-1 flex flex-col">
              <h3 class="text-lg font-semibold mb-3">{{ douyinItem.Title || '无标题' }}</h3>

              <div class="flex items-center gap-4 text-sm text-text-secondary mb-3 flex-wrap">
                <span class="flex items-center gap-1">
                  <PersonOutline class="w-4 h-4" />
                  {{ douyinItem.Author }}
                </span>
                <span v-if="isVideo" class="flex items-center gap-1">
                  <TimeOutline class="w-4 h-4" />
                  {{ formatDuration(douyinItem.Duration) }}
                </span>
                <span v-else class="flex items-center gap-1">
                  <ImagesOutline class="w-4 h-4" />
                  {{ douyinItem.Images.length }} 张图片
                </span>
              </div>

              <div class="mt-auto flex items-center gap-4 flex-wrap">
                <div v-if="isVideo" class="flex items-center gap-2">
                  <span class="text-sm text-text-secondary">清晰度:</span>
                  <NSelect
                    v-model:value="selectedQuality"
                    :options="qualityOptions"
                    size="small"
                    style="width: 200px"
                  />
                  <span v-if="selectedStreamSize" class="text-sm text-text-secondary">
                    {{ formatFileSize(selectedStreamSize) }}
                  </span>
                </div>

                <div v-else class="flex items-center gap-2 text-sm text-text-secondary">
                  将下载为 ZIP 压缩包
                </div>

                <div class="flex gap-2">
                  <NButton 
                    v-if="isAlbum"
                    secondary
                    :loading="downloadingPartial"
                    :disabled="!canDownload"
                    @click="showImageSelector = true"
                  >
                    <template #icon>
                      <ListOutline class="w-4 h-4" />
                    </template>
                    选择部分
                  </NButton>

                  <NButton type="primary" :loading="downloading" :disabled="!canDownload" @click="downloadDouyin">
                    <template #icon>
                      <CloudDownloadOutline class="w-4 h-4" />
                    </template>
                    {{ isVideo ? '下载视频' : '下载图集' }}
                  </NButton>
                </div>
              </div>
            </div>
          </div>
        </NCard>

        <div v-if="isAlbum && douyinItem.Images.length > 0" class="mt-6">
          <h4 class="text-lg font-semibold mb-3">图片预览 ({{ douyinItem.Images.length }})</h4>
          <div class="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 gap-4">
            <div
              v-for="(img, index) in douyinItem.Images"
              :key="index"
              class="aspect-[3/4] bg-secondary rounded-lg overflow-hidden"
            >
              <ProxiedImage
                :src="img.URL"
                class="w-full h-full object-cover hover:opacity-90 transition-opacity cursor-pointer"
              />
            </div>
          </div>
        </div>

        <ImageSelector
          v-model:show="showImageSelector"
          :images="douyinItem?.Images || []"
          :title="douyinItem?.Title || '选择图片'"
          @select="downloadPartialAlbum"
        />
      </div>
    </div>
  </div>
</template>

<style scoped>
</style>
