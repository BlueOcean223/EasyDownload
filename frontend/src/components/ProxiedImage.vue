<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useAppStore } from '@/stores/app'
import { PlayCircleOutline } from '@vicons/ionicons5'
import { LogFrontend } from '../../wailsjs/go/main/App'

const props = defineProps<{
  src: string
  alt?: string
  class?: string
}>()

const store = useAppStore()
const hasError = ref(false)
const isLoading = ref(true)
const isVisible = ref(false)

// Global image cache - MODULE LEVEL, persists across all component instances
const globalImageCache = (() => {
  // Use window to ensure single instance across hot reloads
  const key = '__proxied_image_cache__'
  if (!(window as any)[key]) {
    (window as any)[key] = new Map<string, 'loading' | 'loaded' | 'error'>()
  }
  return (window as any)[key] as Map<string, 'loading' | 'loaded' | 'error'>
})()

// Get the API port from app info
const apiPort = computed(() => {
  return store.appInfo?.apiPort || 18899
})

// Build the proxied image URL
const proxiedSrc = computed(() => {
  if (!props.src) return ''
  return `http://127.0.0.1:${apiPort.value}/api/proxy-image?url=${encodeURIComponent(props.src)}`
})

// Initialize state from global cache
function initFromCache() {
  if (!proxiedSrc.value) return
  
  const status = globalImageCache.get(proxiedSrc.value)
  if (status === 'loaded') {
    // Already loaded before - show immediately
    isLoading.value = false
    hasError.value = false
    isVisible.value = true
  } else if (status === 'error') {
    isLoading.value = false
    hasError.value = true
    isVisible.value = false
  } else {
    // Not in cache or still loading - start fresh
    isLoading.value = true
    hasError.value = false
    isVisible.value = false
  }
}

// Watch src changes
watch(() => props.src, (newSrc) => {
  if (newSrc) {
    initFromCache()
  }
}, { immediate: true })

function handleError() {
  hasError.value = true
  isLoading.value = false
  isVisible.value = false
  if (proxiedSrc.value) {
    globalImageCache.set(proxiedSrc.value, 'error')
  }
  LogFrontend(`[ProxiedImage] ERROR: ${props.src.substring(0, 60)}...`)
}

function handleLoad() {
  isLoading.value = false
  hasError.value = false
  if (proxiedSrc.value) {
    globalImageCache.set(proxiedSrc.value, 'loaded')
  }
  // Trigger fade-in animation
  requestAnimationFrame(() => {
    isVisible.value = true
  })
}
</script>

<template>
  <div class="proxied-image-container" :class="props.class">
    <!-- Loading/Error placeholder - fade out when image loads -->
    <div
      class="placeholder w-full h-full flex items-center justify-center bg-dark-300 transition-opacity duration-150"
      :class="{ 'opacity-0 pointer-events-none': isVisible }"
    >
      <slot name="placeholder">
        <PlayCircleOutline class="w-12 h-12 text-gray-600" />
      </slot>
    </div>

    <!-- Actual image with fade-in animation -->
    <img
      v-if="proxiedSrc"
      :src="proxiedSrc"
      :alt="alt || ''"
      class="absolute inset-0 w-full h-full object-cover transition-opacity duration-150"
      :class="isVisible ? 'opacity-100' : 'opacity-0'"
      @error="handleError"
      @load="handleLoad"
      decoding="async"
    />
  </div>
</template>

<style scoped>
.proxied-image-container {
  position: relative;
  overflow: hidden;
}
</style>
