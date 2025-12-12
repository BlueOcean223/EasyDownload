<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useAppStore } from '@/stores/app'
import { PlayCircleOutline } from '@vicons/ionicons5'

const props = defineProps<{
  src: string
  alt?: string
  class?: string
}>()

const store = useAppStore()
const hasError = ref(false)
const isLoading = ref(true)

// Get the API port from app info
const apiPort = computed(() => {
  return store.appInfo?.apiPort || 18899
})

// Build the proxied image URL
const proxiedSrc = computed(() => {
  if (!props.src) return ''
  // Use the internal API to proxy the image
  return `http://127.0.0.1:${apiPort.value}/api/proxy-image?url=${encodeURIComponent(props.src)}`
})

// Reset error state when src changes
watch(() => props.src, () => {
  hasError.value = false
  isLoading.value = true
})

function handleError() {
  hasError.value = true
  isLoading.value = false
}

function handleLoad() {
  isLoading.value = false
}
</script>

<template>
  <div class="proxied-image-container" :class="props.class">
    <!-- Loading/Error placeholder -->
    <div 
      v-if="!proxiedSrc || hasError" 
      class="placeholder w-full h-full flex items-center justify-center bg-dark-300"
    >
      <slot name="placeholder">
        <PlayCircleOutline class="w-12 h-12 text-gray-600" />
      </slot>
    </div>
    
    <!-- Actual image -->
    <img 
      v-else
      :src="proxiedSrc"
      :alt="alt || ''"
      class="w-full h-full object-cover"
      @error="handleError"
      @load="handleLoad"
      loading="lazy"
    />
  </div>
</template>

<style scoped>
.proxied-image-container {
  position: relative;
  overflow: hidden;
}
</style>
