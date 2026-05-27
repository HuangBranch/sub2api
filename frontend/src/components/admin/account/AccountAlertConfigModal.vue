<template>
  <BaseDialog
    :show="show"
    :title="t('admin.accounts.alertConfig.title')"
    width="wide"
    close-on-click-outside
    @close="handleClose"
  >
    <form id="account-alert-config-form" class="space-y-5" @submit.prevent="handleSave">
      <div v-if="loading" class="flex items-center justify-center py-8">
        <Icon name="refresh" size="lg" class="animate-spin text-gray-400" />
      </div>

      <template v-else>
        <div class="rounded-lg border border-blue-200 bg-blue-50 p-3 text-sm text-blue-700 dark:border-blue-900/40 dark:bg-blue-900/20 dark:text-blue-200">
          {{ t('admin.accounts.alertConfig.description') }}
        </div>

        <section class="space-y-3">
          <div class="flex items-center justify-between gap-4 rounded-lg border border-gray-200 p-4 dark:border-dark-700">
            <div>
              <div class="text-sm font-medium text-gray-900 dark:text-white">
                {{ t('admin.accounts.alertConfig.enabled') }}
              </div>
              <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
                {{ t('admin.accounts.alertConfig.enabledHint') }}
              </p>
            </div>
            <Toggle v-model="form.enabled" />
          </div>
        </section>

        <section class="grid gap-4 md:grid-cols-2">
          <div class="rounded-lg border border-gray-200 p-4 dark:border-dark-700">
            <div class="mb-3 text-sm font-medium text-gray-900 dark:text-white">
              {{ t('admin.accounts.alertConfig.recipientsTitle') }}
            </div>
            <label class="mb-3 flex items-center justify-between gap-4">
              <span>
                <span class="block text-sm text-gray-700 dark:text-dark-200">
                  {{ t('admin.accounts.alertConfig.useOpsRecipients') }}
                </span>
                <span class="block text-xs text-gray-500 dark:text-dark-400">
                  {{ t('admin.accounts.alertConfig.useOpsRecipientsHint') }}
                </span>
              </span>
              <Toggle v-model="form.use_ops_email_recipients" />
            </label>

            <label class="input-label">{{ t('admin.accounts.alertConfig.extraRecipients') }}</label>
            <textarea
              v-model="recipientsText"
              rows="4"
              class="input"
              :placeholder="t('admin.accounts.alertConfig.extraRecipientsPlaceholder')"
            />
            <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
              {{ t('admin.accounts.alertConfig.extraRecipientsHint') }}
            </p>
          </div>

          <div class="rounded-lg border border-gray-200 p-4 dark:border-dark-700">
            <div class="mb-3 text-sm font-medium text-gray-900 dark:text-white">
              {{ t('admin.accounts.alertConfig.cooldownTitle') }}
            </div>
            <div class="grid grid-cols-2 gap-3">
              <div>
                <label class="input-label">{{ t('admin.accounts.alertConfig.cooldownMinutes') }}</label>
                <input v-model.number="form.cooldown_minutes" type="number" min="5" max="1440" class="input" />
              </div>
              <div>
                <label class="input-label">{{ t('admin.accounts.alertConfig.checkIntervalMinutes') }}</label>
                <input v-model.number="form.availability_check_interval_minutes" type="number" min="1" max="1440" class="input" />
              </div>
            </div>
            <p class="mt-2 text-xs text-gray-500 dark:text-dark-400">
              {{ t('admin.accounts.alertConfig.cooldownHint') }}
            </p>
          </div>
        </section>

        <section class="rounded-lg border border-gray-200 p-4 dark:border-dark-700">
          <div class="mb-3 text-sm font-medium text-gray-900 dark:text-white">
            {{ t('admin.accounts.alertConfig.rulesTitle') }}
          </div>
          <div class="grid gap-3 md:grid-cols-2">
            <label class="alert-rule-row">
              <span>
                <span class="block text-sm text-gray-700 dark:text-dark-200">
                  {{ t('admin.accounts.alertConfig.scheduledTestFailure') }}
                </span>
                <span class="block text-xs text-gray-500 dark:text-dark-400">
                  {{ t('admin.accounts.alertConfig.scheduledTestFailureHint') }}
                </span>
              </span>
              <Toggle v-model="form.scheduled_test_failure_enabled" />
            </label>
            <label class="alert-rule-row">
              <span>
                <span class="block text-sm text-gray-700 dark:text-dark-200">
                  {{ t('admin.accounts.alertConfig.rateLimitFailure') }}
                </span>
                <span class="block text-xs text-gray-500 dark:text-dark-400">
                  {{ t('admin.accounts.alertConfig.rateLimitFailureHint') }}
                </span>
              </span>
              <Toggle v-model="form.rate_limit_failure_enabled" />
            </label>
            <label class="alert-rule-row">
              <span>
                <span class="block text-sm text-gray-700 dark:text-dark-200">
                  {{ t('admin.accounts.alertConfig.errorFailure') }}
                </span>
                <span class="block text-xs text-gray-500 dark:text-dark-400">
                  {{ t('admin.accounts.alertConfig.errorFailureHint') }}
                </span>
              </span>
              <Toggle v-model="form.error_failure_enabled" />
            </label>
            <label class="alert-rule-row">
              <span>
                <span class="block text-sm text-gray-700 dark:text-dark-200">
                  {{ t('admin.accounts.alertConfig.minAvailableAccounts') }}
                </span>
                <span class="block text-xs text-gray-500 dark:text-dark-400">
                  {{ t('admin.accounts.alertConfig.minAvailableAccountsHint') }}
                </span>
              </span>
              <Toggle v-model="form.min_available_accounts_enabled" />
            </label>
          </div>
          <div class="mt-4 max-w-xs">
            <label class="input-label">{{ t('admin.accounts.alertConfig.minAvailableThreshold') }}</label>
            <input v-model.number="form.min_available_accounts" type="number" min="1" class="input" />
          </div>
        </section>
      </template>
    </form>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button class="btn btn-secondary" type="button" :disabled="saving" @click="handleClose">
          {{ t('common.cancel') }}
        </button>
        <button class="btn btn-primary" type="submit" form="account-alert-config-form" :disabled="saving || loading">
          {{ saving ? t('common.saving') : t('common.save') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import type { PNRPAccountAlertConfig } from '@/api/admin/accounts'

interface Props {
  show: boolean
}

interface Emits {
  (e: 'close'): void
}

const props = defineProps<Props>()
const emit = defineEmits<Emits>()
const { t } = useI18n()
const appStore = useAppStore()

const defaultConfig = (): PNRPAccountAlertConfig => ({
  enabled: true,
  use_ops_email_recipients: true,
  recipients: [],
  scheduled_test_failure_enabled: true,
  rate_limit_failure_enabled: true,
  error_failure_enabled: true,
  min_available_accounts_enabled: false,
  min_available_accounts: 1,
  cooldown_minutes: 60,
  availability_check_interval_minutes: 5
})

const form = reactive<PNRPAccountAlertConfig>(defaultConfig())
const recipientsText = ref('')
const loading = ref(false)
const saving = ref(false)

const applyConfig = (config: PNRPAccountAlertConfig) => {
  Object.assign(form, defaultConfig(), config)
  recipientsText.value = (config.recipients || []).join('\n')
}

const loadConfig = async () => {
  loading.value = true
  try {
    const config = await adminAPI.accounts.getPNRPAccountAlertConfig()
    applyConfig(config)
  } catch (error: any) {
    appStore.showError(error?.message || t('admin.accounts.alertConfig.loadFailed'))
  } finally {
    loading.value = false
  }
}

watch(
  () => props.show,
  (open) => {
    if (open) {
      loadConfig()
    }
  }
)

const normalizeRecipients = () => {
  const seen = new Set<string>()
  return recipientsText.value
    .split(/[\n,;]/)
    .map((item) => item.trim())
    .filter(Boolean)
    .filter((item) => {
      const key = item.toLowerCase()
      if (seen.has(key)) return false
      seen.add(key)
      return true
    })
}

const normalizeNumber = (value: unknown, fallback: number, min: number, max?: number) => {
  const num = Number(value)
  if (!Number.isFinite(num) || num < min) return fallback
  if (typeof max === 'number' && num > max) return max
  return Math.floor(num)
}

const handleSave = async () => {
  saving.value = true
  try {
    const payload: PNRPAccountAlertConfig = {
      ...form,
      recipients: normalizeRecipients(),
      min_available_accounts: normalizeNumber(form.min_available_accounts, 1, 1),
      cooldown_minutes: normalizeNumber(form.cooldown_minutes, 60, 5, 1440),
      availability_check_interval_minutes: normalizeNumber(form.availability_check_interval_minutes, 5, 1, 1440)
    }
    const saved = await adminAPI.accounts.updatePNRPAccountAlertConfig(payload)
    applyConfig(saved)
    appStore.showSuccess(t('admin.accounts.alertConfig.saveSuccess'))
    emit('close')
  } catch (error: any) {
    appStore.showError(error?.message || t('admin.accounts.alertConfig.saveFailed'))
  } finally {
    saving.value = false
  }
}

const handleClose = () => {
  if (saving.value) return
  emit('close')
}
</script>

<style scoped>
.alert-rule-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  border-radius: 0.5rem;
  border: 1px solid rgb(229 231 235);
  padding: 0.75rem;
}

:global(.dark) .alert-rule-row {
  border-color: rgb(55 65 81);
}
</style>
