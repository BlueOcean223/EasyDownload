<script setup lang="ts">
import { computed, ref } from 'vue'
import { NAlert, NButton, NCard, NEmpty, NInput, NSelect, NSpin, NTag, useMessage } from 'naive-ui'
import {
  CloudDownloadOutline,
  ImagesOutline,
  LinkOutline,
  PersonOutline,
  PlayCircleOutline,
  SearchOutline,
  TimeOutline,
  ListOutline,
  BookOutline
} from '@vicons/ionicons5'
import type { XHSItem, XHSStream, DisplayImage } from '@/types'
import { GetXHSNoteInfo, DownloadXHSNote } from '../../wailsjs/go/main/App'
import ProxiedImage from '@/components/ProxiedImage.vue'
import ImageSelector from '@/components/ImageSelector.vue'
import ImagePreviewModal from '@/components/ImagePreviewModal.vue'
import LazyImageGrid from '@/components/LazyImageGrid.vue'

const message = useMessage()

const url = ref('')
const loading = ref(false)
const downloading = ref(false)
const showImageSelector = ref(false)
const showPreview = ref(false)
const previewStartIndex = ref(0)
const xhsItem = ref<XHSItem | null>(null)
const selectedQuality = ref<string | null>(null)
const error = ref('')

const qualityOptions = ref<{ label: string; value: string }[]>([])

const isVideo = computed(() => xhsItem.value?.Type === 'video')
const isImage = computed(() => xhsItem.value?.Type === 'image')
const canDownload = computed(() => {
  if (!xhsItem.value) return false
  return isVideo.value ? !!selectedQuality.value : true
})

const albumStats = computed(() => {
  const items = xhsItem.value?.Images || []
  return {
    total: items.length
  }
})

const albumSummary = computed(() => {
  const { total } = albumStats.value
  if (total === 0) return ''
  return `${total} 张图片`
})

// Get estimated file size for selected quality
const selectedStreamSize = computed(() => {
  if (!xhsItem.value || !selectedQuality.value) return null
  const stream = xhsItem.value.Streams.find(s => s.QualityKey === selectedQuality.value)
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
  if (!seconds) return '0:00'
  const mins = Math.floor(seconds / 60)
  const secs = seconds % 60
  return `${mins}:${secs.toString().padStart(2, '0')}`
}

function formatCodec(codec?: string) {
  const normalized = (codec || '').toLowerCase().replace(/[._-]/g, '')
  if (normalized === 'hevc' || normalized === 'h265') return 'H.265'
  if (normalized === 'avc' || normalized === 'h264') return 'H.264'
  if (normalized === 'h266' || normalized === 'vvc') return 'H.266'
  if (normalized === 'av1') return 'AV1'
  return codec || ''
}

function streamOptionLabel(stream: XHSStream) {
  const parts = [stream.QualityName || (stream.StreamType ? `Stream ${stream.StreamType}` : '默认')]
  if (stream.Width && stream.Height) parts.push(`${stream.Width}x${stream.Height}`)
  const codec = formatCodec(stream.VideoCodec)
  if (codec) parts.push(codec)
  if (stream.Weight) parts.push(`权重 ${stream.Weight}`)
  if (stream.FPS) parts.push(`${stream.FPS}fps`)
  return parts.join(' · ')
}

async function fetchXHSInfo() {
  if (!url.value.trim()) {
    message.warning('请输入小红书链接或文本')
    return
  }

  loading.value = true
  error.value = ''
  xhsItem.value = null
  selectedQuality.value = null
  qualityOptions.value = []

  try {
    const info = (await GetXHSNoteInfo(url.value)) as XHSItem
    xhsItem.value = info

    if (info.Type === 'video') {
      qualityOptions.value = info.Streams.map((s) => ({
        label: streamOptionLabel(s),
        value: s.QualityKey
      }))
      selectedQuality.value = info.Streams[0]?.QualityKey || null
    }
  } catch (e: any) {
    error.value = e.message || '获取小红书信息失败'
    message.error(error.value)
  } finally {
    loading.value = false
  }
}

async function downloadXHS(indices: number[] = []) {
  if (!xhsItem.value) return
  const qualityKey = isVideo.value ? selectedQuality.value || '' : ''
  if (isVideo.value && !qualityKey) {
    message.warning('请选择清晰度')
    return
  }

  downloading.value = true
  try {
    await DownloadXHSNote(xhsItem.value as any, indices, qualityKey, '')
    message.success('已添加到下载队列')
  } catch (e: any) {
    message.error(e.message || '下载失败')
  } finally {
    downloading.value = false
  }
}

function openPreview(index: number) {
  previewStartIndex.value = index
  showPreview.value = true
}

function handleKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter') {
    fetchXHSInfo()
  }
}

