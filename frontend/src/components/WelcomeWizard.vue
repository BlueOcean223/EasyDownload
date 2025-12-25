<script setup lang="ts">
import { ref, computed } from 'vue'
import { useAppStore } from '@/stores/app'
import { 
  NModal, NCard, NButton, NSteps, NStep, NSpace, 
  NAlert, NResult, useMessage 
} from 'naive-ui'
import { 
  ShieldCheckmarkOutline, 
  FolderOpenOutline,
  CheckmarkCircleOutline,
  ChevronForwardOutline,
  ChevronBackOutline
} from '@vicons/ionicons5'

const props = defineProps<{
  show: boolean
}>()

const emit = defineEmits<{
  (e: 'update:show', value: boolean): void
  (e: 'complete'): void
  (e: 'skip'): void
  (e: 'dont-remind'): void
}>()

const store = useAppStore()
const message = useMessage()

const currentStep = ref(1)
const installing = ref(false)
const certError = ref('')

const canGoNext = computed(() => {
  if (currentStep.value === 1) {
    return store.certInstalled
  }
  if (currentStep.value === 2) {
    return !!store.downloadDir
  }
  return true
})

const stepStatus = computed(() => {
  return (step: number) => {
    if (step < currentStep.value) return 'finish'
    if (step === currentStep.value) return 'process'
    return 'wait'
  }
})

async function installCertificate() {
  installing.value = true
  certError.value = ''
  try {
    await store.installCertificate()
    message.success('证书安装成功')
  } catch (e: any) {
    certError.value = e.message || '证书安装失败，请以管理员身份运行应用'
    message.error(certError.value)
  } finally {
    installing.value = false
  }
}

async function selectFolder() {
  try {
    const dir = await store.selectFolder()
    if (dir) {
      message.success('下载目录已设置')
    }
  } catch (e: any) {
    message.error(e.message || '选择目录失败')
  }
}

function nextStep() {
  if (currentStep.value < 3) {
    currentStep.value++
  }
}

function prevStep() {
  if (currentStep.value > 1) {
    currentStep.value--
  }
}

function complete() {
  emit('complete')
  emit('update:show', false)
}

function skip() {
  emit('skip')
  emit('update:show', false)
}

function dontRemind() {
  emit('dont-remind')
  emit('update:show', false)
}
</script>

<template>
  <NModal 
    :show="show" 
    :mask-closable="false"
    :close-on-esc="false"
    preset="card"
    style="width: 600px"
    title="欢迎使用 EasyDownload"
  >
    <div class="wizard-content">
      <!-- Steps indicator -->
      <NSteps :current="currentStep" class="mb-6">
        <NStep title="安装证书（可选）" :status="stepStatus(1)" />
        <NStep title="选择目录" :status="stepStatus(2)" />
        <NStep title="完成设置" :status="stepStatus(3)" />
      </NSteps>

      <!-- Step 1: Certificate Installation -->
      <div v-if="currentStep === 1" class="step-content">
        <div class="text-center mb-6">
          <ShieldCheckmarkOutline class="w-16 h-16 text-accent mx-auto mb-4" />
          <h3 class="text-lg font-semibold mb-2">（可选）安装 CA 证书</h3>
          <p class="text-text-secondary text-sm">
            仅当你需要使用「视频号」下载/嗅探微信 HTTPS 流量时，才需要安装 CA 根证书到系统信任存储。应用不会自动安装证书，只有你点击“安装证书”才会执行安装。
          </p>
        </div>

        <NAlert v-if="!store.certInstalled" type="warning" class="mb-4">
          此操作需要管理员权限。如果安装失败，请右键点击应用图标，选择“以管理员身份运行”。如果你只需要下载 B站/抖音/小红书，可以暂时跳过或选择不再提醒；之后也可以在「设置 → 证书管理」里手动安装。
        </NAlert>

        <NAlert v-if="certError" type="error" class="mb-4">
          {{ certError }}
        </NAlert>

        <NAlert v-if="store.certInstalled" type="success" class="mb-4">
          <template #icon>
            <CheckmarkCircleOutline class="w-5 h-5" />
          </template>
          证书已成功安装！
        </NAlert>

        <div class="flex justify-center">
          <NButton 
            type="primary" 
            size="large"
            :loading="installing"
            :disabled="store.certInstalled"
            @click="installCertificate"
          >
            {{ store.certInstalled ? '证书已安装' : '安装证书' }}
          </NButton>
        </div>
      </div>

      <!-- Step 2: Download Directory -->
      <div v-if="currentStep === 2" class="step-content">
        <div class="text-center mb-6">
          <FolderOpenOutline class="w-16 h-16 text-accent mx-auto mb-4" />
          <h3 class="text-lg font-semibold mb-2">选择下载目录</h3>
          <p class="text-text-secondary text-sm">
            选择视频文件的保存位置。您可以随时在设置中更改。
          </p>
        </div>

        <div class="bg-tertiary rounded-lg p-4 mb-4">
          <p class="text-sm text-text-secondary mb-2">当前下载目录:</p>
          <p class="text-sm font-mono truncate" :title="store.downloadDir">
            {{ store.downloadDir || '未设置' }}
          </p>
        </div>

        <div class="flex justify-center">
          <NButton 
            type="primary" 
            size="large"
            @click="selectFolder"
          >
            <template #icon>
              <FolderOpenOutline class="w-5 h-5" />
            </template>
            选择目录
          </NButton>
        </div>
      </div>

      <!-- Step 3: Complete -->
      <div v-if="currentStep === 3" class="step-content">
        <NResult
          status="success"
          title="设置完成"
          description="您已完成初始设置，现在可以开始使用 EasyDownload 了！"
        >
          <template #footer>
            <NSpace justify="center">
              <NButton type="primary" size="large" @click="complete">
                开始使用
              </NButton>
            </NSpace>
          </template>
        </NResult>
      </div>
    </div>

    <!-- Footer navigation -->
    <template #footer>
      <div class="flex justify-between items-center">
        <NButton 
          v-if="currentStep > 1" 
          quaternary 
          @click="prevStep"
        >
          <template #icon>
            <ChevronBackOutline class="w-4 h-4" />
          </template>
          上一步
        </NButton>
        <NSpace v-else>
          <NButton quaternary @click="skip">暂时跳过</NButton>
          <NButton quaternary type="warning" @click="dontRemind">不再提醒</NButton>
        </NSpace>

        <NButton 
          v-if="currentStep < 3"
          type="primary"
          :disabled="!canGoNext"
          @click="nextStep"
        >
          下一步
          <template #icon>
            <ChevronForwardOutline class="w-4 h-4" />
          </template>
        </NButton>
      </div>
    </template>
  </NModal>
</template>

<style scoped>
.step-content {
  min-height: 280px;
  display: flex;
  flex-direction: column;
  justify-content: center;
}
</style>
