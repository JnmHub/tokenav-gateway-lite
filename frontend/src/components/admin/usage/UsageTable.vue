<template>
  <div class="card overflow-hidden">
    <div class="overflow-auto">
      <DataTable
        :columns="columns"
        :data="data"
        :loading="loading"
        :server-side-sort="serverSideSort"
        :default-sort-key="defaultSortKey"
        :default-sort-order="defaultSortOrder"
        fixed-layout
        :row-min-height="80"
        @sort="(key, order) => $emit('sort', key, order)"
      >
        <template #cell-user="{ row }">
          <div class="min-w-0 text-sm">
            <button
              v-if="row.main_site_email || row.user?.email"
              class="font-medium text-primary-600 underline decoration-dashed underline-offset-2 transition-colors hover:text-primary-700 dark:text-primary-400 dark:hover:text-primary-300"
              @click="$emit('userClick', row.user_id, row.main_site_email || row.user?.email)"
              :title="t('admin.usage.clickToViewBalance')"
            >
              {{ row.main_site_username || row.user?.username || row.main_site_email || row.user?.email }}
            </button>
            <span v-else class="font-medium text-gray-900 dark:text-white">-</span>
            <span v-if="row.user?.deleted_at" class="ml-1 inline-flex items-center rounded px-1 py-px text-[10px] font-medium leading-tight bg-rose-100 text-rose-600 ring-1 ring-inset ring-rose-200 dark:bg-rose-500/20 dark:text-rose-400 dark:ring-rose-500/30">
              {{ t('admin.usage.userDeletedBadge') }}
            </span>
            <div v-if="row.main_site_email && row.main_site_username" class="truncate text-xs text-gray-500 dark:text-gray-400" :title="row.main_site_email">
              {{ row.main_site_email }}
            </div>
            <div class="text-xs text-gray-500 dark:text-gray-400">
              {{ row.main_site_user_id ? t('admin.usage.mainSiteUserId', { id: row.main_site_user_id }) : `#${row.user_id}` }}
            </div>
          </div>
        </template>

        <template #cell-api_key="{ row }">
          <span class="text-sm text-gray-900 dark:text-white">{{ row.api_key?.name || '-' }}</span>
        </template>

        <template #cell-account="{ row }">
          <div class="min-w-[150px] space-y-1">
            <span class="text-sm text-gray-900 dark:text-white">{{ row.account?.name || '-' }}</span>
            <div v-if="row.account" class="flex max-w-[260px] flex-wrap gap-1">
              <span
                v-if="isAccountTemporarilyPaused(row.account)"
                class="inline-flex items-center rounded px-1.5 py-0.5 text-[10px] font-medium bg-rose-100 text-rose-700 dark:bg-rose-500/20 dark:text-rose-300"
                :title="accountPauseTooltip(row.account)"
              >
                {{ t('admin.usage.grokTemporarilyPaused') }}
              </span>
              <span
                v-if="formatGrokQuotaWindow(row.account.grok_quota_snapshot?.requests)"
                class="inline-flex items-center rounded px-1.5 py-0.5 text-[10px] font-medium bg-sky-100 text-sky-700 dark:bg-sky-500/20 dark:text-sky-300"
              >
                {{ t('admin.usage.grokRequestQuotaShort') }} {{ formatGrokQuotaWindow(row.account.grok_quota_snapshot?.requests) }}
              </span>
              <span
                v-if="formatGrokQuotaWindow(row.account.grok_quota_snapshot?.tokens)"
                class="inline-flex items-center rounded px-1.5 py-0.5 text-[10px] font-medium bg-violet-100 text-violet-700 dark:bg-violet-500/20 dark:text-violet-300"
              >
                {{ t('admin.usage.grokTokenQuotaShort') }} {{ formatGrokQuotaWindow(row.account.grok_quota_snapshot?.tokens) }}
              </span>
              <span
                v-if="row.account.grok_quota_snapshot?.entitlement_status"
                class="inline-flex items-center rounded px-1.5 py-0.5 text-[10px] font-medium bg-emerald-100 text-emerald-700 dark:bg-emerald-500/20 dark:text-emerald-300"
              >
                {{ row.account.grok_quota_snapshot.entitlement_status }}
              </span>
            </div>
          </div>
        </template>

        <template #cell-model="{ row }">
          <div class="min-w-[240px] space-y-1 text-xs">
            <div v-if="row.model_mapping_chain && row.model_mapping_chain.includes('→')" class="space-y-0.5">
              <div v-for="(step, i) in row.model_mapping_chain.split('→')" :key="i"
                 class="break-words"
                 :class="i === 0 ? 'font-medium text-gray-900 dark:text-white' : 'text-gray-500 dark:text-gray-400'"
                 :style="i > 0 ? `padding-left: ${i * 0.75}rem` : ''">
                <span v-if="i > 0" class="mr-0.5">↳</span>{{ step }}
              </div>
            </div>
            <div v-else-if="row.upstream_model && row.upstream_model !== row.model" class="space-y-0.5">
              <div class="break-words font-medium text-gray-900 dark:text-white">{{ row.model }}</div>
              <div class="break-words text-gray-500 dark:text-gray-400"><span class="mr-0.5">↳</span>{{ row.upstream_model }}</div>
            </div>
            <span v-else class="font-medium text-gray-900 dark:text-white">{{ row.model }}</span>
            <div
              v-if="row.upstream_model_mismatch === true && row.upstream_response_model"
              class="rounded-md border border-orange-200 bg-orange-50/70 px-2 py-1.5 text-[11px] text-orange-700 dark:border-orange-500/30 dark:bg-orange-500/10 dark:text-orange-300"
              :title="modelAuditTitle(row)"
            >
              <div class="flex items-center justify-between gap-2">
                <span class="font-medium">{{ t('usage.upstreamResponseModel') }}</span>
                <span class="inline-flex shrink-0 rounded bg-orange-100 px-1.5 py-px text-[10px] font-medium text-orange-800 dark:bg-orange-500/20 dark:text-orange-200">{{ t('usage.modelMismatch') }}</span>
              </div>
              <div class="mt-0.5 break-words font-mono text-[11px]">{{ row.upstream_response_model }}</div>
            </div>
          </div>
        </template>

        <template #cell-reasoning_effort="{ row }">
          <span class="text-sm text-gray-900 dark:text-white">
            {{ formatReasoningEffort(row.reasoning_effort, t) }}
          </span>
        </template>

        <template #cell-endpoint="{ row }">
          <div class="max-w-[320px] space-y-1 text-xs">
            <div class="break-all text-gray-700 dark:text-gray-300">
              <span class="font-medium text-gray-500 dark:text-gray-400">{{ t('usage.inbound') }}:</span>
              <span class="ml-1">{{ row.inbound_endpoint?.trim() || '-' }}</span>
            </div>
            <div class="break-all text-gray-700 dark:text-gray-300">
              <span class="font-medium text-gray-500 dark:text-gray-400">{{ t('usage.upstream') }}:</span>
              <span class="ml-1">{{ row.upstream_endpoint?.trim() || '-' }}</span>
            </div>
          </div>
        </template>

        <template #cell-group="{ row }">
          <div v-if="hasGroupReroute(row)" class="space-y-1 text-xs">
            <div class="flex items-center gap-1.5">
              <span class="text-[10px] font-medium uppercase tracking-wide text-gray-400 dark:text-gray-500">
                {{ t('usage.requestedGroup') }}
              </span>
              <span class="inline-flex max-w-[180px] truncate rounded bg-gray-100 px-2 py-0.5 font-medium text-gray-700 dark:bg-dark-600 dark:text-gray-200" :title="requestedGroupName(row)">
                {{ requestedGroupName(row) }}
              </span>
            </div>
            <div class="flex items-center gap-1.5">
              <span class="text-[10px] text-gray-400 dark:text-gray-500">→</span>
              <span class="text-[10px] font-medium uppercase tracking-wide text-amber-600 dark:text-amber-400">
                {{ t('usage.effectiveGroup') }}
              </span>
              <span class="inline-flex max-w-[180px] truncate rounded bg-amber-100 px-2 py-0.5 font-medium text-amber-800 dark:bg-amber-500/20 dark:text-amber-300" :title="effectiveGroupName(row)">
                {{ effectiveGroupName(row) }}
              </span>
            </div>
          </div>
          <span v-else-if="effectiveGroupName(row) !== '-'" class="inline-flex max-w-[220px] truncate items-center rounded bg-indigo-100 px-2 py-0.5 text-xs font-medium text-indigo-800 dark:bg-indigo-900 dark:text-indigo-200" :title="effectiveGroupName(row)">
            {{ effectiveGroupName(row) }}
          </span>
          <span v-else class="text-sm text-gray-400 dark:text-gray-500">-</span>
        </template>

        <template #cell-stream="{ row }">
          <span class="inline-flex items-center rounded px-2 py-0.5 text-xs font-medium" :class="getRequestTypeBadgeClass(row)">
            {{ getRequestTypeLabel(row) }}
          </span>
        </template>

        <template #cell-billing_mode="{ row }">
          <span class="inline-flex items-center rounded px-2 py-0.5 text-xs font-medium" :class="getBillingModeBadgeClass(getDisplayBillingMode(row))">
            {{ getBillingModeLabel(getDisplayBillingMode(row), t) }}
          </span>
        </template>

        <template #cell-service_tier="{ row }">
          <span class="inline-flex items-center rounded px-2 py-0.5 text-xs font-medium" :class="getServiceTierBadgeClass(row.service_tier)">
            {{ getUsageServiceTierLabel(row.service_tier, t) }}
          </span>
        </template>

        <template #cell-tokens="{ row }">
          <!-- 视频生成按数量、时长和分辨率展示，避免误显示为 Token 请求。 -->
          <div v-if="isVideoUsage(row)" class="flex items-center gap-1.5">
            <svg class="h-4 w-4 text-amber-700 dark:text-amber-400" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 10l4.553-2.276A1 1 0 0121 8.618v6.764a1 1 0 01-1.447.894L15 14M5 18h8a2 2 0 002-2V8a2 2 0 00-2-2H5a2 2 0 00-2 2v8a2 2 0 002 2z" />
            </svg>
            <span class="font-medium text-gray-900 dark:text-white">
              {{ row.video_count || 1 }}{{ t('usage.videoUnit') }}
            </span>
            <span class="text-gray-400">
              {{ row.video_resolution || '-' }} · {{ row.video_duration_seconds || 0 }}{{ t('usage.secondUnit') }}
            </span>
          </div>
          <!-- 图片生成请求（仅按次计费时显示图片格式） -->
          <div v-else-if="isImageUsage(row)" class="flex items-center gap-1.5">
            <svg class="h-4 w-4 text-indigo-700 dark:text-indigo-400" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16l4.586-4.586a2 2 0 012.828 0L16 16m-2-2l1.586-1.586a2 2 0 012.828 0L20 14m-6-6h.01M6 20h12a2 2 0 002-2V6a2 2 0 00-2-2H6a2 2 0 00-2 2v12a2 2 0 002 2z" />
            </svg>
            <span class="font-medium text-gray-900 dark:text-white">{{ row.image_count }}{{ t('usage.imageUnit') }}</span>
            <span class="text-gray-400">({{ formatImageBillingSize(row, t) }})</span>
          </div>
          <!-- Token 请求 -->
          <div v-else class="flex items-center gap-1.5">
            <div class="space-y-1 text-sm">
              <div class="flex items-center gap-2">
                <div class="inline-flex items-center gap-1">
                  <Icon name="arrowDown" size="sm" class="h-3.5 w-3.5 text-emerald-700 dark:text-emerald-400" />
                  <span class="font-medium text-gray-900 dark:text-white">{{ row.input_tokens?.toLocaleString() || 0 }}</span>
                </div>
                <div class="inline-flex items-center gap-1">
                  <Icon name="arrowUp" size="sm" class="h-3.5 w-3.5 text-violet-700 dark:text-violet-400" />
                  <span class="font-medium text-gray-900 dark:text-white">{{ row.output_tokens?.toLocaleString() || 0 }}</span>
                </div>
              </div>
              <div v-if="row.cache_read_tokens > 0 || row.cache_creation_tokens > 0" class="flex items-center gap-2">
                <div v-if="row.cache_read_tokens > 0" class="inline-flex items-center gap-1">
                  <svg class="h-3.5 w-3.5 text-sky-700 dark:text-sky-400" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 8h14M5 8a2 2 0 110-4h14a2 2 0 110 4M5 8v10a2 2 0 002 2h10a2 2 0 002-2V8m-9 4h4" /></svg>
                  <span class="font-medium text-sky-700 dark:text-sky-400">{{ formatCacheTokens(row.cache_read_tokens) }}</span>
                </div>
                <div v-if="row.cache_creation_tokens > 0" class="inline-flex items-center gap-1">
                  <svg class="h-3.5 w-3.5 text-amber-700 dark:text-amber-400" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z" /></svg>
                  <span class="font-medium text-amber-700 dark:text-amber-400">{{ formatCacheTokens(row.cache_creation_tokens) }}</span>
                  <span v-if="row.cache_creation_1h_tokens > 0" class="inline-flex items-center rounded px-1 py-px text-[10px] font-medium leading-tight bg-orange-100 text-orange-600 ring-1 ring-inset ring-orange-200 dark:bg-orange-500/20 dark:text-orange-400 dark:ring-orange-500/30">1h</span>
                  <span v-if="row.cache_ttl_overridden" :title="t('usage.cacheTtlOverriddenHint')" class="inline-flex items-center rounded px-1 py-px text-[10px] font-medium leading-tight bg-rose-100 text-rose-600 ring-1 ring-inset ring-rose-200 dark:bg-rose-500/20 dark:text-rose-400 dark:ring-rose-500/30 cursor-help">R</span>
                </div>
              </div>
              <div v-if="hasImageInputTokens(row)" class="flex items-center gap-2">
                <div class="inline-flex items-center gap-1">
                  <svg class="h-3.5 w-3.5 text-fuchsia-700 dark:text-fuchsia-400" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16l4.586-4.586a2 2 0 012.828 0L16 16m-2-2l1.586-1.586a2 2 0 012.828 0L20 14m-6-6h.01M6 20h12a2 2 0 002-2V6a2 2 0 00-2-2H6a2 2 0 00-2 2v12a2 2 0 002 2z" /></svg>
                  <span class="font-medium text-fuchsia-700 dark:text-fuchsia-400">{{ row.image_input_tokens.toLocaleString() }}</span>
                </div>
              </div>
              <div v-if="hasImageOutputTokens(row)" class="flex items-center gap-2">
                <div class="inline-flex items-center gap-1">
                  <svg class="h-3.5 w-3.5 text-pink-700 dark:text-pink-400" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16l4.586-4.586a2 2 0 012.828 0L16 16m-2-2l1.586-1.586a2 2 0 012.828 0L20 14m-6-6h.01M6 20h12a2 2 0 002-2V6a2 2 0 00-2-2H6a2 2 0 00-2 2v12a2 2 0 002 2z" /></svg>
                  <span class="font-medium text-pink-700 dark:text-pink-400">{{ row.image_output_tokens.toLocaleString() }}</span>
                </div>
              </div>
            </div>
            <!-- Token Detail Tooltip -->
            <div
              class="group relative"
              @mouseenter="showTokenTooltip($event, row)"
              @mouseleave="hideTokenTooltip"
            >
              <div class="flex h-4 w-4 cursor-help items-center justify-center rounded-full bg-gray-100 transition-colors group-hover:bg-blue-100 dark:bg-gray-700 dark:group-hover:bg-blue-900/50">
                <Icon name="infoCircle" size="xs" class="text-gray-400 group-hover:text-blue-500 dark:text-gray-500 dark:group-hover:text-blue-400" />
              </div>
            </div>
          </div>
        </template>

        <template #cell-cost="{ row }">
          <div class="text-sm">
            <div class="flex items-center gap-1.5">
              <span class="font-medium text-green-700 dark:text-green-400">${{ row.actual_cost?.toFixed(6) || '0.000000' }}</span>
              <!-- Cost Detail Tooltip -->
              <div
                class="group relative"
                @mouseenter="showTooltip($event, row)"
                @mouseleave="hideTooltip"
              >
                <div class="flex h-4 w-4 cursor-help items-center justify-center rounded-full bg-gray-100 transition-colors group-hover:bg-blue-100 dark:bg-gray-700 dark:group-hover:bg-blue-900/50">
                  <Icon name="infoCircle" size="xs" class="text-gray-400 group-hover:text-blue-500 dark:text-gray-500 dark:group-hover:text-blue-400" />
                </div>
              </div>
            </div>
            <div v-if="row.account_rate_multiplier != null" class="mt-0.5 text-[11px] text-orange-700 dark:text-orange-400">
              A ${{ accountBilled(row).toFixed(6) }}
            </div>
          </div>
        </template>

        <template #cell-latency="{ row }">
          <div class="flex items-stretch gap-2">
            <span
              data-testid="latency-bar"
              class="flex w-1 shrink-0 flex-col overflow-hidden rounded-full cursor-help"
              :title="latencyBarTooltip(row)"
              :aria-label="latencyBarTooltip(row)"
              role="img"
            >
              <span
                v-if="row.first_token_ms != null"
                data-latency-segment="first-token"
                class="block w-full"
                :class="LATENCY_BAR_CLASSES[firstTokenSeverity(row.first_token_ms)]"
                :style="{ height: `${latencyFirstTokenPercent(row)}%` }"
                aria-hidden="true"
              ></span>
              <span
                data-latency-segment="remaining"
                class="block w-full"
                :class="LATENCY_BAR_CLASSES[durationSeverity(row.duration_ms ?? 0)]"
                :style="{ height: `${100 - latencyFirstTokenPercent(row)}%` }"
                aria-hidden="true"
              ></span>
            </span>
            <div class="grid grid-cols-[max-content_max-content_max-content] items-center gap-x-2 gap-y-1 text-xs">
              <span class="text-gray-400 dark:text-gray-500">{{ t('usage.latencyFirstToken') }}</span>
              <span
                v-if="row.first_token_ms != null"
                class="font-medium tabular-nums"
                :class="LATENCY_TEXT_CLASSES[firstTokenSeverity(row.first_token_ms)]"
              >
                {{ formatDuration(row.first_token_ms) }}
              </span>
              <span v-else class="text-gray-400 dark:text-gray-500">-</span>
              <span
                v-if="row.first_token_ms != null"
                class="rounded px-1.5 py-0.5 text-[10px] font-medium"
                :class="LATENCY_BADGE_CLASSES[firstTokenSeverity(row.first_token_ms)]"
              >
                {{ latencySeverityLabel(firstTokenSeverity(row.first_token_ms)) }}
              </span>
              <!-- 伪造首字：网关先发的 SSE 注释行时刻（未开启/未触发不显示） -->
              <template v-if="row.fake_first_token_ms != null">
                <span class="text-gray-400 dark:text-gray-500">{{ t('usage.latencyFakeFirstToken') }}</span>
                <span class="font-medium tabular-nums text-cyan-600 dark:text-cyan-300">{{ formatDuration(row.fake_first_token_ms) }}</span>
                <span></span>
              </template>
              <span v-else></span>

              <span class="text-gray-400 dark:text-gray-500">{{ t('usage.latencyDuration') }}</span>
              <span
                class="font-medium tabular-nums"
                :class="LATENCY_TEXT_CLASSES[durationSeverity(row.duration_ms ?? 0)]"
              >
                {{ formatDuration(row.duration_ms) }}
              </span>
              <span
                class="rounded px-1.5 py-0.5 text-[10px] font-medium"
                :class="LATENCY_BADGE_CLASSES[durationSeverity(row.duration_ms ?? 0)]"
              >
                {{ latencySeverityLabel(durationSeverity(row.duration_ms ?? 0)) }}
              </span>
            </div>
          </div>
        </template>

        <template #cell-created_at="{ value }">
          <span class="text-sm text-gray-600 dark:text-gray-400">{{ formatDateTime(value) }}</span>
        </template>

        <template #cell-request_id="{ row }">
          <span
            v-if="row.request_id"
            class="block max-w-[180px] truncate font-mono text-xs text-gray-500 dark:text-gray-400"
            :title="row.request_id"
          >
            {{ row.request_id }}
          </span>
          <span v-else class="text-sm text-gray-400 dark:text-gray-500">-</span>
        </template>

        <template #cell-user_agent="{ row }">
          <span v-if="row.user_agent" class="text-sm text-gray-600 dark:text-gray-400 block max-w-[320px] truncate" :title="row.user_agent">{{ formatUserAgent(row.user_agent) }}</span>
          <span v-else class="text-sm text-gray-400 dark:text-gray-500">-</span>
        </template>

        <template #cell-ip_address="{ row }">
          <span v-if="row.ip_address" class="text-sm font-mono text-gray-600 dark:text-gray-400">{{ row.ip_address }}</span>
          <span v-else class="text-sm text-gray-400 dark:text-gray-500">-</span>
        </template>

        <template #empty><EmptyState :message="t('usage.noRecords')" /></template>
      </DataTable>
    </div>
  </div>

  <!-- Token Tooltip Portal -->
  <Teleport to="body">
    <div
      v-if="tokenTooltipVisible"
      class="fixed z-[9999] pointer-events-none -translate-y-1/2"
      :style="{
        left: tokenTooltipPosition.x + 'px',
        top: tokenTooltipPosition.y + 'px'
      }"
    >
      <div class="whitespace-nowrap rounded-lg border border-gray-700 bg-gray-900 px-3 py-2.5 text-xs text-white shadow-xl dark:border-gray-600 dark:bg-gray-800">
        <div class="space-y-1.5">
          <div>
            <div class="text-xs font-semibold text-gray-300 mb-1">{{ t('usage.tokenDetails') }}</div>
            <div v-if="tokenTooltipData && tokenTooltipData.input_tokens > 0 && !hasImageInputTokens(tokenTooltipData)" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.inputTokens') }}</span>
              <span class="font-medium text-white">{{ tokenTooltipData.input_tokens.toLocaleString() }}</span>
            </div>
            <div v-if="tokenTooltipData && hasImageInputTokens(tokenTooltipData) && textInputTokens(tokenTooltipData) > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.inputTokens') }}</span>
              <span class="font-medium text-white">{{ textInputTokens(tokenTooltipData).toLocaleString() }}</span>
            </div>
            <div v-if="tokenTooltipData && hasImageInputTokens(tokenTooltipData)" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('usage.imageInputTokens') }}</span>
              <span class="font-medium text-fuchsia-300">{{ tokenTooltipData.image_input_tokens.toLocaleString() }}</span>
            </div>
            <div v-if="tokenTooltipData && tokenTooltipData.output_tokens > 0 && !hasImageOutputTokens(tokenTooltipData)" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.outputTokens') }}</span>
              <span class="font-medium text-white">{{ tokenTooltipData.output_tokens.toLocaleString() }}</span>
            </div>
            <div v-if="tokenTooltipData && hasImageOutputTokens(tokenTooltipData) && textOutputTokens(tokenTooltipData) > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.outputTokens') }}</span>
              <span class="font-medium text-white">{{ textOutputTokens(tokenTooltipData).toLocaleString() }}</span>
            </div>
            <div v-if="tokenTooltipData && hasImageOutputTokens(tokenTooltipData)" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('usage.imageOutputTokens') }}</span>
              <span class="font-medium text-pink-300">{{ tokenTooltipData.image_output_tokens.toLocaleString() }}</span>
            </div>
            <div v-if="tokenTooltipData && tokenTooltipData.cache_creation_tokens > 0">
              <!-- 有 5m/1h 明细时，展开显示 -->
              <template v-if="tokenTooltipData.cache_creation_5m_tokens > 0 || tokenTooltipData.cache_creation_1h_tokens > 0">
                <div v-if="tokenTooltipData.cache_creation_5m_tokens > 0" class="flex items-center justify-between gap-4">
                  <span class="text-gray-400 flex items-center gap-1.5">
                    {{ t('admin.usage.cacheCreation5mTokens') }}
                    <span class="inline-flex items-center rounded px-1 py-px text-[10px] font-medium leading-tight bg-amber-500/20 text-amber-400 ring-1 ring-inset ring-amber-500/30">5m</span>
                  </span>
                  <span class="font-medium text-white">{{ tokenTooltipData.cache_creation_5m_tokens.toLocaleString() }}</span>
                </div>
                <div v-if="tokenTooltipData.cache_creation_1h_tokens > 0" class="flex items-center justify-between gap-4">
                  <span class="text-gray-400 flex items-center gap-1.5">
                    {{ t('admin.usage.cacheCreation1hTokens') }}
                    <span class="inline-flex items-center rounded px-1 py-px text-[10px] font-medium leading-tight bg-orange-500/20 text-orange-400 ring-1 ring-inset ring-orange-500/30">1h</span>
                  </span>
                  <span class="font-medium text-white">{{ tokenTooltipData.cache_creation_1h_tokens.toLocaleString() }}</span>
                </div>
              </template>
              <!-- 无明细时，只显示聚合值 -->
              <div v-else class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('admin.usage.cacheCreationTokens') }}</span>
                <span class="font-medium text-white">{{ tokenTooltipData.cache_creation_tokens.toLocaleString() }}</span>
              </div>
            </div>
            <div v-if="tokenTooltipData && tokenTooltipData.cache_ttl_overridden" class="flex items-center justify-between gap-4">
              <span class="text-gray-400 flex items-center gap-1.5">
                {{ t('usage.cacheTtlOverriddenLabel') }}
                <span class="inline-flex items-center rounded px-1 py-px text-[10px] font-medium leading-tight bg-rose-500/20 text-rose-400 ring-1 ring-inset ring-rose-500/30">R-{{ tokenTooltipData.cache_creation_1h_tokens > 0 ? '5m' : '1H' }}</span>
              </span>
              <span class="font-medium text-rose-400">{{ tokenTooltipData.cache_creation_1h_tokens > 0 ? t('usage.cacheTtlOverridden1h') : t('usage.cacheTtlOverridden5m') }}</span>
            </div>
            <div v-if="tokenTooltipData && tokenTooltipData.cache_read_tokens > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.cacheReadTokens') }}</span>
              <span class="font-medium text-white">{{ tokenTooltipData.cache_read_tokens.toLocaleString() }}</span>
            </div>
          </div>
          <div class="flex items-center justify-between gap-6 border-t border-gray-700 pt-1.5">
            <span class="text-gray-400">{{ t('usage.totalTokens') }}</span>
            <span class="font-semibold text-blue-400">{{ ((tokenTooltipData?.input_tokens || 0) + (tokenTooltipData?.output_tokens || 0) + (tokenTooltipData?.cache_creation_tokens || 0) + (tokenTooltipData?.cache_read_tokens || 0)).toLocaleString() }}</span>
          </div>
        </div>
        <div class="absolute right-full top-1/2 h-0 w-0 -translate-y-1/2 border-b-[6px] border-r-[6px] border-t-[6px] border-b-transparent border-r-gray-900 border-t-transparent dark:border-r-gray-800"></div>
      </div>
    </div>
  </Teleport>

  <!-- Cost Tooltip Portal -->
  <Teleport to="body">
    <div
      v-if="tooltipVisible"
      class="fixed z-[9999] pointer-events-none -translate-y-1/2"
      :style="{
        left: tooltipPosition.x + 'px',
        top: tooltipPosition.y + 'px'
      }"
    >
      <div class="whitespace-nowrap rounded-lg border border-gray-700 bg-gray-900 px-3 py-2.5 text-xs text-white shadow-xl dark:border-gray-600 dark:bg-gray-800">
        <div class="space-y-1.5">
          <!-- Cost Breakdown -->
          <div class="mb-2 border-b border-gray-700 pb-1.5">
            <div class="text-xs font-semibold text-gray-300 mb-1">{{ t('usage.costDetails') }}</div>
            <div v-if="tooltipData && tooltipData.input_cost > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.inputCost') }}</span>
              <span class="font-medium text-white">${{ tooltipData.input_cost.toFixed(6) }}</span>
            </div>
            <div v-if="tooltipData && hasImageInputTokens(tooltipData)" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('usage.imageInputCost') }}</span>
              <span class="font-medium text-fuchsia-300">${{ (tooltipData.image_input_cost ?? 0).toFixed(6) }}</span>
            </div>
            <div v-if="tooltipData && tooltipData.output_cost > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.outputCost') }}</span>
              <span class="font-medium text-white">${{ tooltipData.output_cost.toFixed(6) }}</span>
            </div>
            <div v-if="tooltipData && hasImageOutputCost(tooltipData)" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('usage.imageOutputCost') }}</span>
              <span class="font-medium text-pink-300">${{ tooltipData.image_output_cost.toFixed(6) }}</span>
            </div>
            <!-- Token billing: show unit prices per 1M tokens -->
            <template v-if="tooltipData && (getDisplayBillingMode(tooltipData) == null || getDisplayBillingMode(tooltipData) === BILLING_MODE_TOKEN)">
              <div v-if="tooltipData && textInputTokens(tooltipData) > 0" class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.inputTokenPrice') }}</span>
                <span class="font-medium text-sky-300">{{ formatTokenPricePerMillion(tooltipData.input_cost, textInputTokens(tooltipData)) }} {{ t('usage.perMillionTokens') }}</span>
              </div>
              <div v-if="tooltipData && hasImageInputTokens(tooltipData)" class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.imageInputTokenPrice') }}</span>
                <span class="font-medium text-fuchsia-300">{{ formatUnitPricePerMillion(tooltipData.image_input_price) }} {{ t('usage.perMillionTokens') }}</span>
              </div>
              <div v-if="tooltipData && hasImageInputTokens(tooltipData)" class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.imageInputPriceSource') }}</span>
                <span class="font-medium text-fuchsia-300">{{ formatImageInputPriceSource(tooltipData, t) }}</span>
              </div>
              <div v-if="tooltipData && tooltipData.output_cost > 0 && textOutputTokens(tooltipData) > 0" class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.outputTokenPrice') }}</span>
                <span class="font-medium text-violet-300">{{ formatTokenPricePerMillion(tooltipData.output_cost, textOutputTokens(tooltipData)) }} {{ t('usage.perMillionTokens') }}</span>
              </div>
              <div v-if="tooltipData && hasImageOutputTokens(tooltipData)" class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.imageOutputTokenPrice') }}</span>
                <span class="font-medium text-pink-300">{{ formatTokenPricePerMillion(tooltipData.image_output_cost ?? 0, tooltipData.image_output_tokens) }} {{ t('usage.perMillionTokens') }}</span>
              </div>
            </template>
            <template v-else-if="tooltipData && isImageUsage(tooltipData)">
              <div class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.imageCount') }}</span>
                <span class="font-medium text-white">{{ tooltipData.image_count }}{{ t('usage.imageUnit') }}</span>
              </div>
              <div class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.imageBillingSize') }}</span>
                <span class="font-medium text-white">{{ formatImageBillingSize(tooltipData, t) }}</span>
              </div>
              <div class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.imageSizeSource') }}</span>
                <span class="font-medium text-white">{{ formatImageSizeSource(tooltipData, t) }}</span>
              </div>
              <div class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.imageInputSize') }}</span>
                <span class="font-medium text-white">{{ formatImageInputSize(tooltipData, t) }}</span>
              </div>
              <div class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.imageOutputSize') }}</span>
                <span class="font-medium text-white">{{ formatImageOutputSize(tooltipData, t) }}</span>
              </div>
              <div v-if="formatImageSizeBreakdown(tooltipData)" class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.imageSizeBreakdown') }}</span>
                <span class="font-medium text-white">{{ formatImageSizeBreakdown(tooltipData) }}</span>
              </div>
              <div class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.imageUnitPrice') }}</span>
                <span class="font-medium text-sky-300">${{ imageUnitPrice(tooltipData).toFixed(6) }}</span>
              </div>
              <div class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.imageTotalPrice') }}</span>
                <span class="font-medium text-white">${{ tooltipData.total_cost?.toFixed(6) || '0.000000' }}</span>
              </div>
            </template>
            <template v-else-if="tooltipData && isVideoUsage(tooltipData)">
              <div class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.videoCount') }}</span>
                <span class="font-medium text-white">{{ tooltipData.video_count || 1 }}{{ t('usage.videoUnit') }}</span>
              </div>
              <div class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.videoResolution') }}</span>
                <span class="font-medium text-white">{{ tooltipData.video_resolution || '-' }}</span>
              </div>
              <div class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.videoDuration') }}</span>
                <span class="font-medium text-white">{{ tooltipData.video_duration_seconds || 0 }}{{ t('usage.secondUnit') }}</span>
              </div>
              <div class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.videoUnitPrice') }}</span>
                <span class="font-medium text-amber-300">${{ videoUnitPricePerSecond(tooltipData).toFixed(6) }}</span>
              </div>
              <div class="flex items-center justify-between gap-4">
                <span class="text-gray-400">{{ t('usage.videoTotalPrice') }}</span>
                <span class="font-medium text-white">${{ tooltipData.total_cost?.toFixed(6) || '0.000000' }}</span>
              </div>
            </template>
            <div v-else class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('usage.unitPrice') }}</span>
              <span class="font-medium text-sky-300">${{ tooltipData?.total_cost?.toFixed(6) || '0.000000' }}</span>
            </div>
            <div v-if="tooltipData && tooltipData.cache_creation_cost > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.cacheCreationCost') }}</span>
              <span class="font-medium text-white">${{ tooltipData.cache_creation_cost.toFixed(6) }}</span>
            </div>
            <div v-if="tooltipData && tooltipData.cache_read_cost > 0" class="flex items-center justify-between gap-4">
              <span class="text-gray-400">{{ t('admin.usage.cacheReadCost') }}</span>
              <span class="font-medium text-white">${{ tooltipData.cache_read_cost.toFixed(6) }}</span>
            </div>
          </div>
          <!-- Rate and Summary -->
          <div class="flex items-center justify-between gap-6">
            <span class="text-gray-400">{{ t('usage.serviceTier') }}</span>
            <span class="font-semibold text-cyan-300">{{ getUsageServiceTierLabel(tooltipData?.service_tier, t) }}</span>
          </div>
          <div class="flex items-center justify-between gap-6">
            <span class="text-gray-400">{{ t('usage.reasoningEffort') }}</span>
            <span class="font-semibold text-sky-300">{{ formatReasoningEffort(tooltipData?.reasoning_effort, t) }}</span>
          </div>
          <div v-if="tooltipData?.requested_service_tier" class="flex items-center justify-between gap-6">
            <span class="text-gray-400">{{ t('usage.requestedServiceTier') }}</span>
            <span class="font-medium text-white">{{ getServiceTierEvidenceLabel(tooltipData.requested_service_tier) }}</span>
          </div>
          <div v-if="tooltipData?.sent_service_tier" class="flex items-center justify-between gap-6">
            <span class="text-gray-400">{{ t('usage.sentServiceTier') }}</span>
            <span class="font-medium text-white">{{ getServiceTierEvidenceLabel(tooltipData.sent_service_tier) }}</span>
          </div>
          <div v-if="tooltipData?.response_service_tier" class="flex items-center justify-between gap-6">
            <span class="text-gray-400">{{ t('usage.responseServiceTier') }}</span>
            <span class="font-medium text-white">{{ getServiceTierEvidenceLabel(tooltipData.response_service_tier) }}</span>
          </div>
          <div class="flex items-center justify-between gap-6">
            <span class="text-gray-400">{{ t('usage.priorityDetectionSource') }}</span>
            <span class="font-medium text-white">{{ getPriorityDetectionSourceLabel(tooltipData?.priority_detection_source) }}</span>
          </div>
          <div class="flex items-center justify-between gap-6">
            <span class="text-gray-400">{{ t('usage.priorityPolicyAction') }}</span>
            <span class="font-medium text-white">{{ getPriorityPolicyActionLabel(tooltipData?.priority_policy_action) }}</span>
          </div>
          <div class="flex items-center justify-between gap-6">
            <span class="text-gray-400">{{ t('usage.priorityPolicyRuleId') }}</span>
            <span class="font-medium text-white">{{ tooltipData?.priority_policy_rule_id || t('usage.priorityPolicyNoRule') }}</span>
          </div>
          <div class="flex items-center justify-between gap-6">
            <span class="text-gray-400">{{ t('usage.priorityPolicyFallback') }}</span>
            <span class="font-medium text-white">{{ tooltipData?.priority_policy_fallback ? t('usage.priorityPolicyFallbackYes') : t('usage.priorityPolicyFallbackNo') }}</span>
          </div>
          <div v-if="tooltipData?.priority_policy_mode" class="flex items-center justify-between gap-6">
            <span class="text-gray-400">{{ t('usage.priorityPolicyMode') }}</span>
            <span class="font-medium text-white">{{ getPriorityPolicyModeLabel(tooltipData.priority_policy_mode) }}</span>
          </div>
          <div class="flex items-center justify-between gap-6">
            <span class="text-gray-400">{{ t('usage.rate') }}</span>
            <span class="font-semibold text-blue-400">{{ formatMultiplier(tooltipData?.rate_multiplier || 1) }}x</span>
          </div>
          <div class="flex items-center justify-between gap-6">
            <span class="text-gray-400">{{ t('usage.original') }}</span>
            <span class="font-medium text-white">${{ tooltipData?.total_cost?.toFixed(6) || '0.000000' }}</span>
          </div>
          <div class="flex items-center justify-between gap-6">
            <span class="text-gray-400">{{ t('usage.userBilled') }}</span>
            <span class="font-semibold text-green-400">${{ tooltipData?.actual_cost?.toFixed(6) || '0.000000' }}</span>
          </div>
          <!-- Account billing (separated from user billing) -->
          <div class="flex items-center justify-between gap-6 border-t border-gray-700 pt-1.5">
            <span class="text-gray-400">{{ t('usage.accountMultiplier') }}</span>
            <span class="font-semibold text-blue-400">{{ formatMultiplier(tooltipData?.account_rate_multiplier ?? 1) }}x</span>
          </div>
          <div class="flex items-center justify-between gap-6">
            <span class="text-gray-400">{{ t('usage.accountBilled') }}</span>
            <span class="font-semibold text-green-400">
              ${{ accountBilled({
                total_cost: tooltipData?.total_cost,
                account_stats_cost: tooltipData?.account_stats_cost,
                account_rate_multiplier: tooltipData?.account_rate_multiplier,
                upstream_model: tooltipData?.upstream_model,
              }).toFixed(6) }}
            </span>
          </div>
        </div>
        <div class="absolute right-full top-1/2 h-0 w-0 -translate-y-1/2 border-b-[6px] border-r-[6px] border-t-[6px] border-b-transparent border-r-gray-900 border-t-transparent dark:border-r-gray-800"></div>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatDateTime, formatReasoningEffort } from '@/utils/format'
