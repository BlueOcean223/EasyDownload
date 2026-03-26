<script setup lang="ts">
import { ref, computed, watch } from 'vue'
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
import { CanRestartAsAdmin, IsAdmin, RestartAsAdmin } from '../../wailsjs/go/main/App'
import { getErrorMessage, isPermissionError } from '@/utils/errors'

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
const showAdminDialog = ref(false)
const restarting = ref(false)
const closing = ref(false)

// Reset closing flag when modal is shown
watch(
  () => props.show,
  (value) => {
    if (value) closing.value = false
  }
)

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
    const errorMsg = getErrorMessage(e, '证书安装失败')
    const permissionError = isPermissionError(errorMsg)
    let isAdmin = false
    try {
      isAdmin = await IsAdmin()
    } catch {
      certError.value = errorMsg
      message.error(certError.value)
      return
    }

    if (permissionError && !isAdmin) {
      let canRestart = false
      try {
        canRestart = await CanRestartAsAdmin()
      } catch {
        certError.value = errorMsg
        message.error(certError.value)
        return
      }
      if (canRestart) {
        showAdminDialog.value = true
        return
      }
    }

    certError.value = errorMsg
    message.error(certError.value)
  } finally {
    installing.value = false
  }
}

async function restartAsAdmin() {
  restarting.value = true
  try {
    await RestartAsAdmin()
    // App will quit automatically after launching elevated process
  } catch (e: any) {
    message.error('无法以管理员身份重启: ' + getErrorMessage(e, '未知错误'))
    showAdminDialog.value = false
  } finally {
    restarting.value = false
  }
}

function cancelAdminRestart() {
  showAdminDialog.value = false
  certError.value = '证书安装需要管理员权限，请手动以管理员身份运行应用'
}

async function selectFolder() {
  try {
    const dir = await store.selectFolder()
    if (dir) {
      message.success('下载目录已设置')
    }
  } catch (e: any) {
    message.error(getErrorMessage(e, '选择目录失败'))
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

function closeWizard(action: 'complete' | 'skip' | 'dont-remind') {
  if (closing.value) return
  closing.value = true
  if (action === 'complete') {
    emit('complete')
  } else if (action === 'skip') {
    emit('skip')
  } else {
    emit('dont-remind')
  }
  emit('update:show', false)
}

function complete() {
  closeWizard('complete')
}

function skip() {
  closeWizard('skip')
}

function dontRemind() {
  closeWizard('dont-remind')
}

function handleModalUpdateShow(value: boolean) {
  if (!value && !closing.value) {
    // Treat clicking the top-right close button as "skip"
    skip()
  }
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
    @update:show="handleModalUpdateShow"
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
            仅用于「视频号」嗅探微信 HTTPS 流量。点击“安装证书”后才会写入系统信任存储。
          </p>
        </div>

        <NAlert v-if="!store.certInstalled" type="warning" class="mb-4">
          需要系统授权。Windows 下可使用管理员权限运行；macOS 下会弹出系统密码授权框，也可在「设置 → 证书管理」手动安装。
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

  <!-- Admin Restart Dialog -->
  <NModal
    v-model:show="showAdminDialog"
    preset="dialog"
    title="需要管理员权限"
    positive-text="以管理员身份重启"
    negative-text="取消"
    :positive-button-props="{ loading: restarting }"
    @positive-click="restartAsAdmin"
    @negative-click="cancelAdminRestart"
  >
    <p>安装证书需要管理员权限，是否以管理员身份重启？</p>
    <p class="text-text-secondary text-sm mt-2">
      系统会弹出 UAC 提示。
    </p>
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
