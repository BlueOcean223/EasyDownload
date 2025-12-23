<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount, watch, nextTick } from 'vue'
import { ImagesOutline, PlayCircleOutline } from '@vicons/ionicons5'
import ProxiedImage from './ProxiedImage.vue'
import type { DouyinImage } from '@/types'
import { useVirtualGrid } from '@/composables/useVirtualGrid'

const props = defineProps<{
  images: DouyinImage[]
}>()

const emit = defineEmits<{
  (e: 'click', index: number): void
}>()

const containerRef = ref<HTMLElement | null>(null)
const containerWidth = ref(0)
const containerHeight = ref(600)

// Constants
const RESIZE_DEBOUNCE = 150
const GAP = 16
const ITEM_ASPECT_RATIO = 3 / 4

// Grid configuration - matches Tailwind breakpoints
const getColumnsCount = () => {
  const width = window.innerWidth
  if (width >= 1024) return 5
  if (width >= 768) return 4
  if (width >= 640) return 3
  return 2
}

const columnsCount = ref(getColumnsCount())

const itemWidth = computed(() => {
  if (containerWidth.value <= 0) return 150
  return (containerWidth.value - GAP * (columnsCount.value - 1)) / columnsCount.value
})

const itemHeight = computed(() => {
  if (itemWidth.value <= 0) return 200
  return itemWidth.value / ITEM_ASPECT_RATIO
})

const { totalHeight, rowHeight, visibleItems, handleScroll, resetScroll } = useVirtualGrid(
  () => props.images,
  () => ({
    columns: columnsCount.value,
    gap: GAP,
    itemHeight: itemHeight.value,
    containerHeight: containerHeight.value,
    overscan: 3
  })
)

function updateContainerSize() {
  if (containerRef.value) {
    containerWidth.value = containerRef.value.clientWidth
    containerHeight.value = containerRef.value.clientHeight
  }
}

let resizeTimeout: number | null = null
const handleResize = () => {
  if (resizeTimeout) clearTimeout(resizeTimeout)
  resizeTimeout = window.setTimeout(() => {
    columnsCount.value = getColumnsCount()
    updateContainerSize()
  }, RESIZE_DEBOUNCE)
}

watch(() => props.images, () => {
  resetScroll()
  nextTick(() => {
    if (containerRef.value) {
      containerRef.value.scrollTop = 0
    }
  })
})

onMounted(() => {
  window.addEventListener('resize', handleResize)
  updateContainerSize()
})

onBeforeUnmount(() => {
  if (resizeTimeout) clearTimeout(resizeTimeout)
  window.removeEventListener('resize', handleResize)
})
</script>

<template>
  <div
    ref="containerRef"
    class="relative overflow-auto"
    style="height: 600px; max-height: 70vh"
    @scroll.passive="handleScroll"
  >
    <div class="relative" :style="{ height: `${totalHeight}px` }">
      <template v-if="containerWidth > 0">
        <div
          v-for="item in visibleItems"
          :key="item.index"
          class="absolute bg-secondary rounded-lg overflow-hidden cursor-pointer"
          :style="{
            left: `${item.col * (itemWidth + GAP)}px`,
            top: `${item.row * rowHeight}px`,
            width: `${itemWidth}px`,
            height: `${itemHeight}px`
          }"
          @click="emit('click', item.index)"
        >
          <ProxiedImage
            :src="item.data.URL"
            class="w-full h-full object-cover hover:opacity-90 transition-opacity"
          >
            <template #placeholder>
              <div class="w-full h-full flex items-center justify-center bg-gray-100 dark:bg-gray-800">
                <ImagesOutline class="w-8 h-8 text-gray-400" />
              </div>
            </template>
          </ProxiedImage>
          <!-- Video indicator badge -->
          <div
            v-if="item.data.VideoURL"
            class="absolute bottom-2 right-2 bg-black/70 text-white px-2 py-1 rounded-md flex items-center gap-1 text-xs"
          >
            <PlayCircleOutline class="w-4 h-4" />
            <span>视频</span>
          </div>
        </div>
      </template>
    </div>
  </div>
</template>