import { formatCacheTokens, formatMultiplier } from '@/utils/formatters'
import { formatTokenPricePerMillion, formatUnitPricePerMillion } from '@/utils/usagePricing'
import { resolveAccountBilledCost } from '@/utils/accountCost'
import { getUsageServiceTierLabel, normalizeUsageServiceTier } from '@/utils/usageServiceTier'
import { resolveUsageRequestType } from '@/utils/usageRequestType'
import {
  LATENCY_BADGE_CLASSES,
  LATENCY_BAR_CLASSES,
  LATENCY_TEXT_CLASSES,
  durationSeverity,
  firstTokenSeverity,
  type LatencySeverity
} from '@/utils/latencyHealth'
import {
  BILLING_MODE_TOKEN,
  getBillingModeLabel,
  getBillingModeBadgeClass,
  isImageUsage,
  isVideoUsage,
  getDisplayBillingMode,
  imageUnitPrice,
  videoUnitPricePerSecond,
} from '@/utils/billingMode'
import {
  formatImageBillingSize,
  formatImageInputSize,
  formatImageOutputSize,
  formatImageSizeBreakdown,
  formatImageSizeSource,
  hasImageOutputTokens,
  textOutputTokens,
  hasImageOutputCost,
  hasImageInputTokens,
  textInputTokens,
  formatImageInputPriceSource,
} from '@/utils/imageUsage'

