<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { CloseOutline, ChevronBackOutline, ChevronForwardOutline } from '@vicons/ionicons5'
import type { DouyinImage } from '@/types'
import { useAppStore } from '@/stores/app'

const props = defineProps<{
  show: boolean
  images: DouyinImage[]
  startIndex?: number
}>()

const emit = defineEmits<{
  (e: 'update:show', value: boolean): void
}>()

const store = useAppStore()
const currentIndex = ref(0)
const direction = ref<'left' | 'right'>('right')
const tipText = ref('')
const showTip = ref(false)
let tipTimer: ReturnType<typeof setTimeout> | null = null

function showCenterTip(text: string) {
  tipText.value = text
  showTip.value = true
  if (tipTimer) clearTimeout(tipTimer)
  tipTimer = setTimeout(() => {
    showTip.value = false
  }, 1500)
}

// Get the API port from app info
const apiPort = computed(() => {
  return store.appInfo?.apiPort || 18899
})

function getProxiedUrl(url: string) {
  if (!url) return ''
  return `http://127.0.0.1:${apiPort.value}/api/proxy-image?url=${encodeURIComponent(url)}`
}

watch(() => props.show, (newVal) => {
  if (newVal) {
    currentIndex.value = props.startIndex || 0
    preloadImages()
    document.body.style.overflow = 'hidden'
  } else {
    document.body.style.overflow = ''
  }
}, { immediate: true })

watch(currentIndex, () => {
  preloadImages()
})

const currentImage = computed(() => props.images[currentIndex.value])

function close() {
  emit('update:show', false)
}

function prev() {
  if (currentIndex.value > 0) {
    direction.value = 'left'
    currentIndex.value--
  } else {
    showCenterTip('已经是第一张了')
  }
}

function next() {
  if (currentIndex.value < props.images.length - 1) {
    direction.value = 'right'
    currentIndex.value++
  } else {
    showCenterTip('已经是最后一张了')
  }
}

function handleKeydown(e: KeyboardEvent) {
  if (!props.show) return
  
  switch (e.key) {
    case 'Escape':
      close()
      break
    case 'ArrowLeft':
      prev()
      break
    case 'ArrowRight':
      next()
      break
  }
}

function preloadImages() {
  if (!props.images.length) return
  
  const indicesToPreload = [
    currentIndex.value - 1,
    currentIndex.value - 2,
    currentIndex.value + 1,
    currentIndex.value + 2
  ]
  
  indicesToPreload.forEach(idx => {
    if (idx >= 0 && idx < props.images.length) {
      const img = new Image()
      img.src = getProxiedUrl(props.images[idx].URL)
    }
  })
}

onMounted(() => {
  window.addEventListener('keydown', handleKeydown)
})

onUnmounted(() => {
  window.removeEventListener('keydown', handleKeydown)
  document.body.style.overflow = ''
})
</script>

<template>
  <Teleport to="body">
    <Transition name="fade">
      <div 
        v-if="show"
        class="fixed inset-0 z-[5000] bg-black/95 flex items-center justify-center select-none outline-none"
        tabindex="-1"
      >
        <!-- Top Bar -->
        <div class="absolute top-0 left-0 right-0 p-4 flex justify-between items-center z-20 text-white/90">
          <div class="text-lg font-medium tracking-wider">
            {{ currentIndex + 1 }} / {{ images.length }}
          </div>
          
          <button 
            class="p-2 rounded-full hover:bg-white/10 transition-colors cursor-pointer"
            @click="close"
            title="关闭 (Esc)"
          >
            <CloseOutline class="w-8 h-8" />
          </button>
        </div>

        <!-- Navigation Buttons -->
        <button 
          class="absolute left-4 top-1/2 -translate-y-1/2 p-3 rounded-full bg-black/20 hover:bg-white/10 text-white/90 z-20 transition-all cursor-pointer"
          :class="{ 'opacity-50': currentIndex === 0 }"
          @click="prev"
          title="上一张 (←)"
        >
          <ChevronBackOutline class="w-8 h-8" />
        </button>

        <button 
          class="absolute right-4 top-1/2 -translate-y-1/2 p-3 rounded-full bg-black/20 hover:bg-white/10 text-white/90 z-20 transition-all cursor-pointer"
          :class="{ 'opacity-50': currentIndex === images.length - 1 }"
          @click="next"
          title="下一张 (→)"
        >
          <ChevronForwardOutline class="w-8 h-8" />
        </button>

        <!-- Center Tip -->
        <Transition name="tip-fade">
          <div 
            v-if="showTip"
            class="absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 z-30 px-6 py-3 bg-black/70 rounded-lg text-white text-base pointer-events-none"
          >
            {{ tipText }}
          </div>
        </Transition>

        <!-- Main Image -->
        <div class="w-full h-full p-4 md:p-12 flex items-center justify-center overflow-hidden" @click.self="close">
          <Transition :name="direction === 'right' ? 'slide-left' : 'slide-right'" mode="out-in">
            <div :key="currentIndex" class="w-full h-full flex items-center justify-center">
              <img
                v-if="currentImage"
                :src="getProxiedUrl(currentImage.URL)"
                class="max-w-full max-h-full object-contain shadow-2xl"
                draggable="false"
                alt="preview"
              />
            </div>
          </Transition>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
/*
  Animation Timing Constants:
  - Duration: 0.3s (300ms) for quick snappy feel
  - Easing: ease-out for smooth deceleration

  Performance Targets:
  - Open/Close: < 150ms visual perception (actual transition 300ms)
  - Navigation: < 100ms visual perception
*/

.fade-enter-active,
.fade-leave-active {
  transition: opacity 0.3s cubic-bezier(0.4, 0, 0.2, 1);
}

.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}

/* Slide Animations - Simple left/right transitions */
.slide-left-enter-active,
.slide-left-leave-active,
.slide-right-enter-active,
.slide-right-leave-active {
  transition: all 0.3s cubic-bezier(0.25, 0.46, 0.45, 0.94);
}

.slide-left-enter-from {
  opacity: 0;
  transform: translateX(40px);
}
.slide-left-leave-to {
  opacity: 0;
  transform: translateX(-40px);
}

.slide-right-enter-from {
  opacity: 0;
  transform: translateX(-40px);
}
.slide-right-leave-to {
  opacity: 0;
  transform: translateX(40px);
}

/* Center Tip Animation */
.tip-fade-enter-active,
.tip-fade-leave-active {
  transition: opacity 0.2s ease;
}
.tip-fade-enter-from,
.tip-fade-leave-to {
  opacity: 0;
}
</style>