// XHS images are compatible with DisplayImage interface
const displayImages = computed((): DisplayImage[] => {
  return xhsItem.value?.Images || []
})
</script>

<template>
  <div class="xhs-page h-full flex flex-col">
    <!-- Header -->
    <div class="header p-4 border-b border-border">
      <h2 class="text-xl font-semibold mb-4 flex items-center gap-2">
        <BookOutline class="w-6 h-6 text-red-500" />
        小红书下载
      </h2>

      <!-- URL Input -->
      <div class="flex gap-3">
        <NInput
          v-model:value="url"
          placeholder="粘贴小红书分享链接/文本，回车快速解析"
          clearable
          size="large"
          class="flex-1"
          @keydown="handleKeydown"
        >
          <template #prefix>
            <SearchOutline class="w-5 h-5 text-text-secondary" />
          </template>
        </NInput>

        <NButton type="primary" size="large" :loading="loading" @click="fetchXHSInfo">
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

      <div v-else-if="!xhsItem" class="h-full flex items-center justify-center">
        <NEmpty description="输入小红书链接开始解析">
          <template #icon>
            <LinkOutline class="w-12 h-12 text-text-secondary opacity-50" />
          </template>
          <template #extra>
            <p class="text-text-secondary text-sm mt-2">
              支持小红书分享文本或短链接，例如：<br />
              http://xhslink.com/xxxx/
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
                  v-if="xhsItem.Cover"
                  :src="xhsItem.Cover"
                  :alt="xhsItem.Title"
                  class="w-full h-full object-cover"
                >
                  <template #placeholder>
                    <div class="w-full h-full flex items-center justify-center">
                      <BookOutline class="w-16 h-16 text-text-secondary opacity-50" />
                    </div>
                  </template>
                </ProxiedImage>
                <div v-else class="w-full h-full flex items-center justify-center">
                  <BookOutline class="w-16 h-16 text-text-secondary opacity-50" />
                </div>

                <!-- Type Badge -->
                <div class="absolute top-2 right-2">
                  <NTag v-if="isVideo" type="success" size="small">
                    <template #icon><PlayCircleOutline /></template>
                    视频
                  </NTag>
                  <NTag v-else type="warning" size="small">
                    <template #icon><ImagesOutline /></template>
                    图文
                  </NTag>
                </div>
              </div>
            </div>

            <!-- Info -->
            <div class="flex-1 flex flex-col">
              <h3 class="text-lg font-semibold mb-3">{{ xhsItem.Title || '无标题' }}</h3>
              
              <div class="text-sm text-text-secondary mb-3 line-clamp-2">
                {{ xhsItem.Desc }}
              </div>

              <div class="flex items-center gap-4 text-sm text-text-secondary mb-3 flex-wrap">
                <span class="flex items-center gap-1">
                  <PersonOutline class="w-4 h-4" />
                  {{ xhsItem.Author }}
                </span>
                <span v-if="isVideo && xhsItem.Streams[0]" class="flex items-center gap-1">
                  <TimeOutline class="w-4 h-4" />
                  {{ '视频' }}
                </span>
                <span v-else class="flex items-center gap-1">
                  <ImagesOutline class="w-4 h-4" />
                  {{ albumSummary }}
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
                    v-if="isImage"
                    secondary
                    :disabled="!canDownload"
                    @click="showImageSelector = true"
                  >
                    <template #icon>
                      <ListOutline class="w-4 h-4" />
                    </template>
                    选择图片
                  </NButton>

                  <NButton type="primary" :loading="downloading" :disabled="!canDownload" @click="() => downloadXHS()">
                    <template #icon>
                      <CloudDownloadOutline class="w-4 h-4" />
                    </template>
                    {{ isVideo ? '下载视频' : '下载全部' }}
                  </NButton>
                </div>
              </div>
            </div>
          </div>
        </NCard>

        <div v-if="isImage && displayImages.length > 0" class="mt-6">
          <h4 class="text-lg font-semibold mb-3">图片预览 ({{ displayImages.length }})</h4>
          <LazyImageGrid
            :images="displayImages"
            @click="openPreview"
          />
        </div>

        <ImageSelector
          v-model:show="showImageSelector"
          :images="displayImages"
          :title="xhsItem?.Title || '选择图片'"
          @select="(indices) => downloadXHS(indices)"
        />

        <ImagePreviewModal
          v-model:show="showPreview"
          :images="displayImages"
          :start-index="previewStartIndex"
        />
      </div>
    </div>
  </div>
</template>

<style scoped>
</style>