const accountBilled = resolveAccountBilledCost


import DataTable from '@/components/common/DataTable.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import Icon from '@/components/icons/Icon.vue'
import type { AdminUsageLog, GrokQuotaWindow, UsageLogAccountSummary } from '@/types'
import type { Column } from '@/components/common/types'

interface Props {
  data: AdminUsageLog[]
  loading?: boolean
  columns: Column[]
  serverSideSort?: boolean
  defaultSortKey?: string
  defaultSortOrder?: 'asc' | 'desc'
}

withDefaults(defineProps<Props>(), {
  loading: false,
  serverSideSort: false,
  defaultSortKey: '',
  defaultSortOrder: 'asc'
})
defineEmits<{
  userClick: [userID: number, email?: string]
  sort: [key: string, order: 'asc' | 'desc']
}>()
const { t } = useI18n()

const sentUpstreamModel = (row: AdminUsageLog): string => row.upstream_model?.trim() || row.model?.trim() || ''

const requestedGroupName = (row: AdminUsageLog): string =>
  row.requested_group_name?.trim() || row.group?.name?.trim() || '-'

const effectiveGroupName = (row: AdminUsageLog): string =>
  row.effective_group_name?.trim() || row.group?.name?.trim() || '-'

const hasGroupReroute = (row: AdminUsageLog): boolean => {
  if (row.group_rerouted) return true
  if (row.requested_group_id == null || row.effective_group_id == null) return false
  return row.requested_group_id !== row.effective_group_id
}

