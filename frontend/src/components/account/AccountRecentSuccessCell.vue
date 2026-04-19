<template>
  <div class="space-y-0.5 text-xs">
    <div class="flex items-center gap-1">
      <span class="text-gray-500 dark:text-gray-400">
        {{ t('admin.accounts.recentSuccess.windowLabel', { minutes: recentWindowMinutes }) }}
      </span>
      <span
        :class="[
          'font-medium',
          props.recentSuccessCount > 0
            ? 'text-emerald-600 dark:text-emerald-400'
            : 'text-gray-600 dark:text-gray-300'
        ]"
      >
        {{ props.recentSuccessCount }}
      </span>
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
  }>(),
  {
    lastSuccessAt: null,
    recentSuccessCount: 0
  }
)

const { t } = useI18n()

const lastSuccessTitle = computed(() =>
  props.lastSuccessAt ? formatDateTime(props.lastSuccessAt) : t('common.time.never')
)
</script>
