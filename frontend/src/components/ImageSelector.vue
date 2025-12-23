<script setup lang="ts">
import { ref, computed, watch, nextTick } from 'vue'
import { NModal, NButton, NCheckbox, NTag, NEmpty } from 'naive-ui'
import { ImagesOutline, CloudDownloadOutline, CheckmarkCircle, PlayCircleOutline } from '@vicons/ionicons5'
import type { DouyinImage } from '@/types'
import ProxiedImage from './ProxiedImage.vue'
import { useVirtualGrid } from '@/composables/useVirtualGrid'

const props = defineProps<{
  show: boolean
  images: DouyinImage[]
  title: string
}>()

const emit = defineEmits<{
  (e: 'update:show', value: boolean): void
  (e: 'select', indices: number[]): void
  (e: 'cancel'): void
}>()

const selectedIndices = ref<number[]>([])
const selectAll = ref(false)
const scrollContainerRef = ref<HTMLElement | null>(null)

// Grid configuration
const COLUMNS = 4
const GAP = 12
const ITEM_WIDTH = 158
const ITEM_HEIGHT = Math.round(ITEM_WIDTH / (3 / 4))
const CONTAINER_HEIGHT = 400

const { totalHeight, rowHeight, visibleItems, handleScroll, resetScroll } = useVirtualGrid(
  () => props.images,
  () => ({
    columns: COLUMNS,
    gap: GAP,
    itemHeight: ITEM_HEIGHT,
    containerHeight: CONTAINER_HEIGHT,
    overscan: 3
  })
)

// Reset when modal opens
watch(() => props.show, (newVal) => {
  if (newVal) {
    selectedIndices.value = []
    selectAll.value = false
    resetScroll()
    nextTick(() => {
      if (scrollContainerRef.value) {
        scrollContainerRef.value.scrollTop = 0
      }
    })
  }
})

// SelectAll logic
watch(selectAll, (newVal) => {
  if (newVal) {
    selectedIndices.value = props.images.map((_, index) => index)
  } else if (selectedIndices.value.length === props.images.length) {
    selectedIndices.value = []
  }
})

watch(selectedIndices, (newVal) => {
  selectAll.value = newVal.length === props.images.length && props.images.length > 0
}, { deep: true })

const hasSelection = computed(() => selectedIndices.value.length > 0)
const selectedSet = computed(() => new Set(selectedIndices.value))

function isSelected(index: number): boolean {
  return selectedSet.value.has(index)
}

function toggleImage(index: number) {
  const idx = selectedIndices.value.indexOf(index)
  if (idx === -1) {
    selectedIndices.value.push(index)
  } else {
    selectedIndices.value.splice(idx, 1)
  }
}

function confirm() {
  emit('select', [...selectedIndices.value].sort((a, b) => a - b))
  emit('update:show', false)
}

function cancel() {
  emit('cancel')
  emit('update:show', false)
}
</script>

<template>
  <NModal
    :show="show"
    preset="card"
    style="width: 700px; max-height: 80vh"
    :title="title || '选择图片'"
    :mask-closable="true"
    @update:show="emit('update:show', $event)"
  >
    <template #header-extra>
      <NTag type="info" size="small">
        <template #icon>
          <ImagesOutline class="w-3 h-3" />
        </template>
        共 {{ images.length }} 张
      </NTag>
    </template>

    <div class="image-selector-content flex flex-col h-full">
      <NEmpty v-if="images.length === 0" description="没有可用的图片" class="py-8">
        <template #icon>
          <ImagesOutline class="w-12 h-12 text-text-secondary opacity-50" />
        </template>
      </NEmpty>

      <template v-else>
        <div class="mb-3 flex items-center justify-between">
          <NCheckbox v-model:checked="selectAll">全选</NCheckbox>
          <div v-if="hasSelection" class="text-sm text-text-secondary">
            已选 <span class="text-primary font-medium">{{ selectedIndices.length }}</span> 张
          </div>
        </div>

        <div
          ref="scrollContainerRef"
          class="virtual-scroll-container overflow-auto pr-2"
          style="max-height: 50vh; height: 400px"
          @scroll.passive="handleScroll"
        >
          <div class="relative" :style="{ height: `${totalHeight}px` }">
            <div
              v-for="item in visibleItems"
              :key="item.index"
              class="absolute cursor-pointer rounded-lg overflow-hidden border-2 group"
              :class="isSelected(item.index) ? 'border-primary' : 'border-transparent hover:border-gray-500'"
              :style="{
                left: `${item.col * (ITEM_WIDTH + GAP)}px`,
                top: `${item.row * rowHeight}px`,
                width: `${ITEM_WIDTH}px`,
                height: `${ITEM_HEIGHT}px`
              }"
              @click="toggleImage(item.index)"
            >
              <div class="w-full h-full relative bg-dark-200">
                <ProxiedImage
                  :src="item.data.URL"
                  class="w-full h-full object-cover"
                />

                <div
                  class="absolute inset-0 flex items-start justify-end p-2 pointer-events-none"
                  :class="isSelected(item.index) ? 'bg-primary/20' : 'bg-black/0 group-hover:bg-black/10'"
                >
                  <div
                    class="w-5 h-5 rounded flex items-center justify-center shadow-sm"
                    :class="isSelected(item.index) ? 'bg-primary text-white' : 'bg-black/40 text-transparent group-hover:bg-black/60'"
                  >
                    <CheckmarkCircle v-if="isSelected(item.index)" class="w-4 h-4" />
                  </div>
                </div>

                <div class="absolute bottom-1 right-1 bg-black/60 text-white text-xs px-1.5 py-0.5 rounded backdrop-blur-sm flex items-center gap-1">
                  <PlayCircleOutline v-if="item.data.VideoURL" class="w-3 h-3" />
                  {{ item.index + 1 }}
                </div>
              </div>
            </div>
          </div>
        </div>
      </template>
    </div>

    <template #footer>
      <div class="flex justify-end gap-3">
        <NButton @click="cancel">取消</NButton>
        <NButton type="primary" :disabled="!hasSelection" @click="confirm">
          <template #icon>
            <CloudDownloadOutline class="w-4 h-4" />
          </template>
          下载选中 ({{ selectedIndices.length }})
        </NButton>
      </div>
    </template>
  </NModal>
</template>

<style scoped>
.text-primary {
  color: var(--primary-color, #18a058);
}
.border-primary {
  border-color: var(--primary-color, #18a058);
}
.bg-primary {
  background-color: var(--primary-color, #18a058);
}
</style>