const modelAuditTitle = (row: AdminUsageLog): string => [
  `${t('usage.requestedModel')}: ${row.model || '-'}`,
  `${t('usage.sentUpstreamModel')}: ${sentUpstreamModel(row) || '-'}`,
  `${t('usage.upstreamResponseModel')}: ${row.upstream_response_model || '-'}`,
].join('\n')

function isAccountTemporarilyPaused(account?: UsageLogAccountSummary | null): boolean {
  if (!account?.temp_unschedulable_until) return false
  const until = Date.parse(account.temp_unschedulable_until)
  return Number.isFinite(until) && until > Date.now()
}

function formatGrokQuotaWindow(window?: GrokQuotaWindow | null): string {
  if (window?.remaining == null || window?.limit == null) return ''
  return `${window.remaining}/${window.limit}`
}

function accountPauseTooltip(account: UsageLogAccountSummary): string {
  const reason = account.temp_unschedulable_reason?.trim() || t('admin.usage.grokPauseReasonUnknown')
  const until = account.temp_unschedulable_until ? formatDateTime(account.temp_unschedulable_until) : '-'
  return t('admin.usage.grokPauseTooltip', { reason, until })
}

function getServiceTierBadgeClass(serviceTier?: string | null): string {
  const tier = normalizeUsageServiceTier(serviceTier)
  if (tier === 'priority') return 'bg-cyan-100 text-cyan-700 dark:bg-cyan-500/20 dark:text-cyan-300'
  if (tier === 'flex') return 'bg-amber-100 text-amber-700 dark:bg-amber-500/20 dark:text-amber-300'
  return 'bg-gray-100 text-gray-600 dark:bg-gray-700 dark:text-gray-300'
}

