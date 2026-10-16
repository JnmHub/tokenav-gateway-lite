<template>
  <BaseDialog
    :show="show"
    :title="t('admin.accounts.dataImportTitle')"
    width="normal"
    close-on-click-outside
    @close="handleClose"
  >
    <form id="import-data-form" class="space-y-4" @submit.prevent="handleImport">
      <div class="text-sm text-gray-600 dark:text-dark-300">
        {{ t('admin.accounts.dataImportHint') }}
      </div>
      <div
        class="rounded-lg border border-amber-200 bg-amber-50 p-3 text-xs text-amber-600 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-400"
      >
        {{ t('admin.accounts.dataImportWarning') }}
      </div>

      <div>
        <label class="input-label">{{ t('admin.accounts.dataImportFile') }}</label>
        <div
          :class="[
            'flex items-center justify-between gap-3 rounded-lg border border-dashed px-4 py-4 transition-colors',
            dragActive
              ? 'border-primary-400 bg-primary-50 dark:border-primary-500 dark:bg-primary-900/20'
              : 'border-gray-300 bg-gray-50 dark:border-dark-600 dark:bg-dark-800'
          ]"
          data-testid="account-import-dropzone"
          @dragenter.prevent="handleDragEnter"
          @dragover.prevent
          @dragleave.prevent="handleDragLeave"
          @drop.prevent="handleDrop"
        >
          <div class="min-w-0">
            <div class="truncate text-sm text-gray-700 dark:text-dark-200" :title="fileListTitle">
              {{ selectedFilesLabel || t('admin.accounts.dataImportSelectFile') }}
            </div>
            <div class="text-xs text-gray-500 dark:text-dark-400">
              {{ t('admin.accounts.dataImportDropHint') }}
              <span v-if="files.length > 1"> · {{ fileListTitle }}</span>
            </div>
          </div>
          <button type="button" class="btn btn-secondary shrink-0" @click="openFilePicker">
            {{ t('common.chooseFile') }}
          </button>
        </div>
        <input
          ref="fileInput"
          type="file"
          class="hidden"
          accept="application/json,.json"
          multiple
          @change="handleFileChange"
        />
      </div>

      <div
        v-if="parsing || preview || previewError"
        class="rounded-xl border border-gray-200 p-4 dark:border-dark-700"
      >
        <div class="text-sm font-medium text-gray-900 dark:text-white">
          {{ t('admin.accounts.dataImportPreview') }}
        </div>
        <div v-if="parsing" class="mt-2 text-sm text-gray-500 dark:text-dark-400">
          {{ t('admin.accounts.dataImportPreviewLoading') }}
        </div>
        <div v-else-if="previewError && !preview" class="mt-2 text-sm text-red-600 dark:text-red-400">
          {{ previewError }}
        </div>
        <div v-else-if="preview" class="mt-3 grid gap-3 sm:grid-cols-3">
          <div class="rounded-lg bg-gray-50 p-3 dark:bg-dark-800">
            <div class="text-xs text-gray-500 dark:text-dark-400">
              {{ t('admin.accounts.dataImportPreviewAccounts') }}
            </div>
            <div class="mt-1 text-xl font-semibold text-gray-900 dark:text-white">
              {{ preview.accounts }}
            </div>
          </div>
          <div class="rounded-lg bg-gray-50 p-3 dark:bg-dark-800">
            <div class="text-xs text-gray-500 dark:text-dark-400">
              {{ t('admin.accounts.dataImportPreviewProxies') }}
            </div>
            <div class="mt-1 text-xl font-semibold text-gray-900 dark:text-white">
              {{ preview.proxies }}
            </div>
          </div>
          <div class="rounded-lg bg-gray-50 p-3 dark:bg-dark-800">
            <div class="text-xs text-gray-500 dark:text-dark-400">
              {{ t('admin.accounts.dataImportPreviewVersion') }}
            </div>
            <div class="mt-1 truncate text-sm font-semibold text-gray-900 dark:text-white">
              {{ preview.version }}
            </div>
          </div>
        </div>
        <div v-if="fileErrors.length" class="mt-3 rounded-lg bg-red-50 p-3 text-xs text-red-700 dark:bg-red-900/20 dark:text-red-300">
          <div class="font-medium">{{ t('admin.accounts.dataImportInvalidFilesTitle') }}</div>
          <div v-for="item in fileErrors" :key="item.name" class="mt-1 break-all">
            {{ item.name }} — {{ item.message }}
          </div>
        </div>
      </div>

      <div
        v-if="result"
        class="space-y-2 rounded-xl border border-gray-200 p-4 dark:border-dark-700"
      >
        <div class="text-sm font-medium text-gray-900 dark:text-white">
          {{ t('admin.accounts.dataImportResult') }}
        </div>
        <div class="text-sm text-gray-700 dark:text-dark-300">
          {{ t('admin.accounts.dataImportResultSummary', result) }}
        </div>

        <div v-if="errorItems.length" class="mt-2">
          <div class="text-sm font-medium text-red-600 dark:text-red-400">
            {{ t('admin.accounts.dataImportErrors') }}
          </div>
          <div
            class="mt-2 max-h-48 overflow-auto rounded-lg bg-gray-50 p-3 font-mono text-xs dark:bg-dark-800"
          >
            <div v-for="(item, idx) in errorItems" :key="idx" class="whitespace-pre-wrap">
              {{ item.kind }} {{ item.name || item.proxy_key || '-' }} — {{ item.message }}
            </div>
          </div>
        </div>
      </div>
    </form>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button class="btn btn-secondary" type="button" :disabled="importing" @click="handleClose">
          {{ t('common.cancel') }}
        </button>
        <button
          class="btn btn-primary"
          type="submit"
          form="import-data-form"
          :disabled="importing || parsing || !parsedPayload"
        >
          {{ importing ? t('admin.accounts.dataImporting') : t('admin.accounts.dataImportButton') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import type { AdminDataImportResult, AdminDataPayload } from '@/types'

interface Props {
  show: boolean
}

interface Emits {
  (e: 'close'): void
  (e: 'imported'): void
}

const props = defineProps<Props>()
const emit = defineEmits<Emits>()

const { t } = useI18n()
const appStore = useAppStore()

const importing = ref(false)
const parsing = ref(false)
const files = ref<File[]>([])
const dragDepth = ref(0)
const dragActive = computed(() => dragDepth.value > 0)
const parsedPayload = ref<AdminDataPayload | null>(null)
const result = ref<AdminDataImportResult | null>(null)
const previewError = ref('')
const preview = ref<{ accounts: number; proxies: number; version: string } | null>(null)
const fileErrors = ref<Array<{ name: string; message: string }>>([])
const hasCreatedData = ref(false)

const fileInput = ref<HTMLInputElement | null>(null)
const selectedFilesLabel = computed(() => {
  if (files.value.length === 0) return ''
  if (files.value.length === 1) return files.value[0]?.name || ''
  return t('admin.accounts.selectedCount', { count: files.value.length })
})
const fileListTitle = computed(() => files.value.map((item) => item.name).join(', '))

const errorItems = computed(() => result.value?.errors || [])

watch(
  () => props.show,
  (open) => {
    if (open) {
      files.value = []
      dragDepth.value = 0
      parsedPayload.value = null
      result.value = null
      preview.value = null
      previewError.value = ''
      fileErrors.value = []
      hasCreatedData.value = false
      if (fileInput.value) {
        fileInput.value.value = ''
      }
    }
  }
)

const openFilePicker = () => {
  fileInput.value?.click()
}

const handleFileChange = (event: Event) => {
  const target = event.target as HTMLInputElement
  void selectFiles(target.files)
  target.value = ''
}

const handleDrop = (event: DragEvent) => {
  dragDepth.value = 0
  void selectFiles(event.dataTransfer?.files)
}

const handleClose = () => {
  if (importing.value) return
  if (hasCreatedData.value) {
    hasCreatedData.value = false
    emit('imported')
  }
  emit('close')
}

const handleDragEnter = () => {
  if (!importing.value) dragDepth.value += 1
}

const handleDragLeave = () => {
  dragDepth.value = Math.max(0, dragDepth.value - 1)
}

const isJsonFile = (sourceFile: File): boolean =>
  sourceFile.name.toLowerCase().endsWith('.json') || sourceFile.type === 'application/json'

const readFileAsText = async (sourceFile: File): Promise<string> => {
  if (typeof sourceFile.text === 'function') {
    return sourceFile.text()
  }

  if (typeof sourceFile.arrayBuffer === 'function') {
    const buffer = await sourceFile.arrayBuffer()
    return new TextDecoder().decode(buffer)
  }

  return await new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result ?? ''))
    reader.onerror = () => reject(reader.error || new Error('Failed to read file'))
    reader.readAsText(sourceFile)
  })
}

