<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { 
  NModal, NCard, NDataTable, NButton, NSpace, 
  NCheckbox, NTag, NEmpty, NScrollbar
} from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import type { BilibiliPart } from '@/types'
import { TimeOutline, ListOutline } from '@vicons/ionicons5'

const props = defineProps<{
  show: boolean
  parts: BilibiliPart[]
  videoTitle: string
}>()

const emit = defineEmits<{
  (e: 'update:show', value: boolean): void
  (e: 'select', parts: number[]): void
  (e: 'cancel'): void
}>()

const selectedParts = ref<number[]>([])
const selectAll = ref(false)

// Reset selection when modal opens
watch(() => props.show, (newVal) => {
  if (newVal) {
    selectedParts.value = []
    selectAll.value = false
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

const hasSelection = computed(() => selectedParts.value.length > 0)

const totalDuration = computed(() => {
  return selectedParts.value.reduce((sum, index) => {
    return sum + (props.parts[index]?.duration || 0)
  }, 0)
})

function formatDuration(seconds: number) {
  const mins = Math.floor(seconds / 60)
  const secs = seconds % 60
  return `${mins}:${secs.toString().padStart(2, '0')}`
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

function confirm() {
  emit('select', [...selectedParts.value].sort((a, b) => a - b))
  emit('update:show', false)
}

function cancel() {
  emit('cancel')
  emit('update:show', false)
}

const columns: DataTableColumns<BilibiliPart & { index: number }> = [
  {
    type: 'selection',
    disabled: () => false,
  },
  {
    title: 'P',
    key: 'page',
    width: 50,
    render: (row) => `P${row.page}`
  },
  {
    title: '标题',
    key: 'partName',
    ellipsis: {
      tooltip: true
    }
  },
  {
    title: '时长',
    key: 'duration',
    width: 80,
    render: (row) => formatDuration(row.duration)
  }
]

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

// Row key function for NDataTable - defined here to avoid TypeScript syntax in template
const getRowKey = (row: BilibiliPart & { index: number }) => row.index
</script>

<template>
  <NModal 
    :show="show" 
    preset="card"
    style="width: 600px; max-height: 80vh"
    :title="`选择分P - ${videoTitle}`"
    :mask-closable="true"
    @update:show="emit('update:show', $event)"
  >
    <template #header-extra>
      <NTag type="info" size="small">
        <template #icon>
          <ListOutline class="w-3 h-3" />
        </template>
        共 {{ parts.length }} P
      </NTag>
    </template>

    <div class="part-selector-content">
      <!-- Empty state -->
      <NEmpty v-if="parts.length === 0" description="没有可用的分P" />

      <!-- Parts list -->
      <template v-else>
        <div class="mb-3 flex items-center justify-between">
          <NCheckbox v-model:checked="selectAll">
            全选
          </NCheckbox>
          <span v-if="hasSelection" class="text-sm text-gray-400">
            已选 {{ selectedParts.length }} P，
            总时长 {{ formatDuration(totalDuration) }}
          </span>
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
      <div class="flex justify-end gap-3">
        <NButton @click="cancel">取消</NButton>
        <NButton 
          type="primary" 
          :disabled="!hasSelection"
          @click="confirm"
        >
          下载选中 ({{ selectedParts.length }})
        </NButton>
      </div>
    </template>
  </NModal>
</template>

<style scoped>
.part-selector-content {
  min-height: 200px;
}
</style>