function getPriorityPolicyModeLabel(mode?: string | null): string {
  if (mode === 'force_priority') return t('usage.priorityPolicyForcePriority')
  if (mode === 'force_standard') return t('usage.priorityPolicyForceStandard')
  return t('usage.priorityPolicyFollowRequest')
}

function getServiceTierEvidenceLabel(value?: string | null): string {
  const raw = String(value || '').trim()
  if (!raw) return '-'
  const normalized = normalizeUsageServiceTier(raw)
  if (normalized === 'priority' || normalized === 'flex' || normalized === 'standard') {
    return getUsageServiceTierLabel(raw, t)
  }
  return raw
}

function getPriorityDetectionSourceLabel(source?: string | null): string {
  const labels: Record<string, string> = {
    request: 'priorityDetectionRequest',
    response: 'priorityDetectionResponse',
    request_response_invalid: 'priorityDetectionRequestAfterInvalidResponse',
    standard_default: 'priorityDetectionStandardDefault',
    standard_response_invalid: 'priorityDetectionStandardAfterInvalidResponse'
  }
  const key = labels[String(source || '').trim()] || 'priorityDetectionHistorical'
  return t(`usage.${key}`)
}

function getPriorityPolicyActionLabel(action?: string | null): string {
  const raw = String(action || '').trim()
  if (!raw) return t('usage.priorityPolicyNoRule')
  const labels: Record<string, string> = {
    pass: 'priorityPolicyActionPass',
    filter: 'priorityPolicyActionFilter',
    block: 'priorityPolicyActionBlock',
    force_priority: 'priorityPolicyActionForcePriority',
    filter_invalid: 'priorityPolicyActionFilterInvalid'
  }
  const key = labels[raw]
  return key ? t(`usage.${key}`) : raw
}

