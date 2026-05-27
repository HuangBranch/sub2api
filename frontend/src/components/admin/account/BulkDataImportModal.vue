<template>
  <BaseDialog
    :show="show"
    :title="t('admin.accounts.bulkDataImportTitle')"
    width="normal"
    close-on-click-outside
    @close="handleClose"
  >
    <form id="bulk-data-import-form" class="space-y-4" @submit.prevent="handleImport">
      <div class="text-sm text-gray-600 dark:text-dark-300">
        {{ t('admin.accounts.bulkDataImportHint') }}
      </div>
      <div
        class="rounded-lg border border-amber-200 bg-amber-50 p-3 text-xs text-amber-600 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-400"
      >
        {{ t('admin.accounts.bulkDataImportWarning') }}
      </div>

      <div>
        <label class="input-label">{{ t('admin.accounts.bulkDataImportFiles') }}</label>
        <div
          class="flex items-center justify-between gap-3 rounded-lg border border-dashed border-gray-300 bg-gray-50 px-4 py-3 dark:border-dark-600 dark:bg-dark-800"
        >
          <div class="min-w-0">
            <div class="truncate text-sm text-gray-700 dark:text-dark-200">
              {{ selectedFilesLabel }}
            </div>
            <div class="text-xs text-gray-500 dark:text-dark-400">JSON (.json)</div>
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
        <div
          v-if="files.length"
          class="mt-2 max-h-36 overflow-auto rounded-lg bg-gray-50 p-3 text-xs text-gray-600 dark:bg-dark-800 dark:text-dark-300"
        >
          <div
            v-for="file in files"
            :key="`${file.name}-${file.size}-${file.lastModified}`"
            class="truncate"
          >
            {{ file.name }}
          </div>
        </div>
      </div>

      <div
        v-if="result"
        class="space-y-2 rounded-xl border border-gray-200 p-4 dark:border-dark-700"
      >
        <div class="text-sm font-medium text-gray-900 dark:text-white">
          {{ t('admin.accounts.bulkDataImportResult') }}
        </div>
        <div class="text-sm text-gray-700 dark:text-dark-300">
          {{ t('admin.accounts.bulkDataImportResultSummary', result) }}
        </div>

        <div v-if="errorItems.length" class="mt-2">
          <div class="text-sm font-medium text-red-600 dark:text-red-400">
            {{ t('admin.accounts.bulkDataImportErrors') }}
          </div>
          <div
            class="mt-2 max-h-48 overflow-auto rounded-lg bg-gray-50 p-3 font-mono text-xs dark:bg-dark-800"
          >
            <div
              v-for="(item, idx) in errorItems"
              :key="`${item.file_name || 'file'}-${idx}-${item.message}`"
              class="whitespace-pre-wrap"
            >
              <span v-if="item.file_name">[{{ item.file_name }}] </span>
              {{ item.kind }} {{ item.name || item.proxy_key || '-' }} - {{ item.message }}
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
          form="bulk-data-import-form"
          :disabled="importing"
        >
          {{ importing ? t('admin.accounts.bulkDataImporting') : t('admin.accounts.bulkDataImportButton') }}
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
import type { AdminDataImportError, AdminDataImportResult, AdminDataPayload } from '@/types'

interface Props {
  show: boolean
}

interface Emits {
  (e: 'close'): void
  (e: 'imported'): void
}

interface BulkDataImportError extends Omit<AdminDataImportError, 'kind'> {
  kind: AdminDataImportError['kind'] | 'file'
  file_name?: string
}

interface BulkDataImportResult {
  files_total: number
  files_imported: number
  files_failed: number
  proxy_created: number
  proxy_reused: number
  proxy_failed: number
  account_created: number
  account_failed: number
  errors: BulkDataImportError[]
}

const props = defineProps<Props>()
const emit = defineEmits<Emits>()

const { t } = useI18n()
const appStore = useAppStore()

const importing = ref(false)
const files = ref<File[]>([])
const result = ref<BulkDataImportResult | null>(null)
const fileInput = ref<HTMLInputElement | null>(null)

const selectedFilesLabel = computed(() =>
  files.value.length > 0
    ? t('admin.accounts.bulkDataImportSelectedFiles', { count: files.value.length })
    : t('admin.accounts.bulkDataImportSelectFiles')
)
const errorItems = computed(() => result.value?.errors || [])

watch(
  () => props.show,
  (open) => {
    if (open) {
      files.value = []
      result.value = null
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
  files.value = Array.from(target.files || [])
}

const handleClose = () => {
  if (importing.value) return
  emit('close')
}

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

const createEmptyResult = (): BulkDataImportResult => ({
  files_total: files.value.length,
  files_imported: 0,
  files_failed: 0,
  proxy_created: 0,
  proxy_reused: 0,
  proxy_failed: 0,
  account_created: 0,
  account_failed: 0,
  errors: []
})

const appendImportResult = (
  target: BulkDataImportResult,
  source: AdminDataImportResult,
  fileName: string
) => {
  target.files_imported += 1
  target.proxy_created += source.proxy_created
  target.proxy_reused += source.proxy_reused
  target.proxy_failed += source.proxy_failed
  target.account_created += source.account_created
  target.account_failed += source.account_failed
  target.errors.push(...(source.errors || []).map((item) => ({ ...item, file_name: fileName })))
}

const extractErrorMessage = (error: any) => {
  return (
    error?.response?.data?.detail ||
    error?.response?.data?.message ||
    error?.message ||
    t('admin.accounts.bulkDataImportFailed')
  )
}

const handleImport = async () => {
  if (!files.value.length) {
    appStore.showError(t('admin.accounts.bulkDataImportSelectFiles'))
    return
  }

  importing.value = true
  const aggregate = createEmptyResult()

  try {
    for (const sourceFile of files.value) {
      try {
        const text = (await readFileAsText(sourceFile)).trim()
        if (!text) {
          aggregate.files_failed += 1
          aggregate.errors.push({
            kind: 'file',
            file_name: sourceFile.name,
            message: t('admin.accounts.bulkDataImportEmptyFile')
          })
          continue
        }

        const dataPayload = JSON.parse(text) as AdminDataPayload
        const res = await adminAPI.accounts.importData({
          data: dataPayload,
          skip_default_group_bind: true
        })
        appendImportResult(aggregate, res, sourceFile.name)
      } catch (error: any) {
        aggregate.files_failed += 1
        aggregate.errors.push({
          kind: 'file',
          file_name: sourceFile.name,
          message: error instanceof SyntaxError
            ? t('admin.accounts.bulkDataImportParseFailed')
            : extractErrorMessage(error)
        })
      }
    }

    result.value = aggregate

    const hasErrors =
      aggregate.files_failed > 0 ||
      aggregate.account_failed > 0 ||
      aggregate.proxy_failed > 0
    const msgParams: Record<string, unknown> = { ...aggregate }

    if (hasErrors) {
      appStore.showError(t('admin.accounts.bulkDataImportCompletedWithErrors', msgParams))
    } else {
      appStore.showSuccess(t('admin.accounts.bulkDataImportSuccess', msgParams))
      emit('imported')
    }
  } finally {
    importing.value = false
  }
}
</script>
