<script setup lang="ts">
import { ref, computed, watch, h } from 'vue'
import { 
  NModal, NCard, NDataTable, NButton, NSpace, 
  NCheckbox, NTag, NEmpty, NScrollbar, NSelect, NSpin
} from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import type { BilibiliPart, BilibiliStream } from '@/types'
import { TimeOutline, ListOutline, CloudDownloadOutline } from '@vicons/ionicons5'

const props = defineProps<{
  show: boolean
  parts: BilibiliPart[]
  videoTitle: string
  streams?: BilibiliStream[]  // Available quality options from first/current part
  selectedQuality?: number    // Currently selected quality
  isBangumi?: boolean
  currentPartIndex?: number
}>()

const emit = defineEmits<{
  (e: 'update:show', value: boolean): void
  (e: 'select', parts: number[]): void
  (e: 'cancel'): void
  (e: 'update:selectedQuality', quality: number): void
}>()

const selectedParts = ref<number[]>([])
const selectAll = ref(false)
const localSelectedQuality = ref<number | null>(null)

// Reset selection when modal opens
watch(() => props.show, (newVal) => {
  if (newVal) {
    const currentIndex = props.currentPartIndex ?? 0
    selectedParts.value = props.isBangumi && currentIndex >= 0 && currentIndex < props.parts.length
      ? [currentIndex]
      : []
    selectAll.value = false
    // Initialize local quality from prop
    if (props.selectedQuality !== undefined) {
      localSelectedQuality.value = props.selectedQuality
    } else if (props.streams && props.streams.length > 0) {
      localSelectedQuality.value = props.streams[0].quality
    } else {
      localSelectedQuality.value = null
    }
  }
})

// Sync quality changes back to parent
watch(localSelectedQuality, (newVal) => {
  if (newVal !== null) {
    emit('update:selectedQuality', newVal)
  }
})

// Watch selectAll changes
watch(selectAll, (newVal) => {
  if (newVal) {
    selectedParts.value = props.parts.map((_, index) => index)
  } else if (selectedParts.value.length === props.parts.length) {
    selectedParts.value = []
  }
})

// Watch selectedParts to update selectAll
watch(selectedParts, (newVal) => {
  selectAll.value = newVal.length === props.parts.length && props.parts.length > 0
}, { deep: true })

// Quality options for selector
const qualityOptions = computed(() => {
  if (!props.streams || props.streams.length === 0) {
    if (props.isBangumi && props.selectedQuality !== undefined) {
      return [{ label: '自动/最高可用', value: props.selectedQuality }]
    }
    return []
  }
  return props.streams.map(s => ({
    label: s.qualityName,
    value: s.quality
  }))
})

const hasSelection = computed(() => selectedParts.value.length > 0)
const canConfirm = computed(() => hasSelection.value && localSelectedQuality.value !== null)
const unitLabel = computed(() => props.isBangumi ? '集' : 'P')
const selectorTitle = computed(() => props.isBangumi ? '选择集数' : '选择分P')
const allCountLabel = computed(() => props.isBangumi ? `共 ${props.parts.length} 集` : `共 ${props.parts.length} P`)

const totalDuration = computed(() => {
  return selectedParts.value.reduce((sum, index) => {
    return sum + (props.parts[index]?.duration || 0)
  }, 0)
})

// Get estimated size for a part at the selected quality
function getPartSize(part: BilibiliPart): number | null {
  if (!part.streams || part.streams.length === 0 || localSelectedQuality.value === null) {
    return null
  }
  const stream = part.streams.find(s => s.quality === localSelectedQuality.value)
  return stream?.size || null
}

// Calculate total size of selected parts
const totalSelectedSize = computed(() => {
  if (localSelectedQuality.value === null) return null
  
  let total = 0
  let hasAnySize = false
  
  for (const index of selectedParts.value) {
    const part = props.parts[index]
    if (part) {
      const size = getPartSize(part)
      if (size && size > 0) {
        total += size
        hasAnySize = true
      }
    }
  }
  
  return hasAnySize ? total : null
})

// Check if any part has stream info
const hasPartStreams = computed(() => {
  return props.parts.some(p => p.streams && p.streams.length > 0)
})

function formatDuration(seconds: number) {
  const mins = Math.floor(seconds / 60)
  const secs = seconds % 60
  return `${mins}:${secs.toString().padStart(2, '0')}`
}

function formatFileSize(bytes?: number | null) {
  if (!bytes || bytes <= 0) return '-'
  const units = ['B', 'KB', 'MB', 'GB']
  let unitIndex = 0
  let size = bytes
  while (size >= 1024 && unitIndex < units.length - 1) {
    size /= 1024
    unitIndex++
  }
  return `${size.toFixed(1)} ${units[unitIndex]}`
}

function togglePart(index: number) {
  const idx = selectedParts.value.indexOf(index)
  if (idx === -1) {
    selectedParts.value.push(index)
  } else {
    selectedParts.value.splice(idx, 1)
  }
}

function isSelected(index: number) {
  return selectedParts.value.includes(index)
}