// Tooltip state - cost
const tooltipVisible = ref(false)
const tooltipPosition = ref({ x: 0, y: 0 })
const tooltipData = ref<AdminUsageLog | null>(null)

// Tooltip state - token
const tokenTooltipVisible = ref(false)
const tokenTooltipPosition = ref({ x: 0, y: 0 })
const tokenTooltipData = ref<AdminUsageLog | null>(null)

const getRequestTypeLabel = (row: AdminUsageLog): string => {
  const requestType = resolveUsageRequestType(row)
  if (requestType === 'cyber') return t('usage.cyber')
  if (requestType === 'ws_v2') return t('usage.ws')
  if (requestType === 'stream') return t('usage.stream')
  if (requestType === 'sync') return t('usage.sync')
  return t('usage.unknown')
}

const getRequestTypeBadgeClass = (row: AdminUsageLog): string => {
  const requestType = resolveUsageRequestType(row)
  if (requestType === 'cyber') return 'bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-200'
  if (requestType === 'ws_v2') return 'bg-violet-100 text-violet-800 dark:bg-violet-900 dark:text-violet-200'
  if (requestType === 'stream') return 'bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-200'
  if (requestType === 'sync') return 'bg-gray-100 text-gray-800 dark:bg-gray-700 dark:text-gray-200'
  return 'bg-amber-100 text-amber-800 dark:bg-amber-900 dark:text-amber-200'
}



