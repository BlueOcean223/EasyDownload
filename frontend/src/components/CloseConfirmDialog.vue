<script setup lang="ts">
import { ref, watch } from 'vue'
import { NModal, NCard, NButton, NSpace, NCheckbox, NRadioGroup, NRadio } from 'naive-ui'
import { useAppStore } from '@/stores/app'

const props = defineProps<{
  show: boolean
}>()

const emit = defineEmits<{
  (e: 'update:show', value: boolean): void
}>()

const store = useAppStore()

const selectedAction = ref<'exit' | 'minimize'>('minimize')
const rememberChoice = ref(false)

// Reset state when dialog opens
watch(() => props.show, (newVal) => {
  if (newVal) {
    selectedAction.value = store.closeAction || 'minimize'
    rememberChoice.value = false
  }
})

async function handleConfirm() {
  // Save preference if "remember" is checked
  if (rememberChoice.value) {
    await store.setCloseAction(selectedAction.value)
    await store.setDontAskOnClose(true)
  }
  
  // Close dialog
  emit('update:show', false)
  
  // Perform the action
  await store.requestAppClose(selectedAction.value)
}

function handleCancel() {
  emit('update:show', false)
}
</script>

<template>
  <NModal
    :show="show"
    :mask-closable="false"
    :close-on-esc="true"
    @update:show="emit('update:show', $event)"
  >
    <NCard
      title="关闭应用"
      :bordered="false"
      size="medium"
      style="width: 400px"
      role="dialog"
      aria-modal="true"
    >
      <div class="space-y-4">
        <p class="text-text-secondary">请选择关闭应用时的操作：</p>
        
        <NRadioGroup v-model:value="selectedAction" class="flex flex-col gap-3">
          <NRadio value="minimize" class="flex items-center">
            <span>最小化到系统托盘</span>
          </NRadio>
          <NRadio value="exit" class="flex items-center">
            <span>退出应用</span>
          </NRadio>
        </NRadioGroup>
        
        <div class="pt-2 border-t border-border">
          <NCheckbox v-model:checked="rememberChoice">
            不再询问，记住我的选择
          </NCheckbox>
        </div>
      </div>
      
      <template #footer>
        <NSpace justify="end">
          <NButton @click="handleCancel">取消</NButton>
          <NButton type="primary" @click="handleConfirm">确定</NButton>
        </NSpace>
      </template>
    </NCard>
  </NModal>
</template>
