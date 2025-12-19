<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { 
  NModal, NButton, NCheckbox, NTag, NEmpty, NScrollbar, NCard
} from 'naive-ui'
import { ImagesOutline, CloudDownloadOutline, CheckmarkCircle } from '@vicons/ionicons5'
import type { DouyinImage } from '@/types'
import ProxiedImage from './ProxiedImage.vue'

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

// Reset selection when modal opens
watch(() => props.show, (newVal) => {
  if (newVal) {
    selectedIndices.value = []
    selectAll.value = false
  }
})

// Watch selectAll changes
watch(selectAll, (newVal) => {
  if (newVal) {
    selectedIndices.value = props.images.map((_, index) => index)
  } else if (selectedIndices.value.length === props.images.length) {
    selectedIndices.value = []
  }
})

// Watch selectedIndices to update selectAll
watch(selectedIndices, (newVal) => {
  selectAll.value = newVal.length === props.images.length && props.images.length > 0
}, { deep: true })

const hasSelection = computed(() => selectedIndices.value.length > 0)

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
      <!-- Empty state -->
      <NEmpty v-if="images.length === 0" description="没有可用的图片" class="py-8">
        <template #icon>
          <ImagesOutline class="w-12 h-12 text-text-secondary opacity-50" />
        </template>
      </NEmpty>

      <!-- Images list -->
      <template v-else>
        <!-- Selection controls -->
        <div class="mb-3 flex items-center justify-between">
          <NCheckbox v-model:checked="selectAll">
            全选
          </NCheckbox>
          
          <div v-if="hasSelection" class="text-sm text-text-secondary">
            已选 <span class="text-primary font-medium">{{ selectedIndices.length }}</span> 张
          </div>
        </div>

        <NScrollbar style="max-height: 50vh" class="pr-2">
          <div class="grid grid-cols-4 gap-3">
            <div 
              v-for="(image, index) in images" 
              :key="index"
              class="relative group cursor-pointer rounded-lg overflow-hidden border-2 transition-all duration-200"
              :class="selectedIndices.includes(index) ? 'border-primary' : 'border-transparent hover:border-gray-500'"
              @click="toggleImage(index)"
            >
              <!-- Aspect ratio box 3:4 -->
              <div class="aspect-[3/4] w-full relative bg-dark-200">
                <ProxiedImage 
                  :src="image.URL" 
                  class="w-full h-full object-cover"
                />
                
                <!-- Overlay / Checkbox -->
                <div 
                  class="absolute inset-0 transition-colors duration-200 flex items-start justify-end p-2"
                  :class="selectedIndices.includes(index) ? 'bg-primary/20' : 'bg-black/0 group-hover:bg-black/10'"
                >
                  <div 
                    class="w-5 h-5 rounded flex items-center justify-center transition-all shadow-sm"
                    :class="selectedIndices.includes(index) ? 'bg-primary text-white' : 'bg-black/40 text-transparent hover:bg-black/60'"
                  >
                    <CheckmarkCircle class="w-4 h-4" v-if="selectedIndices.includes(index)" />
                  </div>
                </div>
                
                <!-- Index badge -->
                <div class="absolute bottom-1 right-1 bg-black/60 text-white text-xs px-1.5 py-0.5 rounded backdrop-blur-sm">
                  {{ index + 1 }}
                </div>
              </div>
            </div>
          </div>
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
