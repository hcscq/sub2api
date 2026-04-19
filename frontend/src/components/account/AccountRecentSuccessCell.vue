<template>
  <div class="space-y-0.5 text-xs">
    <div class="flex items-center gap-1">
      <span class="text-gray-500 dark:text-gray-400">
        {{ t('admin.accounts.recentSuccess.windowLabel', { minutes: recentWindowMinutes }) }}
      </span>
      <span
        :class="[
          'font-medium',
          summaryClass
        ]"
      >
        {{ t('admin.accounts.recentSuccess.summaryLabel', { total: props.recentRequestCount, success: props.recentSuccessCount }) }}
      </span>
    </div>
    <div class="text-gray-500 dark:text-gray-400">
      {{ t('admin.accounts.recentSuccess.rateLabel') }}: {{ successRateText }}
    </div>
    <div class="text-gray-500 dark:text-gray-400" :title="lastSuccessTitle">
      {{ t('admin.accounts.recentSuccess.lastLabel') }}: {{ formatRelativeTime(props.lastSuccessAt) }}
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatDateTime, formatRelativeTime } from '@/utils/format'

const recentWindowMinutes = 15

const props = withDefaults(
  defineProps<{
    lastSuccessAt?: string | null
    recentSuccessCount?: number
    recentRequestCount?: number
  }>(),
  {
    lastSuccessAt: null,
    recentSuccessCount: 0,
    recentRequestCount: 0
  }
)

const { t } = useI18n()

const lastSuccessTitle = computed(() =>
  props.lastSuccessAt ? formatDateTime(props.lastSuccessAt) : t('common.time.never')
)

const successRate = computed(() => {
  if (!props.recentRequestCount || props.recentRequestCount <= 0) return null
  return Math.round((props.recentSuccessCount / props.recentRequestCount) * 100)
})

const successRateText = computed(() =>
  successRate.value == null ? '-' : `${successRate.value}%`
)

const summaryClass = computed(() => {
  if (!props.recentRequestCount || props.recentRequestCount <= 0) {
    return 'text-gray-600 dark:text-gray-300'
  }
  if (props.recentSuccessCount <= 0) {
    return 'text-rose-600 dark:text-rose-400'
  }
  if (props.recentSuccessCount >= props.recentRequestCount) {
    return 'text-emerald-600 dark:text-emerald-400'
  }
  return 'text-amber-600 dark:text-amber-400'
})
</script>