const formatUserAgent = (ua: string): string => {
  return ua
}

const formatDuration = (ms: number | null | undefined): string => {
  if (ms == null) return '-'
  if (ms < 1000) return `${ms}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(2)}s`
  const totalSeconds = Math.round(ms / 1000)
  if (totalSeconds < 3600) return `${Math.floor(totalSeconds / 60)}m ${totalSeconds % 60}s`
  return `${Math.floor(totalSeconds / 3600)}h ${Math.floor((totalSeconds % 3600) / 60)}m`
}

const latencySeverityLabel = (severity: LatencySeverity): string =>
  t(`usage.latencyHealth.${severity}`)

const latencyFirstTokenPercent = (row: AdminUsageLog): number => {
  if (row.first_token_ms == null) return 0
  const total = Math.max(row.duration_ms ?? 0, row.first_token_ms)
  if (total <= 0) return 0
  return Math.min(100, Math.max(0, (row.first_token_ms / total) * 100))
}

const latencyBarTooltip = (row: AdminUsageLog): string => {
  const total = formatDuration(row.duration_ms)
  if (row.first_token_ms == null) {
    return `${t('usage.latencyDuration')}: ${total}`
  }
  const ratio = latencyFirstTokenPercent(row).toFixed(1)
  return `${t('usage.latencyFirstToken')}: ${formatDuration(row.first_token_ms)} (${ratio}%); ${t('usage.latencyDuration')}: ${total}`
}

// Cost tooltip functions
const showTooltip = (event: MouseEvent, row: AdminUsageLog) => {
  const target = event.currentTarget as HTMLElement
  const rect = target.getBoundingClientRect()
  tooltipData.value = row
  tooltipPosition.value.x = rect.right + 8
  tooltipPosition.value.y = rect.top + rect.height / 2
  tooltipVisible.value = true
}

const hideTooltip = () => {
  tooltipVisible.value = false
  tooltipData.value = null
}

// Token tooltip functions
const showTokenTooltip = (event: MouseEvent, row: AdminUsageLog) => {
  const target = event.currentTarget as HTMLElement
  const rect = target.getBoundingClientRect()
  tokenTooltipData.value = row
  tokenTooltipPosition.value.x = rect.right + 8
  tokenTooltipPosition.value.y = rect.top + rect.height / 2
  tokenTooltipVisible.value = true
}

const hideTokenTooltip = () => {
  tokenTooltipVisible.value = false
  tokenTooltipData.value = null
}
</script>