const validatePayload = (data: unknown): AdminDataPayload => {
  if (!data || typeof data !== 'object' || Array.isArray(data)) {
    throw new Error(t('admin.accounts.dataImportInvalidShape'))
  }

  const payload = data as Partial<AdminDataPayload>
  if (payload.type && payload.type !== 'sub2api-data' && payload.type !== 'sub2api-bundle') {
    throw new Error(t('admin.accounts.dataImportUnsupportedType'))
  }
  if (payload.version != null && payload.version !== 1) {
    throw new Error(t('admin.accounts.dataImportUnsupportedVersion'))
  }
  if (!Array.isArray(payload.accounts) || !Array.isArray(payload.proxies)) {
    throw new Error(t('admin.accounts.dataImportInvalidShape'))
  }

  return {
    ...payload,
    exported_at: payload.exported_at || '',
    accounts: payload.accounts,
    proxies: payload.proxies
  } as AdminDataPayload
}

const mergeDataPayloads = (payloads: AdminDataPayload[]): AdminDataPayload => {
  const first = payloads[0]
  if (payloads.length === 1 && first) return first

  return {
    type: payloads.find((item) => item.type)?.type,
    version: payloads.find((item) => item.version != null)?.version,
    exported_at: new Date().toISOString(),
    proxies: payloads.flatMap((item) => item.proxies),
    accounts: payloads.flatMap((item) => item.accounts)
  }
}