function badgeTagType(badge?: string): 'default' | 'info' | 'success' | 'warning' {
  if (badge === '会员') return 'warning'
  if (badge === '限免') return 'success'
  if (badge === '预告') return 'info'
  return 'default'
}

function confirm() {
  emit('select', [...selectedParts.value].sort((a, b) => a - b))
  emit('update:show', false)
}

function cancel() {
  emit('cancel')
  emit('update:show', false)
}

const columns = computed<DataTableColumns<BilibiliPart & { index: number }>>(() => {
  const cols: DataTableColumns<BilibiliPart & { index: number }> = [
    {
      type: 'selection',
      disabled: () => false,
    },
    {
      title: unitLabel.value,
      key: 'page',
      width: props.isBangumi ? 60 : 50,
      render: (row) => props.isBangumi ? `${row.page}` : `P${row.page}`
    },
    {
      title: '标题',
      key: 'partName',
      ellipsis: {
        tooltip: true
      },
      render: (row) => h('div', { class: 'part-title-cell' }, [
        h('span', row.partName || `${unitLabel.value}${row.page}`),
        row.badge ? h(NTag, { size: 'tiny', type: badgeTagType(row.badge), class: 'ml-2' }, { default: () => row.badge }) : null
      ])
    },
    {
      title: '时长',
      key: 'duration',
      width: 80,
      render: (row) => formatDuration(row.duration)
    }
  ]
  
  // Add size column if any part has stream info
  if (hasPartStreams.value) {
    cols.push({
      title: '预估大小',
      key: 'size',
      width: 100,
      render: (row) => {
        const size = getPartSize(row)
        return size ? formatFileSize(size) : '-'
      }
    })
  }
  
  return cols
})

const tableData = computed(() => {
  return props.parts.map((part, index) => ({
    ...part,
    index
  }))
})

const checkedRowKeys = computed({
  get: () => selectedParts.value,
  set: (val) => {
    selectedParts.value = val as number[]
  }
})

// Row key function for NDataTable
const getRowKey = (row: BilibiliPart & { index: number }) => row.index
</script>

<template>
  <NModal 
    :show="show" 
    preset="card"
    style="width: 700px; max-height: 80vh"
    :title="`${selectorTitle} - ${videoTitle}`"
    :mask-closable="true"
    @update:show="emit('update:show', $event)"
  >
    <template #header-extra>
      <NTag type="info" size="small">
        <template #icon>
          <ListOutline class="w-3 h-3" />
        </template>
        {{ allCountLabel }}
      </NTag>
    </template>

    <div class="part-selector-content">
      <!-- Empty state -->
      <NEmpty v-if="parts.length === 0" :description="isBangumi ? '没有可用的剧集' : '没有可用的分P'">
        <template #icon>
          <ListOutline class="w-12 h-12 text-text-secondary opacity-50" />
        </template>
      </NEmpty>

      <!-- Parts list -->
      <template v-else>
        <!-- Quality selector and selection info -->
        <div class="mb-3 flex items-center justify-between flex-wrap gap-2">
          <div class="flex items-center gap-3">
            <NCheckbox v-model:checked="selectAll">
              全选
            </NCheckbox>
            
            <!-- Quality selector -->
            <div v-if="qualityOptions.length > 0" class="flex items-center gap-2">
              <span class="text-sm text-text-secondary">画质:</span>
              <NSelect 
                v-model:value="localSelectedQuality"
                :options="qualityOptions"
                size="small"
                style="width: 130px"
              />
            </div>
          </div>
          
          <!-- Selection summary -->
          <div v-if="hasSelection" class="text-sm text-text-secondary flex items-center gap-2">
            <span>已选 {{ selectedParts.length }} {{ unitLabel }}</span>
            <span>·</span>
            <span>总时长 {{ formatDuration(totalDuration) }}</span>
            <template v-if="totalSelectedSize">
              <span>·</span>
              <span class="text-primary">预估 {{ formatFileSize(totalSelectedSize) }}</span>
            </template>
          </div>
        </div>

        <NScrollbar style="max-height: 400px">
          <NDataTable
            :columns="columns"
            :data="tableData"
            :row-key="getRowKey"
            v-model:checked-row-keys="checkedRowKeys"
            size="small"
            :bordered="false"
          />
        </NScrollbar>
      </template>
    </div>

    <template #footer>
      <div class="flex justify-between items-center">
        <div class="text-sm text-text-secondary">
          <template v-if="hasSelection && totalSelectedSize">
            总计: {{ formatFileSize(totalSelectedSize) }}
          </template>
        </div>
        <div class="flex gap-3">
          <NButton @click="cancel">取消</NButton>
          <NButton 
            type="primary" 
            :disabled="!canConfirm"
            @click="confirm"
          >
            <template #icon>
              <CloudDownloadOutline class="w-4 h-4" />
            </template>
            下载选中 ({{ selectedParts.length }})
          </NButton>
        </div>
      </div>
    </template>
  </NModal>
</template>

<style scoped>
.part-selector-content {
  min-height: 200px;
}

.text-primary {
  color: var(--primary-color, #18a058);
}

.part-title-cell {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.part-title-cell span:first-child {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