const selectFiles = async (sourceFiles: FileList | File[] | null | undefined) => {
  if (importing.value) return
  const incoming = Array.from(sourceFiles || [])
  const jsonFiles = incoming.filter(isJsonFile)
  if (jsonFiles.length === 0) {
    appStore.showError(t('admin.accounts.dataImportSelectFile'))
    return
  }
  if (jsonFiles.length < incoming.length) {
    appStore.showWarning(
      t('admin.accounts.dataImportIgnoredFiles', { count: incoming.length - jsonFiles.length })
    )
  }

  files.value = jsonFiles
  parsedPayload.value = null
  preview.value = null
  previewError.value = ''
  fileErrors.value = []
  result.value = null

  parsing.value = true
  try {
    const validPayloads: AdminDataPayload[] = []
    for (const sourceFile of jsonFiles) {
      try {
        const text = await readFileAsText(sourceFile)
        validPayloads.push(validatePayload(JSON.parse(text)))
      } catch (error: any) {
        fileErrors.value.push({
          name: sourceFile.name,
          message:
            error instanceof SyntaxError
              ? t('admin.accounts.dataImportParseFailed')
              : (error?.message || t('admin.accounts.dataImportFailed'))
        })
      }
    }

    if (validPayloads.length === 0) {
      previewError.value = t('admin.accounts.dataImportNoValidFiles')
      return
    }

    const payload = mergeDataPayloads(validPayloads)
    parsedPayload.value = payload
    preview.value = {
      accounts: payload.accounts.length,
      proxies: payload.proxies.length,
      version: payload.version ? `v${payload.version}` : t('admin.accounts.dataImportPreviewLegacy')
    }
    if (fileErrors.value.length > 0) {
      appStore.showWarning(
        t('admin.accounts.dataImportSkippedInvalidFiles', { count: fileErrors.value.length })
      )
    }
  } finally {
    parsing.value = false
  }
}

const handleImport = async () => {
  if (files.value.length === 0) {
    appStore.showError(t('admin.accounts.dataImportSelectFile'))
    return
  }
  if (!parsedPayload.value) {
    appStore.showError(previewError.value || t('admin.accounts.dataImportParseFailed'))
    return
  }

  importing.value = true
  try {
    const res = await adminAPI.accounts.importData({
      data: parsedPayload.value,
      skip_default_group_bind: true
    })

    result.value = res

    const msgParams: Record<string, unknown> = {
      account_created: res.account_created,
      account_failed: res.account_failed,
      proxy_created: res.proxy_created,
      proxy_reused: res.proxy_reused,
      proxy_failed: res.proxy_failed,
    }
    if (res.account_failed > 0 || res.proxy_failed > 0) {
      if (res.account_created > 0 || res.proxy_created > 0 || res.proxy_reused > 0) {
        hasCreatedData.value = true
      }
      appStore.showError(t('admin.accounts.dataImportCompletedWithErrors', msgParams))
    } else {
      appStore.showSuccess(t('admin.accounts.dataImportSuccess', msgParams))
      emit('imported')
    }
  } catch (error: any) {
    if (error instanceof SyntaxError) {
      appStore.showError(t('admin.accounts.dataImportParseFailed'))
    } else {
      appStore.showError(error?.message || t('admin.accounts.dataImportFailed'))
    }
  } finally {
    importing.value = false
  }
}
</script>
