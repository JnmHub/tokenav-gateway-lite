<template>
  <AppLayout>
    <TablePageLayout>
      <template #filters>
        <div
          class="flex flex-col justify-between gap-4 lg:flex-row lg:items-start"
        >
          <!-- Left: fuzzy search + filters (can wrap to multiple lines) -->
          <div class="flex flex-1 flex-wrap items-center gap-3">
            <div class="relative w-full sm:w-64">
              <Icon
                name="search"
                size="md"
                class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400 dark:text-gray-500"
              />
              <input
                v-model="searchQuery"
                type="text"
                :placeholder="t('admin.groups.searchGroups')"
                class="input pl-10"
                @input="handleSearch"
              />
            </div>
            <Select
              v-model="filters.platform"
              :options="platformFilterOptions"
              :placeholder="t('admin.groups.allPlatforms')"
              class="w-44"
              @change="loadGroups"
            />
            <Select
              v-model="filters.status"
              :options="statusOptions"
              :placeholder="t('admin.groups.allStatus')"
              class="w-40"
              @change="loadGroups"
            />
            <Select
              v-model="filters.is_exclusive"
              :options="exclusiveOptions"
              :placeholder="t('admin.groups.allGroups')"
              class="w-44"
              @change="loadGroups"
            />
          </div>

          <!-- Right: actions -->
          <div
            class="flex w-full flex-shrink-0 flex-wrap items-center justify-end gap-3 lg:w-auto"
          >
            <button
              @click="loadGroups"
              :disabled="loading"
              class="btn btn-secondary"
              :title="t('common.refresh')"
            >
              <Icon
                name="refresh"
                size="md"
                :class="loading ? 'animate-spin' : ''"
              />
            </button>
            <span v-if="capacityLastUpdated" class="hidden text-xs text-gray-400 dark:text-gray-500 xl:inline">
              {{ capacityLastUpdatedLabel }}
            </span>
            <div ref="columnSettingsDropdownRef" class="relative">
              <button
                @click.stop="showColumnSettingsDropdown = !showColumnSettingsDropdown"
                class="btn btn-secondary"
                :title="t('admin.groups.columnSettings')"
              >
                <Icon name="grid" size="md" class="mr-2" />
                {{ t("admin.groups.columnSettings") }}
              </button>
              <div
                v-if="showColumnSettingsDropdown"
                class="absolute right-0 z-50 mt-2 w-56 rounded-lg border border-gray-200 bg-white p-2 shadow-lg dark:border-dark-600 dark:bg-dark-800"
                @click.stop
              >
                <div class="px-2 py-2">
                  <div class="flex items-center justify-between gap-3">
                    <span class="text-xs font-semibold uppercase tracking-wide text-gray-400 dark:text-gray-500">
                      {{ t("admin.groups.viewColumns") }}
                    </span>
                    <Icon name="grid" size="sm" class="text-gray-400" />
                  </div>
                </div>
                <div class="grid grid-cols-1 gap-1">
                  <button
                    v-for="col in toggleableColumns"
                    :key="col.key"
                    @click="toggleColumn(col.key)"
                    class="flex w-full items-center justify-between rounded-md px-3 py-2 text-sm text-gray-700 transition-colors hover:bg-gray-100 dark:text-gray-200 dark:hover:bg-gray-700"
                  >
                    <span class="truncate">{{ col.label }}</span>
                    <Icon v-if="isColumnVisible(col.key)" name="check" size="sm" class="text-primary-500" />
                  </button>
                </div>
              </div>
            </div>
            <button
              @click="openSortModal"
              class="btn btn-secondary"
              :title="t('admin.groups.sortOrder')"
            >
              <Icon name="arrowsUpDown" size="md" class="mr-2" />
              {{ t("admin.groups.sortOrder") }}
            </button>
            <button
              @click="openCreateModal"
              class="btn btn-primary"
              data-tour="groups-create-btn"
            >
              <Icon name="plus" size="md" class="mr-2" />
              {{ t("admin.groups.createGroup") }}
            </button>
          </div>
        </div>
      </template>

      <template #table>
        <DataTable
          :columns="visibleColumns"
          :data="groups"
          :loading="loading"
          :server-side-sort="true"
          default-sort-key="sort_order"
          default-sort-order="asc"
          @sort="handleSort"
        >
          <template #cell-name="{ value, row }">
            <div class="flex flex-wrap items-center gap-2">
              <span class="font-medium text-gray-900 dark:text-white">{{ value }}</span>
              <span
                v-if="row.allow_blocked_keyword_requests"
                class="inline-flex rounded-full bg-amber-50 px-2 py-0.5 text-[11px] font-medium text-amber-700 dark:bg-amber-900/20 dark:text-amber-300"
              >
                {{ t("admin.groups.columns.allowBlockedKeywordRequests") }}
              </span>
              <span
                v-if="row.local_only"
                class="inline-flex rounded-full bg-sky-50 px-2 py-0.5 text-[11px] font-medium text-sky-700 dark:bg-sky-900/20 dark:text-sky-300"
              >
                {{ t("admin.groups.columns.localOnly") }}
              </span>
              <span class="inline-flex rounded-full bg-emerald-50 px-2 py-0.5 text-[11px] font-medium text-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-300">
                {{ row.minimum_tier == null ? "继承节点" : `最低 T${row.minimum_tier}` }}
              </span>
            </div>
          </template>

          <template #cell-id="{ value }">
            <span class="font-mono text-xs text-gray-500 dark:text-gray-400">
              #{{ value }}
            </span>
          </template>

          <template #cell-platform="{ value, row }">
            <span
              :class="[
                'inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium',
                value === 'anthropic'
                  ? 'bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-400'
                  : value === 'openai'
                    ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400'
                    : value === 'antigravity'
                      ? 'bg-purple-100 text-purple-700 dark:bg-purple-900/30 dark:text-purple-400'
                      : value === 'grok'
                        ? 'bg-zinc-200 text-zinc-800 dark:bg-zinc-700 dark:text-zinc-100'
                        : 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400',
              ]"
            >
              <PlatformIcon :platform="value" size="xs" />
              {{ t("admin.groups.platforms." + value) }}
            </span>
            <span v-if="row.display_brand" class="ml-2 text-xs font-medium text-gray-500 dark:text-gray-400">
              {{ row.display_brand }}
            </span>
          </template>

          <template #cell-billing_type="{ row }">
            <div class="space-y-1">
              <!-- Type Badge -->
              <span
                :class="[
                  'inline-block rounded-full px-2 py-0.5 text-xs font-medium',
                  row.subscription_type === 'subscription'
                    ? 'bg-violet-100 text-violet-700 dark:bg-violet-900/30 dark:text-violet-400'
                    : 'bg-gray-100 text-gray-600 dark:bg-gray-700 dark:text-gray-300',
                ]"
              >
                {{
                  row.subscription_type === "subscription"
                    ? t("admin.groups.subscription.subscription")
                    : t("admin.groups.subscription.standard")
                }}
              </span>
              <!-- Subscription Limits - compact single line -->
              <div
                v-if="row.subscription_type === 'subscription'"
                class="text-xs text-gray-500 dark:text-gray-400"
              >
                <template
                  v-if="
                    row.daily_limit_usd ||
                    row.weekly_limit_usd ||
                    row.monthly_limit_usd
                  "
                >
                  <span v-if="row.daily_limit_usd"
                    >${{ row.daily_limit_usd }}/{{
                      t("admin.groups.limitDay")
                    }}</span
                  >
                  <span
                    v-if="
                      row.daily_limit_usd &&
                      (row.weekly_limit_usd || row.monthly_limit_usd)
                    "
                    class="mx-1 text-gray-300 dark:text-gray-600"
                    >·</span
                  >
                  <span v-if="row.weekly_limit_usd"
                    >${{ row.weekly_limit_usd }}/{{
                      t("admin.groups.limitWeek")
                    }}</span
                  >
                  <span
                    v-if="row.weekly_limit_usd && row.monthly_limit_usd"
                    class="mx-1 text-gray-300 dark:text-gray-600"
                    >·</span
                  >
                  <span v-if="row.monthly_limit_usd"
                    >${{ row.monthly_limit_usd }}/{{
                      t("admin.groups.limitMonth")
                    }}</span
                  >
                </template>
                <span v-else class="text-gray-400 dark:text-gray-500">{{
                  t("admin.groups.subscription.noLimit")
                }}</span>
              </div>
            </div>
          </template>

          <template #cell-rate_multiplier="{ value }">
            <span class="text-sm text-gray-700 dark:text-gray-300"
              >{{ value }}x</span
            >
          </template>

          <template #cell-is_exclusive="{ value }">
            <span :class="['badge', value ? 'badge-primary' : 'badge-gray']">
              {{
                value ? t("admin.groups.exclusive") : t("admin.groups.public")
              }}
            </span>
          </template>

          <template #cell-account_count="{ row }">
            <div class="space-y-0.5 text-xs">
              <div>
                <span class="text-gray-500 dark:text-gray-400">{{
                  t("admin.groups.accountsAvailable")
                }}</span>
                <span
                  class="ml-1 font-medium text-emerald-600 dark:text-emerald-400"
                  >{{ row.active_account_count || 0 }}</span
                >
                <span
                  class="ml-1 inline-flex items-center rounded bg-gray-100 px-1.5 py-0.5 font-medium text-gray-800 dark:bg-dark-600 dark:text-gray-300"
                  >{{ t("admin.groups.accountsUnit") }}</span
                >
              </div>
              <div v-if="row.rate_limited_account_count">
                <span class="text-gray-500 dark:text-gray-400">{{
                  t("admin.groups.accountsRateLimited")
                }}</span>
                <span
                  class="ml-1 font-medium text-amber-600 dark:text-amber-400"
                  >{{ row.rate_limited_account_count }}</span
                >
                <span
                  class="ml-1 inline-flex items-center rounded bg-gray-100 px-1.5 py-0.5 font-medium text-gray-800 dark:bg-dark-600 dark:text-gray-300"
                  >{{ t("admin.groups.accountsUnit") }}</span
                >
              </div>
              <div>
                <span class="text-gray-500 dark:text-gray-400">{{
                  t("admin.groups.accountsTotal")
                }}</span>
                <span
                  class="ml-1 font-medium text-gray-700 dark:text-gray-300"
                  >{{ row.account_count || 0 }}</span
                >
                <span
                  class="ml-1 inline-flex items-center rounded bg-gray-100 px-1.5 py-0.5 font-medium text-gray-800 dark:bg-dark-600 dark:text-gray-300"
                  >{{ t("admin.groups.accountsUnit") }}</span
                >
              </div>
            </div>
          </template>

          <template #cell-capacity="{ row }">
            <GroupCapacityBadge
              v-if="capacityMap.get(row.id)"
              :concurrency-used="capacityMap.get(row.id)!.concurrencyUsed"
              :concurrency-max="capacityMap.get(row.id)!.concurrencyMax"
              :sessions-used="capacityMap.get(row.id)!.sessionsUsed"
              :sessions-max="capacityMap.get(row.id)!.sessionsMax"
              :rpm-used="capacityMap.get(row.id)!.rpmUsed"
              :rpm-max="capacityMap.get(row.id)!.rpmMax"
            />
            <span v-else class="text-xs text-gray-400">—</span>
          </template>

          <template #cell-ingress_requests="{ row }">
            <div v-if="capacityLoaded && getIngressRequestsUsed(row.id) !== null"
              class="inline-flex items-center gap-1.5 rounded-md bg-sky-50 px-2 py-1 text-xs font-medium text-sky-700 dark:bg-sky-900/20 dark:text-sky-300"
              :title="t('admin.groups.columns.ingressRequestsHint')"
            >
              <Icon name="bolt" size="xs" />
              <span class="font-mono">{{ getIngressRequestsUsed(row.id) }}</span>
            </div>
            <span v-else class="text-xs text-gray-400" :title="t('admin.groups.ingressRequestsUnavailable')">—</span>
          </template>

          <template #cell-usage="{ row }">
            <div v-if="usageLoading" class="text-xs text-gray-400">—</div>
            <div v-else class="space-y-0.5 text-xs">
              <div class="text-gray-500 dark:text-gray-400">
                <span class="text-gray-400 dark:text-gray-500">{{
                  t("admin.groups.usageToday")
                }}</span>
                <span class="ml-1 font-medium text-gray-700 dark:text-gray-300"
                  >${{
                    formatCost(usageMap.get(row.id)?.today_cost ?? 0)
                  }}</span
                >
                <span class="ml-1 text-gray-400 dark:text-gray-500">
                  · {{ t("admin.groups.usageRequests", { count: formatRequestCount(usageMap.get(row.id)?.today_requests ?? 0) }) }}
                </span>
              </div>
              <div class="text-gray-500 dark:text-gray-400">
                <span class="text-gray-400 dark:text-gray-500">{{
                  t("admin.groups.usageTotal")
                }}</span>
                <span class="ml-1 font-medium text-gray-700 dark:text-gray-300"
                  >${{
                    formatCost(usageMap.get(row.id)?.total_cost ?? 0)
                  }}</span
                >
                <span class="ml-1 text-gray-400 dark:text-gray-500">
                  · {{ t("admin.groups.usageRequests", { count: formatRequestCount(usageMap.get(row.id)?.total_requests ?? 0) }) }}
                </span>
              </div>
              <div
                v-if="(usageMap.get(row.id)?.today_rerouted_requests ?? 0) > 0 || (usageMap.get(row.id)?.total_rerouted_requests ?? 0) > 0"
                class="text-[11px] text-amber-600 dark:text-amber-400"
              >
                {{ t("admin.groups.usageRerouted", { count: formatRequestCount(usageMap.get(row.id)?.total_rerouted_requests ?? 0) }) }}
              </div>
            </div>
          </template>

          <template #cell-status="{ value }">
            <span
              :class="[
                'badge',
                value === 'active' ? 'badge-success' : 'badge-danger',
              ]"
            >
              {{ t("admin.accounts.status." + value) }}
            </span>
          </template>

          <template #cell-actions="{ row }">
            <div class="flex items-center gap-1">
              <button
                @click="handleEdit(row)"
                class="flex flex-col items-center gap-0.5 rounded-lg p-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-primary-600 dark:hover:bg-dark-700 dark:hover:text-primary-400"
              >
                <Icon name="edit" size="sm" />
                <span class="text-xs">{{ t("common.edit") }}</span>
              </button>
              <button
                data-testid="group-duplicate"
                :title="
                  duplicatingGroupIds.has(row.id)
                    ? t('admin.groups.duplicating')
                    : t('admin.groups.duplicate')
                "
                :disabled="duplicatingGroupIds.has(row.id)"
                @click="handleDuplicate(row)"
                class="flex flex-col items-center gap-0.5 rounded-lg p-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-primary-600 disabled:cursor-not-allowed disabled:opacity-50 dark:hover:bg-dark-700 dark:hover:text-primary-400"
              >
                <Icon name="copy" size="sm" />
                <span class="text-xs">
                  {{
                    duplicatingGroupIds.has(row.id)
                      ? t("admin.groups.duplicating")
                      : t("admin.groups.duplicate")
                  }}
                </span>
              </button>
              <button
                v-if="row.platform === 'composite'"
                @click="handleCompositeRoutes(row)"
                class="flex flex-col items-center gap-0.5 rounded-lg p-1.5 text-gray-500 transition-colors hover:bg-cyan-50 hover:text-cyan-600 dark:hover:bg-cyan-900/20 dark:hover:text-cyan-400"
              >
                <Icon name="swap" size="sm" />
                <span class="text-xs">路由</span>
              </button>
              <button
                @click="handleBindAccounts(row)"
                class="flex flex-col items-center gap-0.5 rounded-lg p-1.5 text-gray-500 transition-colors hover:bg-primary-50 hover:text-primary-600 dark:hover:bg-primary-900/20 dark:hover:text-primary-400"
              >
                <Icon name="users" size="sm" />
                <span class="text-xs">{{ t("admin.groups.bindAccounts.button") }}</span>
              </button>
              <button
                @click="handleRateMultipliers(row)"
                class="flex flex-col items-center gap-0.5 rounded-lg p-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-purple-600 dark:hover:bg-dark-700 dark:hover:text-purple-400"
              >
                <Icon name="dollar" size="sm" />
                <span class="text-xs">{{
                  t("admin.groups.rateMultipliers")
                }}</span>
              </button>
              <button
                @click="handleRPMOverrides(row)"
                class="flex flex-col items-center gap-0.5 rounded-lg p-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-orange-600 dark:hover:bg-dark-700 dark:hover:text-orange-400"
              >
                <Icon name="bolt" size="sm" />
                <span class="text-xs">{{
                  t("admin.groups.rpmOverrides")
                }}</span>
              </button>
              <button
                @click="handleDelete(row)"
                class="flex flex-col items-center gap-0.5 rounded-lg p-1.5 text-gray-500 transition-colors hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20 dark:hover:text-red-400"
              >
                <Icon name="trash" size="sm" />
                <span class="text-xs">{{ t("common.delete") }}</span>
              </button>
            </div>
          </template>

          <template #empty>
            <EmptyState
              :title="t('admin.groups.noGroupsYet')"
              :description="t('admin.groups.createFirstGroup')"
              :action-text="t('admin.groups.createGroup')"
              @action="openCreateModal"
            />
          </template>
        </DataTable>
      </template>

      <template #pagination>
        <Pagination
          v-if="pagination.total > 0"
          :page="pagination.page"
          :total="pagination.total"
          :page-size="pagination.page_size"
          @update:page="handlePageChange"
          @update:pageSize="handlePageSizeChange"
        />
      </template>
    </TablePageLayout>

    <!-- Create Group Modal -->
    <BaseDialog
      :show="showCreateModal"
      :title="t('admin.groups.createGroup')"
      width="extra-wide"
      @close="closeCreateModal"
    >
      <form
        id="create-group-form"
        @submit.prevent="handleCreateGroup"
        class="grid gap-5 lg:grid-cols-[13rem_minmax(0,1fr)]"
      >
        <GroupEditorSectionNav
          v-model="createEditorSection"
          :sections="groupEditorSections"
          editor-id="create-group-editor"
        />
        <div class="min-w-0 space-y-5">
          <section
            v-show="createEditorSection === 'basics'"
            id="create-group-editor-basics"
            class="space-y-5"
            role="tabpanel"
            aria-labelledby="create-group-editor-basics-tab"
          >
        <div>
          <label class="input-label">{{ t("admin.groups.form.name") }}</label>
          <input
            v-model="createForm.name"
            type="text"
            required
            class="input"
            :placeholder="t('admin.groups.enterGroupName')"
            data-tour="group-form-name"
          />
        </div>
        <div>
          <label class="input-label">{{
            t("admin.groups.form.description")
          }}</label>
          <textarea
            v-model="createForm.description"
            rows="3"
            class="input"
            :placeholder="t('admin.groups.optionalDescription')"
          ></textarea>
        </div>
        <div>
          <label class="input-label">{{
            t("admin.groups.form.platform")
          }}</label>
          <Select
            v-model="createForm.platform"
            :options="platformOptions"
            data-tour="group-form-platform"
            @change="createForm.copy_accounts_from_group_ids = []"
          />
          <p
            class="input-hint"
            :class="!compositeGroupsEnabled && 'text-amber-600 dark:text-amber-400'"
          >
            {{
              compositeGroupsEnabled
                ? t("admin.groups.platformHint")
                : t("admin.groups.compositeDisabledHint")
            }}
          </p>
        </div>
        <GroupDisplayIdentityFields
          v-model:display-brand="createForm.display_brand"
          v-model:display-icon="createForm.display_icon"
          :platform="createForm.platform"
        />
        <GroupSchedulingModeFields v-model="createForm.scheduling_mode" :platform="createForm.platform" />
        <GroupPerUserConcurrencyFields v-model="createForm.per_user_concurrency" />
        <div class="rounded-lg border border-gray-200 p-4 dark:border-dark-600">
          <div class="flex items-start justify-between gap-4">
            <div>
              <p class="text-sm font-medium text-gray-900 dark:text-gray-100">
                {{ t("admin.groups.form.localOnly") }}
              </p>
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                {{ t("admin.groups.form.localOnlyHint") }}
              </p>
            </div>
            <button
              type="button"
              role="switch"
              :aria-label="t('admin.groups.form.localOnly')"
              :aria-checked="createForm.local_only"
              @click="createForm.local_only = !createForm.local_only"
              :class="[
                'relative inline-flex h-6 w-11 flex-none items-center rounded-full transition-colors',
                createForm.local_only ? 'bg-primary-500' : 'bg-gray-300 dark:bg-dark-600',
              ]"
            >
              <span :class="['inline-block h-4 w-4 rounded-full bg-white shadow transition-transform', createForm.local_only ? 'translate-x-6' : 'translate-x-1']" />
            </button>
          </div>
        </div>
        <!-- 从分组复制账号 -->
        <div v-if="copyAccountsGroupOptions.length > 0">
          <div class="mb-1.5 flex items-center gap-1">
            <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t("admin.groups.copyAccounts.title") }}
            </label>
            <div class="group relative inline-flex">
              <Icon
                name="questionCircle"
                size="sm"
                :stroke-width="2"
                class="cursor-help text-gray-400 transition-colors hover:text-primary-500 dark:text-gray-500 dark:hover:text-primary-400"
              />
              <div
                class="pointer-events-none absolute bottom-full left-0 z-50 mb-2 w-72 opacity-0 transition-all duration-200 group-hover:pointer-events-auto group-hover:opacity-100"
              >
                <div
                  class="rounded-lg bg-gray-900 p-3 text-white shadow-lg dark:bg-gray-800"
                >
                  <p class="text-xs leading-relaxed text-gray-300">
                    {{ t("admin.groups.copyAccounts.tooltip") }}
                  </p>
                  <div
                    class="absolute -bottom-1.5 left-3 h-3 w-3 rotate-45 bg-gray-900 dark:bg-gray-800"
                  ></div>
                </div>
              </div>
            </div>
          </div>
          <!-- 已选分组标签 -->
          <div
            v-if="createForm.copy_accounts_from_group_ids.length > 0"
            class="flex flex-wrap gap-1.5 mb-2"
          >
            <span
              v-for="groupId in createForm.copy_accounts_from_group_ids"
              :key="groupId"
              class="inline-flex items-center gap-1 rounded-full bg-primary-100 px-2.5 py-1 text-xs font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
            >
              {{
                copyAccountsGroupOptions.find((o) => o.value === groupId)
                  ?.label || `#${groupId}`
              }}
              <button
                type="button"
                @click="
                  createForm.copy_accounts_from_group_ids =
                    createForm.copy_accounts_from_group_ids.filter(
                      (id) => id !== groupId,
                    )
                "
                class="ml-0.5 text-primary-500 hover:text-primary-700 dark:hover:text-primary-200"
              >
                <Icon name="x" size="xs" />
              </button>
            </span>
          </div>
          <!-- 分组选择下拉 -->
          <select
            class="input"
            @change="
              (e) => {
                const val = Number((e.target as HTMLSelectElement).value);
                if (
                  val &&
                  !createForm.copy_accounts_from_group_ids.includes(val)
                ) {
                  createForm.copy_accounts_from_group_ids.push(val);
                }
                (e.target as HTMLSelectElement).value = '';
              }
            "
          >
            <option value="">
              {{ t("admin.groups.copyAccounts.selectPlaceholder") }}
            </option>
            <option
              v-for="opt in copyAccountsGroupOptions"
              :key="opt.value"
              :value="opt.value"
              :disabled="
                createForm.copy_accounts_from_group_ids.includes(opt.value)
              "
            >
              {{ opt.label }}
            </option>
          </select>
          <p class="input-hint">{{ t("admin.groups.copyAccounts.hint") }}</p>
        </div>
        <GroupOpenAILongContextBillingField
          v-if="createForm.platform === 'openai'"
          v-model="createForm.allow_openai_long_context_billing"
          :long-context-account-count="
            createOpenAILongContextBillingSummary.longContextAccountCount
          "
          :standard-account-count="
            createOpenAILongContextBillingSummary.standardAccountCount
          "
          :can-enable="createOpenAILongContextBillingSummary.canEnable"
          :uses-copied-groups="
            createOpenAILongContextBillingSummary.usesCopiedGroups
          "
        />
        <div class="rounded-lg border border-gray-200 p-4 dark:border-dark-600">
          <div class="flex items-start justify-between gap-4">
            <div>
              <p class="text-sm font-medium text-gray-900 dark:text-gray-100">
                {{ t("admin.groups.form.allowBlockedKeywordRequests") }}
              </p>
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                {{ t("admin.groups.form.allowBlockedKeywordRequestsHint") }}
              </p>
            </div>
            <button
              type="button"
              role="switch"
              :aria-checked="createForm.allow_blocked_keyword_requests"
              @click="createForm.allow_blocked_keyword_requests = !createForm.allow_blocked_keyword_requests"
              :class="[
                'relative inline-flex h-6 w-11 flex-none items-center rounded-full transition-colors',
                createForm.allow_blocked_keyword_requests ? 'bg-primary-500' : 'bg-gray-300 dark:bg-dark-600',
              ]"
            >
              <span :class="['inline-block h-4 w-4 rounded-full bg-white shadow transition-transform', createForm.allow_blocked_keyword_requests ? 'translate-x-6' : 'translate-x-1']" />
            </button>
          </div>
        </div>
        <div>
          <label class="input-label">分组最低等级</label>
          <div class="grid grid-cols-4 gap-2 rounded-lg border border-gray-200 bg-gray-50 p-1 dark:border-dark-600 dark:bg-dark-800">
            <button
              v-for="option in minimumTierOptions"
              :key="option.label"
              type="button"
              class="rounded-md px-2 py-2 text-xs font-medium transition-colors"
              :class="createForm.minimum_tier === option.value ? 'bg-primary-500 text-white' : 'text-gray-600 hover:bg-white dark:text-gray-300 dark:hover:bg-dark-700'"
              @click="createForm.minimum_tier = option.value"
            >{{ option.label }}</button>
          </div>
          <p class="input-hint">继承节点不会额外提高用户等级；T0/T1/T2 只能提高最终门槛。</p>
        </div>
        <div>
          <label class="input-label">{{
            t("admin.groups.form.rateMultiplier")
          }}</label>
          <input
            v-model.number="createForm.rate_multiplier"
            type="number"
            step="0.001"
            min="0.001"
            required
            class="input"
            data-tour="group-form-multiplier"
          />
          <p class="input-hint">{{ t("admin.groups.rateMultiplierHint") }}</p>
        </div>
        <GroupPeakRatePolicyFields
          v-model:enabled="createForm.peak_rate_enabled"
          v-model:start="createForm.peak_start"
          v-model:end="createForm.peak_end"
          v-model:multiplier="createForm.peak_rate_multiplier"
          v-model:timezone="createForm.peak_timezone"
        />
        <div>
          <label class="input-label">{{ t("admin.groups.form.rpmLimit") }}</label>
          <input
            v-model.number="createForm.rpm_limit"
            type="number"
            min="0"
            step="1"
            class="input"
            :placeholder="t('admin.groups.form.rpmLimitPlaceholder')"
          />
          <p class="input-hint">{{ t("admin.groups.form.rpmLimitHint") }}</p>
        </div>
        <ReasoningEffortPolicyFields
          v-if="createForm.platform === 'openai' || createForm.platform === 'composite'"
          ref="createReasoningEffortPolicyRef"
          id-prefix="create-group-reasoning"
          :platform="createForm.platform"
          v-model:max-effort="createForm.max_reasoning_effort"
          v-model:mappings="createForm.reasoning_effort_mappings"
        />
        <div
          v-if="createForm.subscription_type !== 'subscription'"
          data-tour="group-form-exclusive"
        >
          <div class="mb-1.5 flex items-center gap-1">
            <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t("admin.groups.form.exclusive") }}
            </label>
            <!-- Help Tooltip -->
            <div class="group relative inline-flex">
              <Icon
                name="questionCircle"
                size="sm"
                :stroke-width="2"
                class="cursor-help text-gray-400 transition-colors hover:text-primary-500 dark:text-gray-500 dark:hover:text-primary-400"
              />
              <!-- Tooltip Popover -->
              <div
                class="pointer-events-none absolute bottom-full left-0 z-50 mb-2 w-72 opacity-0 transition-all duration-200 group-hover:pointer-events-auto group-hover:opacity-100"
              >
                <div
                  class="rounded-lg bg-gray-900 p-3 text-white shadow-lg dark:bg-gray-800"
                >
                  <p class="mb-2 text-xs font-medium">
                    {{ t("admin.groups.exclusiveTooltip.title") }}
                  </p>
                  <p class="mb-2 text-xs leading-relaxed text-gray-300">
                    {{ t("admin.groups.exclusiveTooltip.description") }}
                  </p>
                  <div class="rounded bg-gray-800 p-2 dark:bg-gray-700">
                    <p class="text-xs leading-relaxed text-gray-300">
                      <span
                        class="inline-flex items-center gap-1 text-primary-400"
                        ><Icon name="lightbulb" size="xs" />
                        {{ t("admin.groups.exclusiveTooltip.example") }}</span
                      >
                      {{ t("admin.groups.exclusiveTooltip.exampleContent") }}
                    </p>
                  </div>
                  <!-- Arrow -->
                  <div
                    class="absolute -bottom-1.5 left-3 h-3 w-3 rotate-45 bg-gray-900 dark:bg-gray-800"
                  ></div>
                </div>
              </div>
            </div>
          </div>
          <div class="flex items-center gap-3">
            <button
              type="button"
              @click="createForm.is_exclusive = !createForm.is_exclusive"
              :class="[
                'relative inline-flex h-6 w-11 items-center rounded-full transition-colors',
                createForm.is_exclusive
                  ? 'bg-primary-500'
                  : 'bg-gray-300 dark:bg-dark-600',
              ]"
            >
              <span
                :class="[
                  'inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform',
                  createForm.is_exclusive ? 'translate-x-6' : 'translate-x-1',
                ]"
              />
            </button>
            <span class="text-sm text-gray-500 dark:text-gray-400">
              {{
                createForm.is_exclusive
                  ? t("admin.groups.exclusive")
                  : t("admin.groups.public")
              }}
            </span>
          </div>
        </div>

        <!-- Subscription Configuration -->
        <div class="mt-4 border-t pt-4">
          <div>
            <label class="input-label">{{
              t("admin.groups.subscription.type")
            }}</label>
            <Select
              v-model="createForm.subscription_type"
              :options="subscriptionTypeOptions"
            />
            <p class="input-hint">
              {{ t("admin.groups.subscription.typeHint") }}
            </p>
          </div>

          <!-- Subscription limits (only show when subscription type is selected) -->
          <div
            v-if="createForm.subscription_type === 'subscription'"
            class="space-y-4 border-l-2 border-primary-200 pl-4 dark:border-primary-800"
          >
            <div>
              <label class="input-label">{{
                t("admin.groups.subscription.dailyLimit")
              }}</label>
              <input
                v-model.number="createForm.daily_limit_usd"
                type="number"
                step="0.01"
                min="0"
                class="input"
                :placeholder="t('admin.groups.subscription.noLimit')"
              />
            </div>
            <div>
              <label class="input-label">{{
                t("admin.groups.subscription.weeklyLimit")
              }}</label>
              <input
                v-model.number="createForm.weekly_limit_usd"
                type="number"
                step="0.01"
                min="0"
                class="input"
                :placeholder="t('admin.groups.subscription.noLimit')"
              />
            </div>
            <div>
              <label class="input-label">{{
                t("admin.groups.subscription.monthlyLimit")
              }}</label>
              <input
                v-model.number="createForm.monthly_limit_usd"
                type="number"
                step="0.01"
                min="0"
                class="input"
                :placeholder="t('admin.groups.subscription.noLimit')"
              />
            </div>
          </div>
        </div>

          </section>

          <section
            v-show="createEditorSection === 'models'"
            id="create-group-editor-models"
            class="space-y-5"
            role="tabpanel"
            aria-labelledby="create-group-editor-models-tab"
          >
        <div class="border-t pt-4">
          <div class="mb-3 flex items-center justify-between gap-3">
            <div>
              <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
                {{ t("admin.groups.modelsList.title") }}
              </label>
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                {{ t("admin.groups.modelsList.hint") }}
              </p>
            </div>
            <button
              type="button"
              @click="createModelsListState.enabled = !createModelsListState.enabled"
              :class="[
                'relative inline-flex h-6 w-11 flex-shrink-0 items-center rounded-full transition-colors',
                createModelsListState.enabled
                  ? 'bg-primary-500'
                  : 'bg-gray-300 dark:bg-dark-600',
              ]"
            >
              <span
                :class="[
                  'inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform',
                  createModelsListState.enabled ? 'translate-x-6' : 'translate-x-1',
                ]"
              />
            </button>
          </div>
          <div
            v-if="createModelsListState.enabled"
            class="overflow-hidden rounded-lg border border-gray-200 bg-gray-50/50 dark:border-dark-600 dark:bg-dark-800/40"
          >
            <div
              v-if="!createModelsListLoading && createModelsListState.items.length > 0"
              class="flex items-center justify-between gap-2 border-b border-gray-200 bg-gray-50 px-3 py-2 text-xs dark:border-dark-600 dark:bg-dark-800"
            >
              <span class="text-gray-500 dark:text-gray-400">
                已选 {{ createModelsListSelectedCount }} /
                {{ createModelsListState.items.length }}
              </span>
              <div class="flex items-center gap-1.5">
                <button
                  type="button"
                  class="rounded px-2 py-1 font-medium text-primary-600 transition-colors hover:bg-primary-50 dark:text-primary-400 dark:hover:bg-primary-900/20"
                  @click="selectAllModelsListItems(createModelsListState)"
                >
                  全选
                </button>
                <button
                  type="button"
                  class="rounded px-2 py-1 font-medium text-gray-600 transition-colors hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-dark-700"
                  @click="invertModelsListSelection(createModelsListState)"
                >
                  反选
                </button>
              </div>
            </div>
            <div
              class="max-h-64 space-y-2 overflow-y-auto p-2"
            >
              <p v-if="createModelsListLoading" class="text-xs text-gray-500 dark:text-gray-400">
                {{ t("admin.groups.modelsList.loading") }}
              </p>
              <p
                v-else-if="createModelsListState.items.length === 0"
                class="text-xs text-gray-500 dark:text-gray-400"
              >
                {{ t("admin.groups.modelsList.empty") }}
              </p>
              <div
                v-for="(item, index) in createModelsListState.items"
                :key="item.id"
                class="flex items-center gap-2 rounded border border-gray-200 bg-white px-3 py-2 dark:border-dark-600 dark:bg-dark-800"
              >
                <input
                  v-model="item.selected"
                  type="checkbox"
                  class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
                />
                <span class="min-w-0 flex-1 break-all text-sm text-gray-700 dark:text-gray-300">
                  {{ item.id }}
                </span>
                <button
                  type="button"
                  :disabled="index === 0"
                  class="rounded p-1 text-gray-400 hover:bg-gray-100 hover:text-gray-700 disabled:opacity-40 dark:hover:bg-dark-600 dark:hover:text-gray-200"
                  @click="moveCreateModelsListItem(index, index - 1)"
                >
                  <Icon name="arrowUp" size="sm" />
                </button>
                <button
                  type="button"
                  :disabled="index === createModelsListState.items.length - 1"
                  class="rounded p-1 text-gray-400 hover:bg-gray-100 hover:text-gray-700 disabled:opacity-40 dark:hover:bg-dark-600 dark:hover:text-gray-200"
                  @click="moveCreateModelsListItem(index, index + 1)"
                >
                  <Icon name="arrowDown" size="sm" />
                </button>
              </div>
            </div>
          </div>
        </div>

        <GroupModelMappingFields
          v-model="createForm.model_mapping_rows"
          :candidates="createModelsListCandidates"
          :loading="createModelsListLoading"
          :platform="createForm.platform"
        />
        <div
          v-if="createForm.platform === 'openai'"
          class="rounded-lg border border-gray-200 p-4 dark:border-dark-600"
        >
          <label class="input-label" for="create-group-fallback-model">
            {{ t("admin.groups.form.fallbackModel") }}
          </label>
          <input
            id="create-group-fallback-model"
            v-model.trim="createForm.fallback_model"
            type="text"
            maxlength="100"
            class="input"
            :placeholder="t('admin.groups.form.fallbackModelPlaceholder')"
          />
          <p class="input-hint">
            {{ t("admin.groups.form.fallbackModelHint") }}
          </p>
          <div class="mt-4 border-t border-gray-200 pt-4 dark:border-dark-600">
            <label class="input-label" for="create-group-failover-group">
              {{ t("admin.groups.form.failoverGroup") }}
            </label>
            <select
              id="create-group-failover-group"
              v-model="createForm.failover_group_id"
              class="input"
            >
              <option :value="null">{{ t("admin.groups.form.failoverGroupDisabled") }}</option>
              <option v-for="option in failoverGroupOptions" :key="option.id" :value="option.id">
                {{ option.name }}
              </option>
            </select>
            <p class="input-hint">
              {{ t("admin.groups.form.failoverGroupHint") }}
            </p>
            <label class="input-label mt-3" for="create-group-failover-strategy">
              {{ t("admin.groups.form.failoverStrategy") }}
            </label>
            <select
              id="create-group-failover-strategy"
              v-model="createForm.failover_strategy"
              class="input"
              :disabled="!createForm.failover_group_id"
            >
              <option value="original_model_first">{{ t("admin.groups.form.failoverStrategyOriginal") }}</option>
              <option value="current_group_first">{{ t("admin.groups.form.failoverStrategyCurrent") }}</option>
            </select>
          </div>
        </div>
        <GroupBillingModelSourceFields v-model="createForm.billing_model_source" />
        <GroupPricingFields
          v-model="createPricingForm"
          :platform="createForm.platform"
        />
        <GroupResponseModelFields
          v-model="createForm.response_model_mode"
          v-model:custom-value="createForm.response_model_custom"
        />

          </section>

          <section
            v-show="createEditorSection === 'media'"
            id="create-group-editor-media"
            class="space-y-5"
            role="tabpanel"
            aria-labelledby="create-group-editor-media-tab"
          >
        <div v-if="supportsLivePlatform(createForm.platform)" class="border-t pt-4">
          <label class="block mb-2 font-medium text-gray-700 dark:text-gray-300">
            {{ t("admin.groups.live.title") }}
          </label>
          <label class="flex items-start gap-3 rounded-lg border border-gray-200 bg-gray-50 p-3 dark:border-dark-600 dark:bg-dark-800">
            <input
              v-model="createForm.allow_live"
              type="checkbox"
              class="mt-0.5 h-4 w-4 rounded border-gray-300 text-blue-600 focus:ring-blue-500"
            />
            <span>
              <span class="block text-sm font-medium text-gray-800 dark:text-gray-200">{{ t("admin.groups.live.allow") }}</span>
              <span class="mt-1 block text-xs text-gray-500 dark:text-gray-400">{{ t("admin.groups.live.hint") }}</span>
            </span>
          </label>
        </div>

        <!-- 图片生成计费配置 -->
        <div
          v-if="
            createForm.platform === 'antigravity' ||
            createForm.platform === 'gemini' ||
            createForm.platform === 'openai' ||
            createForm.platform === 'grok' ||
            createForm.platform === 'composite'
          "
          class="border-t pt-4"
        >
          <label
            class="block mb-2 font-medium text-gray-700 dark:text-gray-300"
          >
            {{ t("admin.groups.imagePricing.title") }}
          </label>
          <p class="text-xs text-gray-500 dark:text-gray-400 mb-3">
            {{ t("admin.groups.imagePricing.description") }}
          </p>
          <div class="mb-4 grid grid-cols-1 gap-3 md:grid-cols-2">
            <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
              <input
                v-model="createForm.allow_image_generation"
                type="checkbox"
                class="rounded border-gray-300 text-blue-600 focus:ring-blue-500"
              />
              {{
                t(
                  createForm.platform === "grok"
                    ? "admin.groups.imagePricing.allowGrokMediaGeneration"
                    : "admin.groups.imagePricing.allowImageGeneration",
                )
              }}
            </label>
            <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
              <input
                v-model="createForm.image_rate_independent"
                type="checkbox"
                class="rounded border-gray-300 text-blue-600 focus:ring-blue-500"
              />
              {{ t("admin.groups.imagePricing.independentMultiplier") }}
            </label>
          </div>
          <div
            v-if="createForm.image_rate_independent"
            class="mb-4"
          >
            <label class="input-label">{{
              t("admin.groups.imagePricing.imageMultiplier")
            }}</label>
            <input
              v-model.number="createForm.image_rate_multiplier"
              type="number"
              step="0.0001"
              min="0"
              class="input"
              placeholder="1"
            />
          </div>
          <div
            class="grid gap-3"
            :class="createForm.platform === 'grok' ? 'grid-cols-2' : 'grid-cols-3'"
          >
            <div>
              <label class="input-label">1K ($)</label>
              <input
                v-model.number="createForm.image_price_1k"
                type="number"
                step="0.001"
                min="0"
                class="input"
                placeholder="0.134"
              />
            </div>
            <div>
              <label class="input-label">2K ($)</label>
              <input
                v-model.number="createForm.image_price_2k"
                type="number"
                step="0.001"
                min="0"
                class="input"
                placeholder="0.201"
              />
            </div>
            <div v-if="createForm.platform !== 'grok'">
              <label class="input-label">4K ($)</label>
              <input
                v-model.number="createForm.image_price_4k"
                type="number"
                step="0.001"
                min="0"
                class="input"
                placeholder="0.268"
              />
            </div>
          </div>
          <p class="mt-3 text-xs text-gray-500 dark:text-gray-400">
            {{ t("admin.groups.imagePricing.modeHint") }}
          </p>
          <div class="mt-2 rounded-lg bg-gray-50 p-3 text-xs text-gray-700 dark:bg-gray-800 dark:text-gray-300">
            <div class="mb-1 font-medium">
              {{ t("admin.groups.imagePricing.finalPricePreview") }}
            </div>
            <div
              class="grid gap-2"
              :class="createForm.platform === 'grok' ? 'grid-cols-2' : 'grid-cols-3'"
            >
              <div
                v-for="item in createImageFinalPricePreview"
                :key="item.label"
              >
                {{ item.label }}: {{ item.value }}
              </div>
            </div>
          </div>
        </div>

        <!-- Grok 视频生成按秒计费配置；媒体权限沿用上方分组能力开关。 -->
        <div
          v-if="createForm.platform === 'grok'"
          class="border-t pt-4"
        >
          <label class="block mb-2 font-medium text-gray-700 dark:text-gray-300">
            {{ t("admin.groups.videoPricing.title") }}
          </label>
          <p class="text-xs text-gray-500 dark:text-gray-400 mb-3">
            {{ t("admin.groups.videoPricing.description") }}
          </p>
          <div class="mb-4">
            <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
              <input
                v-model="createForm.video_rate_independent"
                type="checkbox"
                class="rounded border-gray-300 text-blue-600 focus:ring-blue-500"
              />
              {{ t("admin.groups.videoPricing.independentMultiplier") }}
            </label>
          </div>
          <div v-if="createForm.video_rate_independent" class="mb-4">
            <label class="input-label">{{
              t("admin.groups.videoPricing.videoMultiplier")
            }}</label>
            <input
              v-model.number="createForm.video_rate_multiplier"
              type="number"
              step="0.0001"
              min="0"
              class="input"
              placeholder="1"
            />
          </div>
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
            <div>
              <label class="input-label">480p ($ / s)</label>
              <input
                v-model.number="createForm.video_price_480p"
                type="number"
                step="0.001"
                min="0"
                class="input"
                :placeholder="t('admin.groups.videoPricing.useCatalogPrice')"
              />
            </div>
            <div>
              <label class="input-label">720p ($ / s)</label>
              <input
                v-model.number="createForm.video_price_720p"
                type="number"
                step="0.001"
                min="0"
                class="input"
                :placeholder="t('admin.groups.videoPricing.useCatalogPrice')"
              />
            </div>
            <div>
              <label class="input-label">1080p ($ / s)</label>
              <input
                v-model.number="createForm.video_price_1080p"
                type="number"
                step="0.001"
                min="0"
                class="input"
                :placeholder="t('admin.groups.videoPricing.useCatalogPrice')"
              />
            </div>
          </div>
          <div class="mt-4 overflow-hidden rounded-lg border border-gray-200 dark:border-gray-700">
            <div class="grid grid-cols-4 bg-gray-50 text-xs font-medium text-gray-600 dark:bg-gray-800 dark:text-gray-300">
              <div class="px-3 py-2">{{ t("admin.groups.videoPricing.modelFamily") }}</div>
              <div v-for="tier in grokVideoPriceTiers" :key="tier" class="px-3 py-2 text-center">
                {{ tier }} ($ / s)
              </div>
            </div>
            <div
              v-for="family in grokVideoPriceFamilies"
              :key="family"
              class="grid grid-cols-4 border-t border-gray-200 dark:border-gray-700"
            >
              <code class="px-3 py-2 text-xs text-gray-700 dark:text-gray-200">{{ family }}</code>
              <div v-for="tier in grokVideoPriceTiers" :key="tier" class="p-2">
                <input
                  v-model.number="createForm.video_model_prices[family][tier]"
                  type="number"
                  step="0.001"
                  min="0"
                  class="input text-center"
                  :placeholder="t('admin.groups.videoPricing.useCatalogPrice')"
                />
              </div>
            </div>
          </div>
          <p class="mt-3 text-xs text-gray-500 dark:text-gray-400">
            {{ t("admin.groups.videoPricing.modeHint") }}
          </p>
          <div class="mt-2 rounded-lg bg-gray-50 p-3 text-xs text-gray-700 dark:bg-gray-800 dark:text-gray-300">
            <div class="mb-1 font-medium">
              {{ t("admin.groups.videoPricing.finalPricePreview") }}
            </div>
            <div class="grid grid-cols-1 gap-2 sm:grid-cols-3">
              <div v-for="item in createVideoFinalPricePreview" :key="item.label">
                {{ item.label }}: {{ item.value }}
              </div>
            </div>
          </div>
        </div>

        <div v-if="createForm.platform === 'openai'" class="border-t pt-4">
          <label class="block mb-2 font-medium text-gray-700 dark:text-gray-300">
            {{ t("admin.groups.webSearchPricing.title") }}
          </label>
          <label class="input-label">
            {{ t("admin.groups.webSearchPricing.pricePerCall") }}
          </label>
          <input
            v-model.number="createForm.web_search_price_per_call"
            type="number"
            step="0.001"
            min="0"
            class="input"
            placeholder="0.01"
          />
          <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">
            {{ t("admin.groups.webSearchPricing.pricePerCallHint") }}
          </p>
          <div class="mt-2 rounded-lg bg-gray-50 p-3 text-xs text-gray-700 dark:bg-gray-800 dark:text-gray-300">
            {{
              t("admin.groups.webSearchPricing.finalPricePreview", {
                price: createWebSearchFinalPricePreview,
              })
            }}
          </div>
        </div>

          </section>

          <section
            v-show="createEditorSection === 'advanced'"
            id="create-group-editor-advanced"
            class="space-y-5"
            role="tabpanel"
            aria-labelledby="create-group-editor-advanced-tab"
          >
        <!-- 支持的模型系列（仅 antigravity 平台） -->
        <div v-if="createForm.platform === 'antigravity'" class="border-t pt-4">
          <div class="mb-1.5 flex items-center gap-1">
            <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t("admin.groups.supportedScopes.title") }}
            </label>
            <!-- Help Tooltip -->
            <div class="group relative inline-flex">
              <Icon
                name="questionCircle"
                size="sm"
                :stroke-width="2"
                class="cursor-help text-gray-400 transition-colors hover:text-primary-500 dark:text-gray-500 dark:hover:text-primary-400"
              />
              <div
                class="pointer-events-none absolute bottom-full left-0 z-50 mb-2 w-72 opacity-0 transition-all duration-200 group-hover:pointer-events-auto group-hover:opacity-100"
              >
                <div
                  class="rounded-lg bg-gray-900 p-3 text-white shadow-lg dark:bg-gray-800"
                >
                  <p class="text-xs leading-relaxed text-gray-300">
                    {{ t("admin.groups.supportedScopes.tooltip") }}
                  </p>
                  <div
                    class="absolute -bottom-1.5 left-3 h-3 w-3 rotate-45 bg-gray-900 dark:bg-gray-800"
                  ></div>
                </div>
              </div>
            </div>
          </div>
          <div class="space-y-2">
            <label class="flex items-center gap-2 cursor-pointer">
              <input
                type="checkbox"
                :checked="createForm.supported_model_scopes.includes('claude')"
                @change="toggleCreateScope('claude')"
                class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-600 dark:bg-dark-700"
              />
              <span class="text-sm text-gray-700 dark:text-gray-300">{{
                t("admin.groups.supportedScopes.claude")
              }}</span>
            </label>
            <label class="flex items-center gap-2 cursor-pointer">
              <input
                type="checkbox"
                :checked="
                  createForm.supported_model_scopes.includes('gemini_text')
                "
                @change="toggleCreateScope('gemini_text')"
                class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-600 dark:bg-dark-700"
              />
              <span class="text-sm text-gray-700 dark:text-gray-300">{{
                t("admin.groups.supportedScopes.geminiText")
              }}</span>
            </label>
            <label class="flex items-center gap-2 cursor-pointer">
              <input
                type="checkbox"
                :checked="
                  createForm.supported_model_scopes.includes('gemini_image')
                "
                @change="toggleCreateScope('gemini_image')"
                class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-600 dark:bg-dark-700"
              />
              <span class="text-sm text-gray-700 dark:text-gray-300">{{
                t("admin.groups.supportedScopes.geminiImage")
              }}</span>
            </label>
          </div>
          <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">
            {{ t("admin.groups.supportedScopes.hint") }}
          </p>
        </div>

        <!-- MCP XML 协议注入（仅 antigravity 平台） -->
        <div v-if="createForm.platform === 'antigravity'" class="border-t pt-4">
          <div class="mb-1.5 flex items-center gap-1">
            <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t("admin.groups.mcpXml.title") }}
            </label>
            <div class="group relative inline-flex">
              <Icon
                name="questionCircle"
                size="sm"
                :stroke-width="2"
                class="cursor-help text-gray-400 transition-colors hover:text-primary-500 dark:text-gray-500 dark:hover:text-primary-400"
              />
              <div
                class="pointer-events-none absolute bottom-full left-0 z-50 mb-2 w-72 opacity-0 transition-all duration-200 group-hover:pointer-events-auto group-hover:opacity-100"
              >
                <div
                  class="rounded-lg bg-gray-900 p-3 text-white shadow-lg dark:bg-gray-800"
                >
                  <p class="text-xs leading-relaxed text-gray-300">
                    {{ t("admin.groups.mcpXml.tooltip") }}
                  </p>
                  <div
                    class="absolute -bottom-1.5 left-3 h-3 w-3 rotate-45 bg-gray-900 dark:bg-gray-800"
                  ></div>
                </div>
              </div>
            </div>
          </div>
          <div class="flex items-center gap-3">
            <button
              type="button"
              @click="createForm.mcp_xml_inject = !createForm.mcp_xml_inject"
              :class="[
                'relative inline-flex h-6 w-11 items-center rounded-full transition-colors',
                createForm.mcp_xml_inject
                  ? 'bg-primary-500'
                  : 'bg-gray-300 dark:bg-dark-600',
              ]"
            >
              <span
                :class="[
                  'inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform',
                  createForm.mcp_xml_inject ? 'translate-x-6' : 'translate-x-1',
                ]"
              />
            </button>
            <span class="text-sm text-gray-500 dark:text-gray-400">
              {{
                createForm.mcp_xml_inject
                  ? t("admin.groups.mcpXml.enabled")
                  : t("admin.groups.mcpXml.disabled")
              }}
            </span>
          </div>
        </div>

        <!-- Claude Code 客户端限制（仅 anthropic 平台） -->
        <div v-if="createForm.platform === 'anthropic'" class="border-t pt-4">
          <div class="mb-1.5 flex items-center gap-1">
            <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t("admin.groups.claudeCode.title") }}
            </label>
            <!-- Help Tooltip -->
            <div class="group relative inline-flex">
              <Icon
                name="questionCircle"
                size="sm"
                :stroke-width="2"
                class="cursor-help text-gray-400 transition-colors hover:text-primary-500 dark:text-gray-500 dark:hover:text-primary-400"
              />
              <div
                class="pointer-events-none absolute bottom-full left-0 z-50 mb-2 w-72 opacity-0 transition-all duration-200 group-hover:pointer-events-auto group-hover:opacity-100"
              >
                <div
                  class="rounded-lg bg-gray-900 p-3 text-white shadow-lg dark:bg-gray-800"
                >
                  <p class="text-xs leading-relaxed text-gray-300">
                    {{ t("admin.groups.claudeCode.tooltip") }}
                  </p>
                  <div
                    class="absolute -bottom-1.5 left-3 h-3 w-3 rotate-45 bg-gray-900 dark:bg-gray-800"
                  ></div>
                </div>
              </div>
            </div>
          </div>
          <div class="flex items-center gap-3">
            <button
              type="button"
              @click="
                createForm.claude_code_only = !createForm.claude_code_only
              "
              :class="[
                'relative inline-flex h-6 w-11 items-center rounded-full transition-colors',
                createForm.claude_code_only
                  ? 'bg-primary-500'
                  : 'bg-gray-300 dark:bg-dark-600',
              ]"
            >
              <span
                :class="[
                  'inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform',
                  createForm.claude_code_only
                    ? 'translate-x-6'
                    : 'translate-x-1',
                ]"
              />
            </button>
            <span class="text-sm text-gray-500 dark:text-gray-400">
              {{
                createForm.claude_code_only
                  ? t("admin.groups.claudeCode.enabled")
                  : t("admin.groups.claudeCode.disabled")
              }}
            </span>
          </div>
          <!-- 降级分组选择（仅当启用 claude_code_only 时显示） -->
          <div v-if="createForm.claude_code_only" class="mt-3">
            <label class="input-label">{{
              t("admin.groups.claudeCode.fallbackGroup")
            }}</label>
            <Select
              v-model="createForm.fallback_group_id"
              :options="fallbackGroupOptions"
              :placeholder="t('admin.groups.claudeCode.noFallback')"
            />
            <p class="input-hint">
              {{ t("admin.groups.claudeCode.fallbackHint") }}
            </p>
          </div>
        </div>

        <!-- OpenAI Messages 调度配置（仅 openai 平台） -->
        <div
          v-if="createForm.platform === 'openai'"
          class="border-t border-gray-200 dark:border-dark-400 pt-4 mt-4"
        >
          <h4 class="text-sm font-medium text-gray-700 dark:text-gray-300 mb-3">
            {{ t("admin.groups.openaiMessages.title") }}
          </h4>

          <!-- 允许 Messages 调度开关 -->
          <div class="flex items-center justify-between">
            <label class="text-sm text-gray-600 dark:text-gray-400">{{
              t("admin.groups.openaiMessages.allowDispatch")
            }}</label>
            <button
              type="button"
              @click="
                createForm.allow_messages_dispatch =
                  !createForm.allow_messages_dispatch
              "
              class="relative inline-flex h-6 w-12 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none"
              :class="
                createForm.allow_messages_dispatch
                  ? 'bg-primary-500'
                  : 'bg-gray-300 dark:bg-dark-600'
              "
            >
              <span
                class="pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out"
                :class="
                  createForm.allow_messages_dispatch
                    ? 'translate-x-6'
                    : 'translate-x-1'
                "
              />
            </button>
          </div>
          <p class="text-xs text-gray-500 dark:text-gray-400 mt-1">
            {{ t("admin.groups.openaiMessages.allowDispatchHint") }}
          </p>

          <div v-if="createForm.allow_messages_dispatch" class="mt-3">
            <div
              class="relative overflow-hidden rounded-xl border border-gray-200 bg-white shadow-sm dark:border-dark-600 dark:bg-dark-800"
            >
              <div
                class="border-b border-gray-100 bg-gray-50/80 px-4 py-3 dark:border-dark-700 dark:bg-dark-700/50"
              >
                <div class="flex items-center gap-2">
                  <div class="h-2 w-2 rounded-full bg-blue-500"></div>
                  <label
                    class="text-sm font-medium text-gray-900 dark:text-white"
                    >{{
                      t("admin.groups.openaiMessages.familyMappingTitle")
                    }}</label
                  >
                </div>
                <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                  {{ t("admin.groups.openaiMessages.familyMappingHint") }}
                </p>
              </div>
              <div class="p-4">
                <div class="grid gap-4 md:grid-cols-3">
                  <div>
                    <label class="input-label">{{
                      t("admin.groups.openaiMessages.opusModel")
                    }}</label>
                    <input
                      v-model="createForm.opus_mapped_model"
                      type="text"
                      :placeholder="
                        t('admin.groups.openaiMessages.opusModelPlaceholder')
                      "
                      class="input"
                    />
                  </div>
                  <div>
                    <label class="input-label">{{
                      t("admin.groups.openaiMessages.sonnetModel")
                    }}</label>
                    <input
                      v-model="createForm.sonnet_mapped_model"
                      type="text"
                      :placeholder="
                        t('admin.groups.openaiMessages.sonnetModelPlaceholder')
                      "
                      class="input"
                    />
                  </div>
                  <div>
                    <label class="input-label">{{
                      t("admin.groups.openaiMessages.haikuModel")
                    }}</label>
                    <input
                      v-model="createForm.haiku_mapped_model"
                      type="text"
                      :placeholder="
                        t('admin.groups.openaiMessages.haikuModelPlaceholder')
                      "
                      class="input"
                    />
                  </div>
                </div>
              </div>
            </div>

            <div
              class="mt-5 relative overflow-hidden rounded-xl border border-primary-200 bg-white shadow-sm dark:border-primary-900/50 dark:bg-dark-800"
            >
              <div
                class="border-b border-primary-100 bg-primary-50/80 px-4 py-3 dark:border-primary-900/40 dark:bg-primary-900/20"
              >
                <div class="flex items-start justify-between gap-3">
                  <div>
                    <div class="flex items-center gap-2">
                      <div class="h-2 w-2 rounded-full bg-primary-500"></div>
                      <label
                        class="text-sm font-medium text-primary-900 dark:text-primary-100"
                        >{{
                          t("admin.groups.openaiMessages.exactMappingTitle")
                        }}</label
                      >
                    </div>
                    <p
                      class="mt-1 text-xs text-primary-600/90 dark:text-primary-400/90"
                    >
                      {{ t("admin.groups.openaiMessages.exactMappingHint") }}
                    </p>
                  </div>
                </div>
              </div>

              <div class="p-4 bg-gray-50/30 dark:bg-dark-800/30">
                <div
                  v-if="createForm.exact_model_mappings.length === 0"
                  class="flex items-center justify-between gap-3 rounded-xl border-2 border-dashed border-primary-200 bg-white px-5 py-4 text-sm text-primary-700 transition-colors hover:border-primary-300 dark:border-primary-900/40 dark:bg-dark-800 dark:text-primary-300 dark:hover:border-primary-800"
                >
                  <span>{{
                    t("admin.groups.openaiMessages.noExactMappings")
                  }}</span>
                  <button
                    type="button"
                    @click="addCreateMessagesDispatchMapping"
                    class="flex items-center gap-1.5 text-sm font-medium text-primary-600 transition-colors hover:text-primary-700 dark:text-primary-400 dark:hover:text-primary-300"
                  >
                    <Icon name="plus" size="sm" />
                    {{ t("admin.groups.openaiMessages.addExactMapping") }}
                  </button>
                </div>

                <div v-else class="space-y-3">
                  <div
                    v-for="row in createForm.exact_model_mappings"
                    :key="getCreateMessagesDispatchRowKey(row)"
                    class="group relative rounded-xl border border-gray-200 bg-white p-4 shadow-sm transition-all hover:border-primary-300 hover:shadow-md dark:border-dark-600 dark:bg-dark-700 dark:hover:border-primary-700"
                  >
                    <div class="flex items-center gap-4">
                      <div
                        class="grid flex-1 gap-4 md:grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] md:items-start"
                      >
                        <div>
                          <label class="input-label">{{
                            t("admin.groups.openaiMessages.claudeModel")
                          }}</label>
                          <input
                            v-model="row.claude_model"
                            type="text"
                            :placeholder="
                              t(
                                'admin.groups.openaiMessages.claudeModelPlaceholder',
                              )
                            "
                            class="input bg-gray-50 focus:bg-white dark:bg-dark-800 dark:focus:bg-dark-900"
                          />
                        </div>
                        <div
                          class="hidden md:flex md:justify-center md:pt-7 text-primary-300 dark:text-primary-700"
                        >
                          <Icon
                            name="arrowRight"
                            size="sm"
                            class="transition-transform group-hover:translate-x-1"
                          />
                        </div>
                        <div>
                          <label class="input-label">{{
                            t("admin.groups.openaiMessages.targetModel")
                          }}</label>
                          <input
                            v-model="row.target_model"
                            type="text"
                            :placeholder="
                              t(
                                'admin.groups.openaiMessages.targetModelPlaceholder',
                              )
                            "
                            class="input bg-gray-50 focus:bg-white dark:bg-dark-800 dark:focus:bg-dark-900"
                          />
                        </div>
                      </div>
                      <button
                        type="button"
                        @click="removeCreateMessagesDispatchMapping(row)"
                        class="mt-6 flex h-9 w-9 items-center justify-center rounded-lg text-gray-400 transition-colors hover:bg-red-50 hover:text-red-500 dark:hover:bg-red-900/20 dark:hover:text-red-400"
                        :title="
                          t('admin.groups.openaiMessages.removeExactMapping')
                        "
                      >
                        <Icon name="trash" size="sm" />
                      </button>
                    </div>
                  </div>

                  <button
                    type="button"
                    @click="addCreateMessagesDispatchMapping"
                    class="flex w-full items-center justify-center gap-2 rounded-xl border-2 border-dashed border-gray-300 bg-white py-3 text-sm font-medium text-gray-500 transition-all hover:border-primary-300 hover:bg-primary-50/50 hover:text-primary-600 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-400 dark:hover:border-primary-800 dark:hover:bg-primary-900/20 dark:hover:text-primary-400"
                  >
                    <Icon name="plus" size="sm" />
                    {{ t("admin.groups.openaiMessages.addExactMapping") }}
                  </button>
                </div>
              </div>
            </div>
          </div>
        </div>

        <!-- 账号过滤控制 (OpenAI/Antigravity/Anthropic/Gemini) -->
        <div
          v-if="
            ['openai', 'antigravity', 'anthropic', 'gemini'].includes(
              createForm.platform,
            )
          "
          class="border-t border-gray-200 dark:border-dark-400 pt-4 mt-4 space-y-4"
        >
          <h4 class="text-sm font-medium text-gray-700 dark:text-gray-300 mb-3">
            账号过滤控制
          </h4>

          <!-- require_oauth_only toggle -->
          <div class="flex items-center justify-between">
            <div>
              <label class="text-sm text-gray-600 dark:text-gray-400"
                >仅允许 OAuth 账号</label
              >
              <p class="text-xs text-gray-500 dark:text-gray-400 mt-0.5">
                {{
                  createForm.require_oauth_only
                    ? "已启用 — 排除 API Key 类型账号"
                    : "未启用"
                }}
              </p>
            </div>
            <button
              type="button"
              @click="
                createForm.require_oauth_only = !createForm.require_oauth_only
              "
              class="relative inline-flex h-6 w-12 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none"
              :class="
                createForm.require_oauth_only
                  ? 'bg-primary-500'
                  : 'bg-gray-300 dark:bg-dark-600'
              "
            >
              <span
                class="pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out"
                :class="
                  createForm.require_oauth_only
                    ? 'translate-x-6'
                    : 'translate-x-1'
                "
              />
            </button>
          </div>

          <!-- require_privacy_set toggle -->
          <div class="flex items-center justify-between">
            <div>
              <label class="text-sm text-gray-600 dark:text-gray-400"
                >仅允许隐私保护已设置的账号</label
              >
              <p class="text-xs text-gray-500 dark:text-gray-400 mt-0.5">
                {{
                  createForm.require_privacy_set
                    ? "已启用 — Privacy 未设置的账号将被排除"
                    : "未启用"
                }}
              </p>
            </div>
            <button
              type="button"
              @click="
                createForm.require_privacy_set = !createForm.require_privacy_set
              "
              class="relative inline-flex h-6 w-12 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none"
              :class="
                createForm.require_privacy_set
                  ? 'bg-primary-500'
                  : 'bg-gray-300 dark:bg-dark-600'
              "
            >
              <span
                class="pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out"
                :class="
                  createForm.require_privacy_set
                    ? 'translate-x-6'
                    : 'translate-x-1'
                "
              />
            </button>
          </div>
        </div>

        <!-- 无效请求兜底（仅 anthropic/antigravity 平台，且非订阅分组） -->
        <div
          v-if="
            ['anthropic', 'antigravity'].includes(createForm.platform) &&
            createForm.subscription_type !== 'subscription'
          "
          class="border-t pt-4"
        >
          <label class="input-label">{{
            t("admin.groups.invalidRequestFallback.title")
          }}</label>
          <Select
            v-model="createForm.fallback_group_id_on_invalid_request"
            :options="invalidRequestFallbackOptions"
            :placeholder="t('admin.groups.invalidRequestFallback.noFallback')"
          />
          <p class="input-hint">
            {{ t("admin.groups.invalidRequestFallback.hint") }}
          </p>
        </div>

        <!-- 模型路由配置（仅 anthropic 平台） -->
        <div v-if="createForm.platform === 'anthropic'" class="border-t pt-4">
          <div class="mb-1.5 flex items-center gap-1">
            <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t("admin.groups.modelRouting.title") }}
            </label>
            <!-- Help Tooltip -->
            <div class="group relative inline-flex">
              <Icon
                name="questionCircle"
                size="sm"
                :stroke-width="2"
                class="cursor-help text-gray-400 transition-colors hover:text-primary-500 dark:text-gray-500 dark:hover:text-primary-400"
              />
              <div
                class="pointer-events-none absolute bottom-full left-0 z-50 mb-2 w-80 opacity-0 transition-all duration-200 group-hover:pointer-events-auto group-hover:opacity-100"
              >
                <div
                  class="rounded-lg bg-gray-900 p-3 text-white shadow-lg dark:bg-gray-800"
                >
                  <p class="text-xs leading-relaxed text-gray-300">
                    {{ t("admin.groups.modelRouting.tooltip") }}
                  </p>
                  <div
                    class="absolute -bottom-1.5 left-3 h-3 w-3 rotate-45 bg-gray-900 dark:bg-gray-800"
                  ></div>
                </div>
              </div>
            </div>
          </div>
          <!-- 启用开关 -->
          <div class="flex items-center gap-3 mb-3">
            <button
              type="button"
              @click="
                createForm.model_routing_enabled =
                  !createForm.model_routing_enabled
              "
              :class="[
                'relative inline-flex h-6 w-11 items-center rounded-full transition-colors',
                createForm.model_routing_enabled
                  ? 'bg-primary-500'
                  : 'bg-gray-300 dark:bg-dark-600',
              ]"
            >
              <span
                :class="[
                  'inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform',
                  createForm.model_routing_enabled
                    ? 'translate-x-6'
                    : 'translate-x-1',
                ]"
              />
            </button>
            <span class="text-sm text-gray-500 dark:text-gray-400">
              {{
                createForm.model_routing_enabled
                  ? t("admin.groups.modelRouting.enabled")
                  : t("admin.groups.modelRouting.disabled")
              }}
            </span>
          </div>
          <p
            v-if="!createForm.model_routing_enabled"
            class="text-xs text-gray-500 dark:text-gray-400 mb-3"
          >
            {{ t("admin.groups.modelRouting.disabledHint") }}
          </p>
          <p v-else class="text-xs text-gray-500 dark:text-gray-400 mb-3">
            {{ t("admin.groups.modelRouting.noRulesHint") }}
          </p>
          <!-- 路由规则列表（仅在启用时显示） -->
          <div v-if="createForm.model_routing_enabled" class="space-y-3">
            <div
              v-for="rule in createModelRoutingRules"
              :key="getCreateRuleRenderKey(rule)"
              class="rounded-lg border border-gray-200 p-3 dark:border-dark-600"
            >
              <div class="flex items-start gap-3">
                <div class="flex-1 space-y-2">
                  <div>
                    <label class="input-label text-xs">{{
                      t("admin.groups.modelRouting.modelPattern")
                    }}</label>
                    <input
                      v-model="rule.pattern"
                      type="text"
                      class="input text-sm"
                      :placeholder="
                        t('admin.groups.modelRouting.modelPatternPlaceholder')
                      "
                    />
                  </div>
                  <div>
                    <label class="input-label text-xs">{{
                      t("admin.groups.modelRouting.accounts")
                    }}</label>
                    <!-- 已选账号标签 -->
                    <div
                      v-if="rule.accounts.length > 0"
                      class="flex flex-wrap gap-1.5 mb-2"
                    >
                      <span
                        v-for="account in rule.accounts"
                        :key="account.id"
                        class="inline-flex items-center gap-1 rounded-full bg-primary-100 px-2.5 py-1 text-xs font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
                      >
                        {{ account.name }}
                        <button
                          type="button"
                          @click="removeSelectedAccount(rule, account.id)"
                          class="ml-0.5 text-primary-500 hover:text-primary-700 dark:hover:text-primary-200"
                        >
                          <Icon name="x" size="xs" />
                        </button>
                      </span>
                    </div>
                    <!-- 账号搜索输入框 -->
                    <div class="relative account-search-container">
                      <input
                        v-model="
                          accountSearchKeyword[getCreateRuleSearchKey(rule)]
                        "
                        type="text"
                        class="input text-sm"
                        :placeholder="
                          t(
                            'admin.groups.modelRouting.searchAccountPlaceholder',
                          )
                        "
                        @input="searchAccountsByRule(rule)"
                        @focus="onAccountSearchFocus(rule)"
                      />
                      <!-- 搜索结果下拉框 -->
                      <div
                        v-if="
                          showAccountDropdown[getCreateRuleSearchKey(rule)] &&
                          accountSearchResults[getCreateRuleSearchKey(rule)]
                            ?.length > 0
                        "
                        class="absolute z-50 mt-1 max-h-48 w-full overflow-auto rounded-lg border bg-white shadow-lg dark:border-dark-600 dark:bg-dark-800"
                      >
                        <button
                          v-for="account in accountSearchResults[
                            getCreateRuleSearchKey(rule)
                          ]"
                          :key="account.id"
                          type="button"
                          @click="selectAccount(rule, account)"
                          class="w-full px-3 py-2 text-left text-sm hover:bg-gray-100 dark:hover:bg-dark-700"
                          :class="{
                            'opacity-50': rule.accounts.some(
                              (a) => a.id === account.id,
                            ),
                          }"
                          :disabled="
                            rule.accounts.some((a) => a.id === account.id)
                          "
                        >
                          <span>{{ account.name }}</span>
                          <span class="ml-2 text-xs text-gray-400"
                            >#{{ account.id }}</span
                          >
                        </button>
                      </div>
                    </div>
                    <p class="text-xs text-gray-400 mt-1">
                      {{ t("admin.groups.modelRouting.accountsHint") }}
                    </p>
                  </div>
                </div>
                <button
                  type="button"
                  @click="removeCreateRoutingRule(rule)"
                  class="mt-5 p-1.5 text-gray-400 hover:text-red-500 transition-colors"
                  :title="t('admin.groups.modelRouting.removeRule')"
                >
                  <Icon name="trash" size="sm" />
                </button>
              </div>
            </div>
          </div>
          <!-- 添加规则按钮（仅在启用时显示） -->
          <button
            v-if="createForm.model_routing_enabled"
            type="button"
            @click="addCreateRoutingRule"
            class="mt-3 flex items-center gap-1.5 text-sm text-primary-600 hover:text-primary-700 dark:text-primary-400 dark:hover:text-primary-300"
          >
            <Icon name="plus" size="sm" />
            {{ t("admin.groups.modelRouting.addRule") }}
          </button>
        </div>
          </section>
        </div>
      </form>

      <template #footer>
        <div class="flex justify-end gap-3 pt-4">
          <button
            @click="closeCreateModal"
            type="button"
            class="btn btn-secondary"
          >
            {{ t("common.cancel") }}
          </button>
          <button
            type="submit"
            form="create-group-form"
            :disabled="submitting"
            class="btn btn-primary"
            data-tour="group-form-submit"
          >
            <svg
              v-if="submitting"
              class="-ml-1 mr-2 h-4 w-4 animate-spin"
              fill="none"
              viewBox="0 0 24 24"
            >
              <circle
                class="opacity-25"
                cx="12"
                cy="12"
                r="10"
                stroke="currentColor"
                stroke-width="4"
              ></circle>
              <path
                class="opacity-75"
                fill="currentColor"
                d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
              ></path>
            </svg>
            {{ submitting ? t("admin.groups.creating") : t("common.create") }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <!-- Edit Group Modal -->
    <BaseDialog
      :show="showEditModal"
      :title="t('admin.groups.editGroup')"
      width="extra-wide"
      @close="closeEditModal"
    >
      <form
        v-if="editingGroup"
        id="edit-group-form"
        @submit.prevent="handleUpdateGroup"
        class="grid gap-5 lg:grid-cols-[13rem_minmax(0,1fr)]"
      >
        <GroupEditorSectionNav
          v-model="editEditorSection"
          :sections="groupEditorSections"
          editor-id="edit-group-editor"
        />
        <div class="min-w-0 space-y-5">
          <section
            v-show="editEditorSection === 'basics'"
            id="edit-group-editor-basics"
            class="space-y-5"
            role="tabpanel"
            aria-labelledby="edit-group-editor-basics-tab"
          >
        <div>
          <label class="input-label">{{ t("admin.groups.form.name") }}</label>
          <input
            v-model="editForm.name"
            type="text"
            required
            class="input"
            data-tour="edit-group-form-name"
          />
        </div>
        <div>
          <label class="input-label">{{
            t("admin.groups.form.description")
          }}</label>
          <textarea
            v-model="editForm.description"
            rows="3"
            class="input"
          ></textarea>
        </div>
        <div>
          <label class="input-label">{{
            t("admin.groups.form.platform")
          }}</label>
          <Select
            v-model="editForm.platform"
            :options="platformOptions"
            :disabled="true"
            data-tour="group-form-platform"
          />
          <p class="input-hint">{{ t("admin.groups.platformNotEditable") }}</p>
        </div>
        <GroupDisplayIdentityFields
          v-model:display-brand="editForm.display_brand"
          v-model:display-icon="editForm.display_icon"
          :platform="editForm.platform"
        />
        <GroupSchedulingModeFields v-model="editForm.scheduling_mode" :platform="editForm.platform" />
        <GroupPerUserConcurrencyFields v-model="editForm.per_user_concurrency" />
        <div class="rounded-lg border border-gray-200 p-4 dark:border-dark-600">
          <div class="flex items-start justify-between gap-4">
            <div>
              <p class="text-sm font-medium text-gray-900 dark:text-gray-100">
                {{ t("admin.groups.form.localOnly") }}
              </p>
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                {{ t("admin.groups.form.localOnlyHint") }}
              </p>
            </div>
            <button
              type="button"
              role="switch"
              :aria-label="t('admin.groups.form.localOnly')"
              :aria-checked="editForm.local_only"
              @click="editForm.local_only = !editForm.local_only"
              :class="[
                'relative inline-flex h-6 w-11 flex-none items-center rounded-full transition-colors',
                editForm.local_only ? 'bg-primary-500' : 'bg-gray-300 dark:bg-dark-600',
              ]"
            >
              <span :class="['inline-block h-4 w-4 rounded-full bg-white shadow transition-transform', editForm.local_only ? 'translate-x-6' : 'translate-x-1']" />
            </button>
          </div>
        </div>
        <!-- 从分组复制账号（编辑时） -->
        <div v-if="copyAccountsGroupOptionsForEdit.length > 0">
          <div class="mb-1.5 flex items-center gap-1">
            <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t("admin.groups.copyAccounts.title") }}
            </label>
            <div class="group relative inline-flex">
              <Icon
                name="questionCircle"
                size="sm"
                :stroke-width="2"
                class="cursor-help text-gray-400 transition-colors hover:text-primary-500 dark:text-gray-500 dark:hover:text-primary-400"
              />
              <div
                class="pointer-events-none absolute bottom-full left-0 z-50 mb-2 w-72 opacity-0 transition-all duration-200 group-hover:pointer-events-auto group-hover:opacity-100"
              >
                <div
                  class="rounded-lg bg-gray-900 p-3 text-white shadow-lg dark:bg-gray-800"
                >
                  <p class="text-xs leading-relaxed text-gray-300">
                    {{ t("admin.groups.copyAccounts.tooltipEdit") }}
                  </p>
                  <div
                    class="absolute -bottom-1.5 left-3 h-3 w-3 rotate-45 bg-gray-900 dark:bg-gray-800"
                  ></div>
                </div>
              </div>
            </div>
          </div>
          <!-- 已选分组标签 -->
          <div
            v-if="editForm.copy_accounts_from_group_ids.length > 0"
            class="flex flex-wrap gap-1.5 mb-2"
          >
            <span
              v-for="groupId in editForm.copy_accounts_from_group_ids"
              :key="groupId"
              class="inline-flex items-center gap-1 rounded-full bg-primary-100 px-2.5 py-1 text-xs font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
            >
              {{
                copyAccountsGroupOptionsForEdit.find((o) => o.value === groupId)
                  ?.label || `#${groupId}`
              }}
              <button
                type="button"
                @click="
                  editForm.copy_accounts_from_group_ids =
                    editForm.copy_accounts_from_group_ids.filter(
                      (id) => id !== groupId,
                    )
                "
                class="ml-0.5 text-primary-500 hover:text-primary-700 dark:hover:text-primary-200"
              >
                <Icon name="x" size="xs" />
              </button>
            </span>
          </div>
          <!-- 分组选择下拉 -->
          <select
            class="input"
            @change="
              (e) => {
                const val = Number((e.target as HTMLSelectElement).value);
                if (
                  val &&
                  !editForm.copy_accounts_from_group_ids.includes(val)
                ) {
                  editForm.copy_accounts_from_group_ids.push(val);
                }
                (e.target as HTMLSelectElement).value = '';
              }
            "
          >
            <option value="">
              {{ t("admin.groups.copyAccounts.selectPlaceholder") }}
            </option>
            <option
              v-for="opt in copyAccountsGroupOptionsForEdit"
              :key="opt.value"
              :value="opt.value"
              :disabled="
                editForm.copy_accounts_from_group_ids.includes(opt.value)
              "
            >
              {{ opt.label }}
            </option>
          </select>
          <p class="input-hint">
            {{ t("admin.groups.copyAccounts.hintEdit") }}
          </p>
        </div>
        <GroupOpenAILongContextBillingField
          v-if="editForm.platform === 'openai'"
          v-model="editForm.allow_openai_long_context_billing"
          :long-context-account-count="
            editOpenAILongContextBillingSummary.longContextAccountCount
          "
          :standard-account-count="
            editOpenAILongContextBillingSummary.standardAccountCount
          "
          :can-enable="editOpenAILongContextBillingSummary.canEnable"
          :uses-copied-groups="
            editOpenAILongContextBillingSummary.usesCopiedGroups
          "
        />
        <div class="rounded-lg border border-gray-200 p-4 dark:border-dark-600">
          <div class="flex items-start justify-between gap-4">
            <div>
              <p class="text-sm font-medium text-gray-900 dark:text-gray-100">
                {{ t("admin.groups.form.allowBlockedKeywordRequests") }}
              </p>
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                {{ t("admin.groups.form.allowBlockedKeywordRequestsHint") }}
              </p>
            </div>
            <button
              type="button"
              role="switch"
              :aria-checked="editForm.allow_blocked_keyword_requests"
              @click="editForm.allow_blocked_keyword_requests = !editForm.allow_blocked_keyword_requests"
              :class="[
                'relative inline-flex h-6 w-11 flex-none items-center rounded-full transition-colors',
                editForm.allow_blocked_keyword_requests ? 'bg-primary-500' : 'bg-gray-300 dark:bg-dark-600',
              ]"
            >
              <span :class="['inline-block h-4 w-4 rounded-full bg-white shadow transition-transform', editForm.allow_blocked_keyword_requests ? 'translate-x-6' : 'translate-x-1']" />
            </button>
          </div>
        </div>
        <div>
          <label class="input-label">分组最低等级</label>
          <div class="grid grid-cols-4 gap-2 rounded-lg border border-gray-200 bg-gray-50 p-1 dark:border-dark-600 dark:bg-dark-800">
            <button
              v-for="option in minimumTierOptions"
              :key="option.label"
              type="button"
              class="rounded-md px-2 py-2 text-xs font-medium transition-colors"
              :class="editForm.minimum_tier === option.value ? 'bg-primary-500 text-white' : 'text-gray-600 hover:bg-white dark:text-gray-300 dark:hover:bg-dark-700'"
              @click="editForm.minimum_tier = option.value"
            >{{ option.label }}</button>
          </div>
          <p class="input-hint">主站会与节点等级取较高值，并在用户选择分组时显示最终等级。</p>
        </div>
        <div>
          <label class="input-label">{{
            t("admin.groups.form.rateMultiplier")
          }}</label>
          <input
            v-model.number="editForm.rate_multiplier"
            type="number"
            step="0.001"
            min="0.001"
            required
            class="input"
            data-tour="group-form-multiplier"
          />
        </div>
        <GroupPeakRatePolicyFields
          v-model:enabled="editForm.peak_rate_enabled"
          v-model:start="editForm.peak_start"
          v-model:end="editForm.peak_end"
          v-model:multiplier="editForm.peak_rate_multiplier"
          v-model:timezone="editForm.peak_timezone"
        />
        <div>
          <label class="input-label">{{ t("admin.groups.form.rpmLimit") }}</label>
          <input
            v-model.number="editForm.rpm_limit"
            type="number"
            min="0"
            step="1"
            class="input"
            :placeholder="t('admin.groups.form.rpmLimitPlaceholder')"
          />
          <p class="input-hint">{{ t("admin.groups.form.rpmLimitHint") }}</p>
        </div>
        <ReasoningEffortPolicyFields
          v-if="editForm.platform === 'openai' || editForm.platform === 'composite'"
          ref="editReasoningEffortPolicyRef"
          id-prefix="edit-group-reasoning"
          :platform="editForm.platform"
          v-model:max-effort="editForm.max_reasoning_effort"
          v-model:mappings="editForm.reasoning_effort_mappings"
        />
        <div v-if="editForm.subscription_type !== 'subscription'">
          <div class="mb-1.5 flex items-center gap-1">
            <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t("admin.groups.form.exclusive") }}
            </label>
            <!-- Help Tooltip -->
            <div class="group relative inline-flex">
              <Icon
                name="questionCircle"
                size="sm"
                :stroke-width="2"
                class="cursor-help text-gray-400 transition-colors hover:text-primary-500 dark:text-gray-500 dark:hover:text-primary-400"
              />
              <!-- Tooltip Popover -->
              <div
                class="pointer-events-none absolute bottom-full left-0 z-50 mb-2 w-72 opacity-0 transition-all duration-200 group-hover:pointer-events-auto group-hover:opacity-100"
              >
                <div
                  class="rounded-lg bg-gray-900 p-3 text-white shadow-lg dark:bg-gray-800"
                >
                  <p class="mb-2 text-xs font-medium">
                    {{ t("admin.groups.exclusiveTooltip.title") }}
                  </p>
                  <p class="mb-2 text-xs leading-relaxed text-gray-300">
                    {{ t("admin.groups.exclusiveTooltip.description") }}
                  </p>
                  <div class="rounded bg-gray-800 p-2 dark:bg-gray-700">
                    <p class="text-xs leading-relaxed text-gray-300">
                      <span
                        class="inline-flex items-center gap-1 text-primary-400"
                        ><Icon name="lightbulb" size="xs" />
                        {{ t("admin.groups.exclusiveTooltip.example") }}</span
                      >
                      {{ t("admin.groups.exclusiveTooltip.exampleContent") }}
                    </p>
                  </div>
                  <!-- Arrow -->
                  <div
                    class="absolute -bottom-1.5 left-3 h-3 w-3 rotate-45 bg-gray-900 dark:bg-gray-800"
                  ></div>
                </div>
              </div>
            </div>
          </div>
          <div class="flex items-center gap-3">
            <button
              type="button"
              @click="editForm.is_exclusive = !editForm.is_exclusive"
              :class="[
                'relative inline-flex h-6 w-11 items-center rounded-full transition-colors',
                editForm.is_exclusive
                  ? 'bg-primary-500'
                  : 'bg-gray-300 dark:bg-dark-600',
              ]"
            >
              <span
                :class="[
                  'inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform',
                  editForm.is_exclusive ? 'translate-x-6' : 'translate-x-1',
                ]"
              />
            </button>
            <span class="text-sm text-gray-500 dark:text-gray-400">
              {{
                editForm.is_exclusive
                  ? t("admin.groups.exclusive")
                  : t("admin.groups.public")
              }}
            </span>
          </div>
        </div>
        <div>
          <label class="input-label">{{ t("admin.groups.form.status") }}</label>
          <Select v-model="editForm.status" :options="editStatusOptions" />
        </div>

        <!-- Subscription Configuration -->
        <div class="mt-4 border-t pt-4">
          <div>
            <label class="input-label">{{
              t("admin.groups.subscription.type")
            }}</label>
            <Select
              v-model="editForm.subscription_type"
              :options="subscriptionTypeOptions"
              :disabled="true"
            />
            <p class="input-hint">
              {{ t("admin.groups.subscription.typeNotEditable") }}
            </p>
          </div>

          <!-- Subscription limits (only show when subscription type is selected) -->
          <div
            v-if="editForm.subscription_type === 'subscription'"
            class="space-y-4 border-l-2 border-primary-200 pl-4 dark:border-primary-800"
          >
            <div>
              <label class="input-label">{{
                t("admin.groups.subscription.dailyLimit")
              }}</label>
              <input
                v-model.number="editForm.daily_limit_usd"
                type="number"
                step="0.01"
                min="0"
                class="input"
                :placeholder="t('admin.groups.subscription.noLimit')"
              />
            </div>
            <div>
              <label class="input-label">{{
                t("admin.groups.subscription.weeklyLimit")
              }}</label>
              <input
                v-model.number="editForm.weekly_limit_usd"
                type="number"
                step="0.01"
                min="0"
                class="input"
                :placeholder="t('admin.groups.subscription.noLimit')"
              />
            </div>
            <div>
              <label class="input-label">{{
                t("admin.groups.subscription.monthlyLimit")
              }}</label>
              <input
                v-model.number="editForm.monthly_limit_usd"
                type="number"
                step="0.01"
                min="0"
                class="input"
                :placeholder="t('admin.groups.subscription.noLimit')"
              />
            </div>
          </div>
        </div>

          </section>

          <section
            v-show="editEditorSection === 'models'"
            id="edit-group-editor-models"
            class="space-y-5"
            role="tabpanel"
            aria-labelledby="edit-group-editor-models-tab"
          >
        <div class="border-t pt-4">
          <div class="mb-3 flex items-center justify-between gap-3">
            <div>
              <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
                {{ t("admin.groups.modelsList.title") }}
              </label>
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                {{ t("admin.groups.modelsList.hint") }}
              </p>
            </div>
            <button
              type="button"
              @click="editModelsListState.enabled = !editModelsListState.enabled"
              :class="[
                'relative inline-flex h-6 w-11 flex-shrink-0 items-center rounded-full transition-colors',
                editModelsListState.enabled
                  ? 'bg-primary-500'
                  : 'bg-gray-300 dark:bg-dark-600',
              ]"
            >
              <span
                :class="[
                  'inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform',
                  editModelsListState.enabled ? 'translate-x-6' : 'translate-x-1',
                ]"
              />
            </button>
          </div>
          <div
            v-if="editModelsListState.enabled"
            class="overflow-hidden rounded-lg border border-gray-200 bg-gray-50/50 dark:border-dark-600 dark:bg-dark-800/40"
          >
            <div
              v-if="!editModelsListLoading && editModelsListState.items.length > 0"
              class="flex items-center justify-between gap-2 border-b border-gray-200 bg-gray-50 px-3 py-2 text-xs dark:border-dark-600 dark:bg-dark-800"
            >
              <span class="text-gray-500 dark:text-gray-400">
                已选 {{ editModelsListSelectedCount }} /
                {{ editModelsListState.items.length }}
              </span>
              <div class="flex items-center gap-1.5">
                <button
                  type="button"
                  class="rounded px-2 py-1 font-medium text-primary-600 transition-colors hover:bg-primary-50 dark:text-primary-400 dark:hover:bg-primary-900/20"
                  @click="selectAllModelsListItems(editModelsListState)"
                >
                  全选
                </button>
                <button
                  type="button"
                  class="rounded px-2 py-1 font-medium text-gray-600 transition-colors hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-dark-700"
                  @click="invertModelsListSelection(editModelsListState)"
                >
                  反选
                </button>
              </div>
            </div>
            <div
              class="max-h-64 space-y-2 overflow-y-auto p-2"
            >
              <p v-if="editModelsListLoading" class="text-xs text-gray-500 dark:text-gray-400">
                {{ t("admin.groups.modelsList.loading") }}
              </p>
              <p
                v-else-if="editModelsListState.items.length === 0"
                class="text-xs text-gray-500 dark:text-gray-400"
              >
                {{ t("admin.groups.modelsList.empty") }}
              </p>
              <div
                v-for="(item, index) in editModelsListState.items"
                :key="item.id"
                class="flex items-center gap-2 rounded border border-gray-200 bg-white px-3 py-2 dark:border-dark-600 dark:bg-dark-800"
              >
                <input
                  v-model="item.selected"
                  type="checkbox"
                  class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
                />
                <span class="min-w-0 flex-1 break-all text-sm text-gray-700 dark:text-gray-300">
                  {{ item.id }}
                </span>
                <button
                  type="button"
                  :disabled="index === 0"
                  class="rounded p-1 text-gray-400 hover:bg-gray-100 hover:text-gray-700 disabled:opacity-40 dark:hover:bg-dark-600 dark:hover:text-gray-200"
                  @click="moveEditModelsListItem(index, index - 1)"
                >
                  <Icon name="arrowUp" size="sm" />
                </button>
                <button
                  type="button"
                  :disabled="index === editModelsListState.items.length - 1"
                  class="rounded p-1 text-gray-400 hover:bg-gray-100 hover:text-gray-700 disabled:opacity-40 dark:hover:bg-dark-600 dark:hover:text-gray-200"
                  @click="moveEditModelsListItem(index, index + 1)"
                >
                  <Icon name="arrowDown" size="sm" />
                </button>
              </div>
            </div>
          </div>
        </div>

        <div v-if="createForm.platform === 'grok'" class="border-t pt-4">
          <label class="block mb-2 font-medium text-gray-700 dark:text-gray-300">
            {{ t("admin.groups.voicePricing.title") }}
          </label>
          <p class="mb-3 text-xs text-gray-500 dark:text-gray-400">
            {{ t("admin.groups.voicePricing.description") }}
          </p>
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
            <div>
              <label class="input-label">{{ t("admin.groups.voicePricing.realtime") }}</label>
              <input v-model.number="createForm.audio_realtime_price_per_min" type="number" min="0" step="0.001" class="input" placeholder="0.10" />
            </div>
            <div>
              <label class="input-label">{{ t("admin.groups.voicePricing.tts") }}</label>
              <input v-model.number="createForm.audio_tts_price_per_million_chars" type="number" min="0" step="0.01" class="input" placeholder="15" />
            </div>
            <div>
              <label class="input-label">{{ t("admin.groups.voicePricing.stt") }}</label>
              <input v-model.number="createForm.audio_stt_price_per_hour" type="number" min="0" step="0.001" class="input" placeholder="0.36" />
            </div>
          </div>
        </div>

        <div v-if="createForm.platform === 'grok'" class="border-t pt-4">
          <label class="block mb-2 font-medium text-gray-700 dark:text-gray-300">
            {{ t("admin.groups.grokSearchPricing.title") }}
          </label>
          <label class="input-label">
            {{ t("admin.groups.grokSearchPricing.pricePer1k") }}
          </label>
          <input
            v-model.number="createForm.search_price_per_1k"
            type="number"
            step="0.01"
            min="0"
            class="input"
            placeholder="10"
          />
          <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">
            {{ t("admin.groups.grokSearchPricing.pricePer1kHint") }}
          </p>
          <div class="mt-2 rounded-lg bg-gray-50 p-3 text-xs text-gray-700 dark:bg-gray-800 dark:text-gray-300">
            {{
              t("admin.groups.grokSearchPricing.finalPricePreview", {
                price: createGrokSearchFinalPricePreview,
              })
            }}
          </div>
        </div>

        <GroupModelMappingFields
          v-model="editForm.model_mapping_rows"
          :candidates="editModelsListCandidates"
          :loading="editModelsListLoading"
          :platform="editForm.platform"
          :group-id="editingGroup?.id ?? null"
          :health="editRouteHealth"
          @reset-route-health="handleResetRouteHealth"
        />
        <div
          v-if="editForm.platform === 'openai'"
          class="rounded-lg border border-gray-200 p-4 dark:border-dark-600"
        >
          <label class="input-label" for="edit-group-fallback-model">
            {{ t("admin.groups.form.fallbackModel") }}
          </label>
          <input
            id="edit-group-fallback-model"
            v-model.trim="editForm.fallback_model"
            type="text"
            maxlength="100"
            class="input"
            :placeholder="t('admin.groups.form.fallbackModelPlaceholder')"
          />
          <p class="input-hint">
            {{ t("admin.groups.form.fallbackModelHint") }}
          </p>
          <div class="mt-4 border-t border-gray-200 pt-4 dark:border-dark-600">
            <label class="input-label" for="edit-group-failover-group">
              {{ t("admin.groups.form.failoverGroup") }}
            </label>
            <select
              id="edit-group-failover-group"
              v-model="editForm.failover_group_id"
              class="input"
            >
              <option :value="null">{{ t("admin.groups.form.failoverGroupDisabled") }}</option>
              <option v-for="option in failoverGroupOptions" :key="option.id" :value="option.id">
                {{ option.name }}
              </option>
            </select>
            <p class="input-hint">
              {{ t("admin.groups.form.failoverGroupHint") }}
            </p>
            <label class="input-label mt-3" for="edit-group-failover-strategy">
              {{ t("admin.groups.form.failoverStrategy") }}
            </label>
            <select
              id="edit-group-failover-strategy"
              v-model="editForm.failover_strategy"
              class="input"
              :disabled="!editForm.failover_group_id"
            >
              <option value="original_model_first">{{ t("admin.groups.form.failoverStrategyOriginal") }}</option>
              <option value="current_group_first">{{ t("admin.groups.form.failoverStrategyCurrent") }}</option>
            </select>
          </div>
        </div>
        <GroupBillingModelSourceFields v-model="editForm.billing_model_source" />
        <div
          v-if="editPricingLoadError"
          class="rounded-xl border border-red-200 bg-red-50 p-4 dark:border-red-900/60 dark:bg-red-950/20"
        >
          <p class="text-sm font-medium text-red-700 dark:text-red-300">
            {{ t("admin.groups.pricing.loadFailed") }}
          </p>
          <p class="mt-1 text-xs leading-5 text-red-600/80 dark:text-red-300/80">
            {{ t("admin.groups.pricing.loadFailedHint") }}
          </p>
          <button
            type="button"
            class="btn btn-secondary mt-3 min-h-9 text-xs"
            @click="retryEditPricing"
          >
            <Icon name="refresh" size="xs" class="mr-1" />
            {{ t("common.retry") }}
          </button>
        </div>
        <GroupPricingFields
          v-else
          v-model="editPricingForm"
          :platform="editForm.platform"
          :group-id="editingGroup.id"
          :loading="editPricingLoading"
          :shared="editPricingShared"
        />
        <GroupResponseModelFields
          v-model="editForm.response_model_mode"
          v-model:custom-value="editForm.response_model_custom"
        />

          </section>

          <section
            v-show="editEditorSection === 'media'"
            id="edit-group-editor-media"
            class="space-y-5"
            role="tabpanel"
            aria-labelledby="edit-group-editor-media-tab"
          >
        <div v-if="supportsLivePlatform(editForm.platform)" class="border-t pt-4">
          <label class="block mb-2 font-medium text-gray-700 dark:text-gray-300">
            {{ t("admin.groups.live.title") }}
          </label>
          <label class="flex items-start gap-3 rounded-lg border border-gray-200 bg-gray-50 p-3 dark:border-dark-600 dark:bg-dark-800">
            <input
              v-model="editForm.allow_live"
              type="checkbox"
              class="mt-0.5 h-4 w-4 rounded border-gray-300 text-blue-600 focus:ring-blue-500"
            />
            <span>
              <span class="block text-sm font-medium text-gray-800 dark:text-gray-200">{{ t("admin.groups.live.allow") }}</span>
              <span class="mt-1 block text-xs text-gray-500 dark:text-gray-400">{{ t("admin.groups.live.hint") }}</span>
            </span>
          </label>
        </div>

        <!-- 图片生成计费配置 -->
        <div
          v-if="
            editForm.platform === 'antigravity' ||
            editForm.platform === 'gemini' ||
            editForm.platform === 'openai' ||
            editForm.platform === 'grok' ||
            editForm.platform === 'composite'
          "
          class="border-t pt-4"
        >
          <label
            class="block mb-2 font-medium text-gray-700 dark:text-gray-300"
          >
            {{ t("admin.groups.imagePricing.title") }}
          </label>
          <p class="text-xs text-gray-500 dark:text-gray-400 mb-3">
            {{ t("admin.groups.imagePricing.description") }}
          </p>
          <div class="mb-4 grid grid-cols-1 gap-3 md:grid-cols-2">
            <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
              <input
                v-model="editForm.allow_image_generation"
                type="checkbox"
                class="rounded border-gray-300 text-blue-600 focus:ring-blue-500"
              />
              {{
                t(
                  editForm.platform === "grok"
                    ? "admin.groups.imagePricing.allowGrokMediaGeneration"
                    : "admin.groups.imagePricing.allowImageGeneration",
                )
              }}
            </label>
            <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
              <input
                v-model="editForm.image_rate_independent"
                type="checkbox"
                class="rounded border-gray-300 text-blue-600 focus:ring-blue-500"
              />
              {{ t("admin.groups.imagePricing.independentMultiplier") }}
            </label>
          </div>
          <div
            v-if="editForm.image_rate_independent"
            class="mb-4"
          >
            <label class="input-label">{{
              t("admin.groups.imagePricing.imageMultiplier")
            }}</label>
            <input
              v-model.number="editForm.image_rate_multiplier"
              type="number"
              step="0.0001"
              min="0"
              class="input"
              placeholder="1"
            />
          </div>
          <div
            class="grid gap-3"
            :class="editForm.platform === 'grok' ? 'grid-cols-2' : 'grid-cols-3'"
          >
            <div>
              <label class="input-label">1K ($)</label>
              <input
                v-model.number="editForm.image_price_1k"
                type="number"
                step="0.001"
                min="0"
                class="input"
                placeholder="0.134"
              />
            </div>
            <div>
              <label class="input-label">2K ($)</label>
              <input
                v-model.number="editForm.image_price_2k"
                type="number"
                step="0.001"
                min="0"
                class="input"
                placeholder="0.201"
              />
            </div>
            <div v-if="editForm.platform !== 'grok'">
              <label class="input-label">4K ($)</label>
              <input
                v-model.number="editForm.image_price_4k"
                type="number"
                step="0.001"
                min="0"
                class="input"
                placeholder="0.268"
              />
            </div>
          </div>
          <p class="mt-3 text-xs text-gray-500 dark:text-gray-400">
            {{ t("admin.groups.imagePricing.modeHint") }}
          </p>
          <div class="mt-2 rounded-lg bg-gray-50 p-3 text-xs text-gray-700 dark:bg-gray-800 dark:text-gray-300">
            <div class="mb-1 font-medium">
              {{ t("admin.groups.imagePricing.finalPricePreview") }}
            </div>
            <div
              class="grid gap-2"
              :class="editForm.platform === 'grok' ? 'grid-cols-2' : 'grid-cols-3'"
            >
              <div
                v-for="item in editImageFinalPricePreview"
                :key="item.label"
              >
                {{ item.label }}: {{ item.value }}
              </div>
            </div>
          </div>
        </div>

        <!-- Grok 视频生成按秒计费配置；媒体权限沿用上方分组能力开关。 -->
        <div
          v-if="editForm.platform === 'grok'"
          class="border-t pt-4"
        >
          <label class="block mb-2 font-medium text-gray-700 dark:text-gray-300">
            {{ t("admin.groups.videoPricing.title") }}
          </label>
          <p class="text-xs text-gray-500 dark:text-gray-400 mb-3">
            {{ t("admin.groups.videoPricing.description") }}
          </p>
          <div class="mb-4">
            <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
              <input
                v-model="editForm.video_rate_independent"
                type="checkbox"
                class="rounded border-gray-300 text-blue-600 focus:ring-blue-500"
              />
              {{ t("admin.groups.videoPricing.independentMultiplier") }}
            </label>
          </div>
          <div v-if="editForm.video_rate_independent" class="mb-4">
            <label class="input-label">{{
              t("admin.groups.videoPricing.videoMultiplier")
            }}</label>
            <input
              v-model.number="editForm.video_rate_multiplier"
              type="number"
              step="0.0001"
              min="0"
              class="input"
              placeholder="1"
            />
          </div>
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
            <div>
              <label class="input-label">480p ($ / s)</label>
              <input
                v-model.number="editForm.video_price_480p"
                type="number"
                step="0.001"
                min="0"
                class="input"
                :placeholder="t('admin.groups.videoPricing.useCatalogPrice')"
              />
            </div>
            <div>
              <label class="input-label">720p ($ / s)</label>
              <input
                v-model.number="editForm.video_price_720p"
                type="number"
                step="0.001"
                min="0"
                class="input"
                :placeholder="t('admin.groups.videoPricing.useCatalogPrice')"
              />
            </div>
            <div>
              <label class="input-label">1080p ($ / s)</label>
              <input
                v-model.number="editForm.video_price_1080p"
                type="number"
                step="0.001"
                min="0"
                class="input"
                :placeholder="t('admin.groups.videoPricing.useCatalogPrice')"
              />
            </div>
          </div>
          <div class="mt-4 overflow-hidden rounded-lg border border-gray-200 dark:border-gray-700">
            <div class="grid grid-cols-4 bg-gray-50 text-xs font-medium text-gray-600 dark:bg-gray-800 dark:text-gray-300">
              <div class="px-3 py-2">{{ t("admin.groups.videoPricing.modelFamily") }}</div>
              <div v-for="tier in grokVideoPriceTiers" :key="tier" class="px-3 py-2 text-center">
                {{ tier }} ($ / s)
              </div>
            </div>
            <div
              v-for="family in grokVideoPriceFamilies"
              :key="family"
              class="grid grid-cols-4 border-t border-gray-200 dark:border-gray-700"
            >
              <code class="px-3 py-2 text-xs text-gray-700 dark:text-gray-200">{{ family }}</code>
              <div v-for="tier in grokVideoPriceTiers" :key="tier" class="p-2">
                <input
                  v-model.number="editForm.video_model_prices[family][tier]"
                  type="number"
                  step="0.001"
                  min="0"
                  class="input text-center"
                  :placeholder="t('admin.groups.videoPricing.useCatalogPrice')"
                />
              </div>
            </div>
          </div>
          <p class="mt-3 text-xs text-gray-500 dark:text-gray-400">
            {{ t("admin.groups.videoPricing.modeHint") }}
          </p>
          <div class="mt-2 rounded-lg bg-gray-50 p-3 text-xs text-gray-700 dark:bg-gray-800 dark:text-gray-300">
            <div class="mb-1 font-medium">
              {{ t("admin.groups.videoPricing.finalPricePreview") }}
            </div>
            <div class="grid grid-cols-1 gap-2 sm:grid-cols-3">
              <div v-for="item in editVideoFinalPricePreview" :key="item.label">
                {{ item.label }}: {{ item.value }}
              </div>
            </div>
          </div>
        </div>

        <div v-if="editForm.platform === 'openai'" class="border-t pt-4">
          <label class="block mb-2 font-medium text-gray-700 dark:text-gray-300">
            {{ t("admin.groups.webSearchPricing.title") }}
          </label>
          <label class="input-label">
            {{ t("admin.groups.webSearchPricing.pricePerCall") }}
          </label>
          <input
            v-model.number="editForm.web_search_price_per_call"
            type="number"
            step="0.001"
            min="0"
            class="input"
            placeholder="0.01"
          />
          <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">
            {{ t("admin.groups.webSearchPricing.pricePerCallHint") }}
          </p>
          <div class="mt-2 rounded-lg bg-gray-50 p-3 text-xs text-gray-700 dark:bg-gray-800 dark:text-gray-300">
            {{
              t("admin.groups.webSearchPricing.finalPricePreview", {
                price: editWebSearchFinalPricePreview,
              })
            }}
          </div>
        </div>

        <div v-if="editForm.platform === 'grok'" class="border-t pt-4">
          <label class="block mb-2 font-medium text-gray-700 dark:text-gray-300">
            {{ t("admin.groups.voicePricing.title") }}
          </label>
          <p class="mb-3 text-xs text-gray-500 dark:text-gray-400">
            {{ t("admin.groups.voicePricing.description") }}
          </p>
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
            <div>
              <label class="input-label">{{ t("admin.groups.voicePricing.realtime") }}</label>
              <input v-model.number="editForm.audio_realtime_price_per_min" type="number" min="0" step="0.001" class="input" placeholder="0.10" />
            </div>
            <div>
              <label class="input-label">{{ t("admin.groups.voicePricing.tts") }}</label>
              <input v-model.number="editForm.audio_tts_price_per_million_chars" type="number" min="0" step="0.01" class="input" placeholder="15" />
            </div>
            <div>
              <label class="input-label">{{ t("admin.groups.voicePricing.stt") }}</label>
              <input v-model.number="editForm.audio_stt_price_per_hour" type="number" min="0" step="0.001" class="input" placeholder="0.36" />
            </div>
          </div>
        </div>

        <div v-if="editForm.platform === 'grok'" class="border-t pt-4">
          <label class="block mb-2 font-medium text-gray-700 dark:text-gray-300">
            {{ t("admin.groups.grokSearchPricing.title") }}
          </label>
          <label class="input-label">
            {{ t("admin.groups.grokSearchPricing.pricePer1k") }}
          </label>
          <input
            v-model.number="editForm.search_price_per_1k"
            type="number"
            step="0.01"
            min="0"
            class="input"
            placeholder="10"
          />
          <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">
            {{ t("admin.groups.grokSearchPricing.pricePer1kHint") }}
          </p>
          <div class="mt-2 rounded-lg bg-gray-50 p-3 text-xs text-gray-700 dark:bg-gray-800 dark:text-gray-300">
            {{
              t("admin.groups.grokSearchPricing.finalPricePreview", {
                price: editGrokSearchFinalPricePreview,
              })
            }}
          </div>
        </div>

          </section>

          <section
            v-show="editEditorSection === 'advanced'"
            id="edit-group-editor-advanced"
            class="space-y-5"
            role="tabpanel"
            aria-labelledby="edit-group-editor-advanced-tab"
          >
        <!-- 支持的模型系列（仅 antigravity 平台） -->
        <div v-if="editForm.platform === 'antigravity'" class="border-t pt-4">
          <div class="mb-1.5 flex items-center gap-1">
            <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t("admin.groups.supportedScopes.title") }}
            </label>
            <!-- Help Tooltip -->
            <div class="group relative inline-flex">
              <Icon
                name="questionCircle"
                size="sm"
                :stroke-width="2"
                class="cursor-help text-gray-400 transition-colors hover:text-primary-500 dark:text-gray-500 dark:hover:text-primary-400"
              />
              <div
                class="pointer-events-none absolute bottom-full left-0 z-50 mb-2 w-72 opacity-0 transition-all duration-200 group-hover:pointer-events-auto group-hover:opacity-100"
              >
                <div
                  class="rounded-lg bg-gray-900 p-3 text-white shadow-lg dark:bg-gray-800"
                >
                  <p class="text-xs leading-relaxed text-gray-300">
                    {{ t("admin.groups.supportedScopes.tooltip") }}
                  </p>
                  <div
                    class="absolute -bottom-1.5 left-3 h-3 w-3 rotate-45 bg-gray-900 dark:bg-gray-800"
                  ></div>
                </div>
              </div>
            </div>
          </div>
          <div class="space-y-2">
            <label class="flex items-center gap-2 cursor-pointer">
              <input
                type="checkbox"
                :checked="editForm.supported_model_scopes.includes('claude')"
                @change="toggleEditScope('claude')"
                class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-600 dark:bg-dark-700"
              />
              <span class="text-sm text-gray-700 dark:text-gray-300">{{
                t("admin.groups.supportedScopes.claude")
              }}</span>
            </label>
            <label class="flex items-center gap-2 cursor-pointer">
              <input
                type="checkbox"
                :checked="
                  editForm.supported_model_scopes.includes('gemini_text')
                "
                @change="toggleEditScope('gemini_text')"
                class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-600 dark:bg-dark-700"
              />
              <span class="text-sm text-gray-700 dark:text-gray-300">{{
                t("admin.groups.supportedScopes.geminiText")
              }}</span>
            </label>
            <label class="flex items-center gap-2 cursor-pointer">
              <input
                type="checkbox"
                :checked="
                  editForm.supported_model_scopes.includes('gemini_image')
                "
                @change="toggleEditScope('gemini_image')"
                class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-600 dark:bg-dark-700"
              />
              <span class="text-sm text-gray-700 dark:text-gray-300">{{
                t("admin.groups.supportedScopes.geminiImage")
              }}</span>
            </label>
          </div>
          <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">
            {{ t("admin.groups.supportedScopes.hint") }}
          </p>
        </div>

        <!-- MCP XML 协议注入（仅 antigravity 平台） -->
        <div v-if="editForm.platform === 'antigravity'" class="border-t pt-4">
          <div class="mb-1.5 flex items-center gap-1">
            <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t("admin.groups.mcpXml.title") }}
            </label>
            <div class="group relative inline-flex">
              <Icon
                name="questionCircle"
                size="sm"
                :stroke-width="2"
                class="cursor-help text-gray-400 transition-colors hover:text-primary-500 dark:text-gray-500 dark:hover:text-primary-400"
              />
              <div
                class="pointer-events-none absolute bottom-full left-0 z-50 mb-2 w-72 opacity-0 transition-all duration-200 group-hover:pointer-events-auto group-hover:opacity-100"
              >
                <div
                  class="rounded-lg bg-gray-900 p-3 text-white shadow-lg dark:bg-gray-800"
                >
                  <p class="text-xs leading-relaxed text-gray-300">
                    {{ t("admin.groups.mcpXml.tooltip") }}
                  </p>
                  <div
                    class="absolute -bottom-1.5 left-3 h-3 w-3 rotate-45 bg-gray-900 dark:bg-gray-800"
                  ></div>
                </div>
              </div>
            </div>
          </div>
          <div class="flex items-center gap-3">
            <button
              type="button"
              @click="editForm.mcp_xml_inject = !editForm.mcp_xml_inject"
              :class="[
                'relative inline-flex h-6 w-11 items-center rounded-full transition-colors',
                editForm.mcp_xml_inject
                  ? 'bg-primary-500'
                  : 'bg-gray-300 dark:bg-dark-600',
              ]"
            >
              <span
                :class="[
                  'inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform',
                  editForm.mcp_xml_inject ? 'translate-x-6' : 'translate-x-1',
                ]"
              />
            </button>
            <span class="text-sm text-gray-500 dark:text-gray-400">
              {{
                editForm.mcp_xml_inject
                  ? t("admin.groups.mcpXml.enabled")
                  : t("admin.groups.mcpXml.disabled")
              }}
            </span>
          </div>
        </div>

        <!-- Claude Code 客户端限制（仅 anthropic 平台） -->
        <div v-if="editForm.platform === 'anthropic'" class="border-t pt-4">
          <div class="mb-1.5 flex items-center gap-1">
            <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t("admin.groups.claudeCode.title") }}
            </label>
            <!-- Help Tooltip -->
            <div class="group relative inline-flex">
              <Icon
                name="questionCircle"
                size="sm"
                :stroke-width="2"
                class="cursor-help text-gray-400 transition-colors hover:text-primary-500 dark:text-gray-500 dark:hover:text-primary-400"
              />
              <div
                class="pointer-events-none absolute bottom-full left-0 z-50 mb-2 w-72 opacity-0 transition-all duration-200 group-hover:pointer-events-auto group-hover:opacity-100"
              >
                <div
                  class="rounded-lg bg-gray-900 p-3 text-white shadow-lg dark:bg-gray-800"
                >
                  <p class="text-xs leading-relaxed text-gray-300">
                    {{ t("admin.groups.claudeCode.tooltip") }}
                  </p>
                  <div
                    class="absolute -bottom-1.5 left-3 h-3 w-3 rotate-45 bg-gray-900 dark:bg-gray-800"
                  ></div>
                </div>
              </div>
            </div>
          </div>
          <div class="flex items-center gap-3">
            <button
              type="button"
              @click="editForm.claude_code_only = !editForm.claude_code_only"
              :class="[
                'relative inline-flex h-6 w-11 items-center rounded-full transition-colors',
                editForm.claude_code_only
                  ? 'bg-primary-500'
                  : 'bg-gray-300 dark:bg-dark-600',
              ]"
            >
              <span
                :class="[
                  'inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform',
                  editForm.claude_code_only ? 'translate-x-6' : 'translate-x-1',
                ]"
              />
            </button>
            <span class="text-sm text-gray-500 dark:text-gray-400">
              {{
                editForm.claude_code_only
                  ? t("admin.groups.claudeCode.enabled")
                  : t("admin.groups.claudeCode.disabled")
              }}
            </span>
          </div>
          <!-- 降级分组选择（仅当启用 claude_code_only 时显示） -->
          <div v-if="editForm.claude_code_only" class="mt-3">
            <label class="input-label">{{
              t("admin.groups.claudeCode.fallbackGroup")
            }}</label>
            <Select
              v-model="editForm.fallback_group_id"
              :options="fallbackGroupOptionsForEdit"
              :placeholder="t('admin.groups.claudeCode.noFallback')"
            />
            <p class="input-hint">
              {{ t("admin.groups.claudeCode.fallbackHint") }}
            </p>
          </div>
        </div>

        <!-- OpenAI Messages 调度配置（仅 openai 平台） -->
        <div
          v-if="editForm.platform === 'openai'"
          class="border-t border-gray-200 dark:border-dark-400 pt-4 mt-4"
        >
          <h4 class="text-sm font-medium text-gray-700 dark:text-gray-300 mb-3">
            {{ t("admin.groups.openaiMessages.title") }}
          </h4>

          <!-- 允许 Messages 调度开关 -->
          <div class="flex items-center justify-between">
            <label class="text-sm text-gray-600 dark:text-gray-400">{{
              t("admin.groups.openaiMessages.allowDispatch")
            }}</label>
            <button
              type="button"
              @click="
                editForm.allow_messages_dispatch =
                  !editForm.allow_messages_dispatch
              "
              class="relative inline-flex h-6 w-12 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none"
              :class="
                editForm.allow_messages_dispatch
                  ? 'bg-primary-500'
                  : 'bg-gray-300 dark:bg-dark-600'
              "
            >
              <span
                class="pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out"
                :class="
                  editForm.allow_messages_dispatch
                    ? 'translate-x-6'
                    : 'translate-x-1'
                "
              />
            </button>
          </div>
          <p class="text-xs text-gray-500 dark:text-gray-400 mt-1">
            {{ t("admin.groups.openaiMessages.allowDispatchHint") }}
          </p>

          <div v-if="editForm.allow_messages_dispatch" class="mt-3">
            <div
              class="relative overflow-hidden rounded-xl border border-gray-200 bg-white shadow-sm dark:border-dark-600 dark:bg-dark-800"
            >
              <div
                class="border-b border-gray-100 bg-gray-50/80 px-4 py-3 dark:border-dark-700 dark:bg-dark-700/50"
              >
                <div class="flex items-center gap-2">
                  <div class="h-2 w-2 rounded-full bg-blue-500"></div>
                  <label
                    class="text-sm font-medium text-gray-900 dark:text-white"
                    >{{
                      t("admin.groups.openaiMessages.familyMappingTitle")
                    }}</label
                  >
                </div>
                <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                  {{ t("admin.groups.openaiMessages.familyMappingHint") }}
                </p>
              </div>
              <div class="p-4">
                <div class="grid gap-4 md:grid-cols-3">
                  <div>
                    <label class="input-label">{{
                      t("admin.groups.openaiMessages.opusModel")
                    }}</label>
                    <input
                      v-model="editForm.opus_mapped_model"
                      type="text"
                      :placeholder="
                        t('admin.groups.openaiMessages.opusModelPlaceholder')
                      "
                      class="input"
                    />
                  </div>
                  <div>
                    <label class="input-label">{{
                      t("admin.groups.openaiMessages.sonnetModel")
                    }}</label>
                    <input
                      v-model="editForm.sonnet_mapped_model"
                      type="text"
                      :placeholder="
                        t('admin.groups.openaiMessages.sonnetModelPlaceholder')
                      "
                      class="input"
                    />
                  </div>
                  <div>
                    <label class="input-label">{{
                      t("admin.groups.openaiMessages.haikuModel")
                    }}</label>
                    <input
                      v-model="editForm.haiku_mapped_model"
                      type="text"
                      :placeholder="
                        t('admin.groups.openaiMessages.haikuModelPlaceholder')
                      "
                      class="input"
                    />
                  </div>
                </div>
              </div>
            </div>

            <div
              class="mt-5 relative overflow-hidden rounded-xl border border-primary-200 bg-white shadow-sm dark:border-primary-900/50 dark:bg-dark-800"
            >
              <div
                class="border-b border-primary-100 bg-primary-50/80 px-4 py-3 dark:border-primary-900/40 dark:bg-primary-900/20"
              >
                <div class="flex items-start justify-between gap-3">
                  <div>
                    <div class="flex items-center gap-2">
                      <div class="h-2 w-2 rounded-full bg-primary-500"></div>
                      <label
                        class="text-sm font-medium text-primary-900 dark:text-primary-100"
                        >{{
                          t("admin.groups.openaiMessages.exactMappingTitle")
                        }}</label
                      >
                    </div>
                    <p
                      class="mt-1 text-xs text-primary-600/90 dark:text-primary-400/90"
                    >
                      {{ t("admin.groups.openaiMessages.exactMappingHint") }}
                    </p>
                  </div>
                </div>
              </div>

              <div class="p-4 bg-gray-50/30 dark:bg-dark-800/30">
                <div
                  v-if="editForm.exact_model_mappings.length === 0"
                  class="flex items-center justify-between gap-3 rounded-xl border-2 border-dashed border-primary-200 bg-white px-5 py-4 text-sm text-primary-700 transition-colors hover:border-primary-300 dark:border-primary-900/40 dark:bg-dark-800 dark:text-primary-300 dark:hover:border-primary-800"
                >
                  <span>{{
                    t("admin.groups.openaiMessages.noExactMappings")
                  }}</span>
                  <button
                    type="button"
                    @click="addEditMessagesDispatchMapping"
                    class="flex items-center gap-1.5 text-sm font-medium text-primary-600 transition-colors hover:text-primary-700 dark:text-primary-400 dark:hover:text-primary-300"
                  >
                    <Icon name="plus" size="sm" />
                    {{ t("admin.groups.openaiMessages.addExactMapping") }}
                  </button>
                </div>

                <div v-else class="space-y-3">
                  <div
                    v-for="row in editForm.exact_model_mappings"
                    :key="getEditMessagesDispatchRowKey(row)"
                    class="group relative rounded-xl border border-gray-200 bg-white p-4 shadow-sm transition-all hover:border-primary-300 hover:shadow-md dark:border-dark-600 dark:bg-dark-700 dark:hover:border-primary-700"
                  >
                    <div class="flex items-center gap-4">
                      <div
                        class="grid flex-1 gap-4 md:grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] md:items-start"
                      >
                        <div>
                          <label class="input-label">{{
                            t("admin.groups.openaiMessages.claudeModel")
                          }}</label>
                          <input
                            v-model="row.claude_model"
                            type="text"
                            :placeholder="
                              t(
                                'admin.groups.openaiMessages.claudeModelPlaceholder',
                              )
                            "
                            class="input bg-gray-50 focus:bg-white dark:bg-dark-800 dark:focus:bg-dark-900"
                          />
                        </div>
                        <div
                          class="hidden md:flex md:justify-center md:pt-7 text-primary-300 dark:text-primary-700"
                        >
                          <Icon
                            name="arrowRight"
                            size="sm"
                            class="transition-transform group-hover:translate-x-1"
                          />
                        </div>
                        <div>
                          <label class="input-label">{{
                            t("admin.groups.openaiMessages.targetModel")
                          }}</label>
                          <input
                            v-model="row.target_model"
                            type="text"
                            :placeholder="
                              t(
                                'admin.groups.openaiMessages.targetModelPlaceholder',
                              )
                            "
                            class="input bg-gray-50 focus:bg-white dark:bg-dark-800 dark:focus:bg-dark-900"
                          />
                        </div>
                      </div>
                      <button
                        type="button"
                        @click="removeEditMessagesDispatchMapping(row)"
                        class="mt-6 flex h-9 w-9 items-center justify-center rounded-lg text-gray-400 transition-colors hover:bg-red-50 hover:text-red-500 dark:hover:bg-red-900/20 dark:hover:text-red-400"
                        :title="
                          t('admin.groups.openaiMessages.removeExactMapping')
                        "
                      >
                        <Icon name="trash" size="sm" />
                      </button>
                    </div>
                  </div>

                  <button
                    type="button"
                    @click="addEditMessagesDispatchMapping"
                    class="flex w-full items-center justify-center gap-2 rounded-xl border-2 border-dashed border-gray-300 bg-white py-3 text-sm font-medium text-gray-500 transition-all hover:border-primary-300 hover:bg-primary-50/50 hover:text-primary-600 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-400 dark:hover:border-primary-800 dark:hover:bg-primary-900/20 dark:hover:text-primary-400"
                  >
                    <Icon name="plus" size="sm" />
                    {{ t("admin.groups.openaiMessages.addExactMapping") }}
                  </button>
                </div>
              </div>
            </div>
          </div>
        </div>

        <!-- 账号过滤控制 (OpenAI/Antigravity/Anthropic/Gemini) -->
        <div
          v-if="
            ['openai', 'antigravity', 'anthropic', 'gemini'].includes(
              editForm.platform,
            )
          "
          class="border-t border-gray-200 dark:border-dark-400 pt-4 mt-4 space-y-4"
        >
          <h4 class="text-sm font-medium text-gray-700 dark:text-gray-300 mb-3">
            账号过滤控制
          </h4>

          <!-- require_oauth_only toggle -->
          <div class="flex items-center justify-between">
            <div>
              <label class="text-sm text-gray-600 dark:text-gray-400"
                >仅允许 OAuth 账号</label
              >
              <p class="text-xs text-gray-500 dark:text-gray-400 mt-0.5">
                {{
                  editForm.require_oauth_only
                    ? "已启用 — 排除 API Key 类型账号"
                    : "未启用"
                }}
              </p>
            </div>
            <button
              type="button"
              @click="
                editForm.require_oauth_only = !editForm.require_oauth_only
              "
              class="relative inline-flex h-6 w-12 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none"
              :class="
                editForm.require_oauth_only
                  ? 'bg-primary-500'
                  : 'bg-gray-300 dark:bg-dark-600'
              "
            >
              <span
                class="pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out"
                :class="
                  editForm.require_oauth_only
                    ? 'translate-x-6'
                    : 'translate-x-1'
                "
              />
            </button>
          </div>

          <!-- require_privacy_set toggle -->
          <div class="flex items-center justify-between">
            <div>
              <label class="text-sm text-gray-600 dark:text-gray-400"
                >仅允许隐私保护已设置的账号</label
              >
              <p class="text-xs text-gray-500 dark:text-gray-400 mt-0.5">
                {{
                  editForm.require_privacy_set
                    ? "已启用 — Privacy 未设置的账号将被排除"
                    : "未启用"
                }}
              </p>
            </div>
            <button
              type="button"
              @click="
                editForm.require_privacy_set = !editForm.require_privacy_set
              "
              class="relative inline-flex h-6 w-12 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none"
              :class="
                editForm.require_privacy_set
                  ? 'bg-primary-500'
                  : 'bg-gray-300 dark:bg-dark-600'
              "
            >
              <span
                class="pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out"
                :class="
                  editForm.require_privacy_set
                    ? 'translate-x-6'
                    : 'translate-x-1'
                "
              />
            </button>
          </div>
        </div>

        <!-- 无效请求兜底（仅 anthropic/antigravity 平台，且非订阅分组） -->
        <div
          v-if="
            ['anthropic', 'antigravity'].includes(editForm.platform) &&
            editForm.subscription_type !== 'subscription'
          "
          class="border-t pt-4"
        >
          <label class="input-label">{{
            t("admin.groups.invalidRequestFallback.title")
          }}</label>
          <Select
            v-model="editForm.fallback_group_id_on_invalid_request"
            :options="invalidRequestFallbackOptionsForEdit"
            :placeholder="t('admin.groups.invalidRequestFallback.noFallback')"
          />
          <p class="input-hint">
            {{ t("admin.groups.invalidRequestFallback.hint") }}
          </p>
        </div>

        <!-- 模型路由配置（仅 anthropic 平台） -->
        <div v-if="editForm.platform === 'anthropic'" class="border-t pt-4">
          <div class="mb-1.5 flex items-center gap-1">
            <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t("admin.groups.modelRouting.title") }}
            </label>
            <!-- Help Tooltip -->
            <div class="group relative inline-flex">
              <Icon
                name="questionCircle"
                size="sm"
                :stroke-width="2"
                class="cursor-help text-gray-400 transition-colors hover:text-primary-500 dark:text-gray-500 dark:hover:text-primary-400"
              />
              <div
                class="pointer-events-none absolute bottom-full left-0 z-50 mb-2 w-80 opacity-0 transition-all duration-200 group-hover:pointer-events-auto group-hover:opacity-100"
              >
                <div
                  class="rounded-lg bg-gray-900 p-3 text-white shadow-lg dark:bg-gray-800"
                >
                  <p class="text-xs leading-relaxed text-gray-300">
                    {{ t("admin.groups.modelRouting.tooltip") }}
                  </p>
                  <div
                    class="absolute -bottom-1.5 left-3 h-3 w-3 rotate-45 bg-gray-900 dark:bg-gray-800"
                  ></div>
                </div>
              </div>
            </div>
          </div>
          <!-- 启用开关 -->
          <div class="flex items-center gap-3 mb-3">
            <button
              type="button"
              @click="
                editForm.model_routing_enabled = !editForm.model_routing_enabled
              "
              :class="[
                'relative inline-flex h-6 w-11 items-center rounded-full transition-colors',
                editForm.model_routing_enabled
                  ? 'bg-primary-500'
                  : 'bg-gray-300 dark:bg-dark-600',
              ]"
            >
              <span
                :class="[
                  'inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform',
                  editForm.model_routing_enabled
                    ? 'translate-x-6'
                    : 'translate-x-1',
                ]"
              />
            </button>
            <span class="text-sm text-gray-500 dark:text-gray-400">
              {{
                editForm.model_routing_enabled
                  ? t("admin.groups.modelRouting.enabled")
                  : t("admin.groups.modelRouting.disabled")
              }}
            </span>
          </div>
          <p
            v-if="!editForm.model_routing_enabled"
            class="text-xs text-gray-500 dark:text-gray-400 mb-3"
          >
            {{ t("admin.groups.modelRouting.disabledHint") }}
          </p>
          <p v-else class="text-xs text-gray-500 dark:text-gray-400 mb-3">
            {{ t("admin.groups.modelRouting.noRulesHint") }}
          </p>
          <!-- 路由规则列表（仅在启用时显示） -->
          <div v-if="editForm.model_routing_enabled" class="space-y-3">
            <div
              v-for="rule in editModelRoutingRules"
              :key="getEditRuleRenderKey(rule)"
              class="rounded-lg border border-gray-200 p-3 dark:border-dark-600"
            >
              <div class="flex items-start gap-3">
                <div class="flex-1 space-y-2">
                  <div>
                    <label class="input-label text-xs">{{
                      t("admin.groups.modelRouting.modelPattern")
                    }}</label>
                    <input
                      v-model="rule.pattern"
                      type="text"
                      class="input text-sm"
                      :placeholder="
                        t('admin.groups.modelRouting.modelPatternPlaceholder')
                      "
                    />
                  </div>
                  <div>
                    <label class="input-label text-xs">{{
                      t("admin.groups.modelRouting.accounts")
                    }}</label>
                    <!-- 已选账号标签 -->
                    <div
                      v-if="rule.accounts.length > 0"
                      class="flex flex-wrap gap-1.5 mb-2"
                    >
                      <span
                        v-for="account in rule.accounts"
                        :key="account.id"
                        class="inline-flex items-center gap-1 rounded-full bg-primary-100 px-2.5 py-1 text-xs font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
                      >
                        {{ account.name }}
                        <button
                          type="button"
                          @click="removeSelectedAccount(rule, account.id, true)"
                          class="ml-0.5 text-primary-500 hover:text-primary-700 dark:hover:text-primary-200"
                        >
                          <Icon name="x" size="xs" />
                        </button>
                      </span>
                    </div>
                    <!-- 账号搜索输入框 -->
                    <div class="relative account-search-container">
                      <input
                        v-model="
                          accountSearchKeyword[getEditRuleSearchKey(rule)]
                        "
                        type="text"
                        class="input text-sm"
                        :placeholder="
                          t(
                            'admin.groups.modelRouting.searchAccountPlaceholder',
                          )
                        "
                        @input="searchAccountsByRule(rule, true)"
                        @focus="onAccountSearchFocus(rule, true)"
                      />
                      <!-- 搜索结果下拉框 -->
                      <div
                        v-if="
                          showAccountDropdown[getEditRuleSearchKey(rule)] &&
                          accountSearchResults[getEditRuleSearchKey(rule)]
                            ?.length > 0
                        "
                        class="absolute z-50 mt-1 max-h-48 w-full overflow-auto rounded-lg border bg-white shadow-lg dark:border-dark-600 dark:bg-dark-800"
                      >
                        <button
                          v-for="account in accountSearchResults[
                            getEditRuleSearchKey(rule)
                          ]"
                          :key="account.id"
                          type="button"
                          @click="selectAccount(rule, account, true)"
                          class="w-full px-3 py-2 text-left text-sm hover:bg-gray-100 dark:hover:bg-dark-700"
                          :class="{
                            'opacity-50': rule.accounts.some(
                              (a) => a.id === account.id,
                            ),
                          }"
                          :disabled="
                            rule.accounts.some((a) => a.id === account.id)
                          "
                        >
                          <span>{{ account.name }}</span>
                          <span class="ml-2 text-xs text-gray-400"
                            >#{{ account.id }}</span
                          >
                        </button>
                      </div>
                    </div>
                    <p class="text-xs text-gray-400 mt-1">
                      {{ t("admin.groups.modelRouting.accountsHint") }}
                    </p>
                  </div>
                </div>
                <button
                  type="button"
                  @click="removeEditRoutingRule(rule)"
                  class="mt-5 p-1.5 text-gray-400 hover:text-red-500 transition-colors"
                  :title="t('admin.groups.modelRouting.removeRule')"
                >
                  <Icon name="trash" size="sm" />
                </button>
              </div>
            </div>
          </div>
          <!-- 添加规则按钮（仅在启用时显示） -->
          <button
            v-if="editForm.model_routing_enabled"
            type="button"
            @click="addEditRoutingRule"
            class="mt-3 flex items-center gap-1.5 text-sm text-primary-600 hover:text-primary-700 dark:text-primary-400 dark:hover:text-primary-300"
          >
            <Icon name="plus" size="sm" />
            {{ t("admin.groups.modelRouting.addRule") }}
          </button>
        </div>
          </section>
        </div>
      </form>

      <template #footer>
        <div class="flex justify-end gap-3 pt-4">
          <button
            @click="closeEditModal"
            type="button"
            class="btn btn-secondary"
          >
            {{ t("common.cancel") }}
          </button>
          <button
            type="submit"
            form="edit-group-form"
            :disabled="submitting || !editPricingReady"
            class="btn btn-primary"
            data-tour="group-form-submit"
          >
            <svg
              v-if="submitting"
              class="-ml-1 mr-2 h-4 w-4 animate-spin"
              fill="none"
              viewBox="0 0 24 24"
            >
              <circle
                class="opacity-25"
                cx="12"
                cy="12"
                r="10"
                stroke="currentColor"
                stroke-width="4"
              ></circle>
              <path
                class="opacity-75"
                fill="currentColor"
                d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
              ></path>
            </svg>
            {{ submitting ? t("admin.groups.updating") : t("common.update") }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <!-- Delete Confirmation Dialog -->
    <ConfirmDialog
      :show="showDeleteDialog"
      :title="t('admin.groups.deleteGroup')"
      :message="deleteConfirmMessage"
      :confirm-text="t('common.delete')"
      :cancel-text="t('common.cancel')"
      :danger="true"
      @confirm="confirmDelete"
      @cancel="showDeleteDialog = false"
    />

    <!-- Duplicate Confirmation Dialog -->
    <ConfirmDialog
      :show="showDuplicateDialog"
      :title="t('admin.groups.duplicateConfirmTitle')"
      :message="duplicateConfirmMessage"
      :confirm-text="t('admin.groups.confirmDuplicate')"
      :cancel-text="t('common.cancel')"
      @confirm="confirmDuplicate"
      @cancel="closeDuplicateDialog"
    />

    <!-- Sort Order Modal -->
    <BaseDialog
      :show="showSortModal"
      :title="t('admin.groups.sortOrder')"
      width="normal"
      @close="closeSortModal"
    >
      <div class="space-y-4">
        <p class="text-sm text-gray-500 dark:text-gray-400">
          {{ t("admin.groups.sortOrderHint") }}
        </p>
        <VueDraggable
          v-model="sortableGroups"
          :animation="200"
          class="space-y-2"
        >
          <div
            v-for="group in sortableGroups"
            :key="group.id"
            class="flex cursor-grab items-center gap-3 rounded-lg border border-gray-200 bg-white p-3 transition-shadow hover:shadow-md active:cursor-grabbing dark:border-dark-600 dark:bg-dark-700"
          >
            <div class="text-gray-400">
              <Icon name="menu" size="md" />
            </div>
            <div class="flex-1">
              <div class="font-medium text-gray-900 dark:text-white">
                {{ group.name }}
              </div>
              <div class="text-xs text-gray-500 dark:text-gray-400">
                <span
                  :class="[
                    'inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium',
                    group.platform === 'anthropic'
                      ? 'bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-400'
                      : group.platform === 'openai'
                        ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400'
                        : group.platform === 'antigravity'
                          ? 'bg-purple-100 text-purple-700 dark:bg-purple-900/30 dark:text-purple-400'
                          : group.platform === 'grok'
                            ? 'bg-zinc-200 text-zinc-800 dark:bg-zinc-700 dark:text-zinc-100'
                            : 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400',
                  ]"
                >
                  {{ t("admin.groups.platforms." + group.platform) }}
                </span>
              </div>
            </div>
            <div class="text-sm text-gray-400">#{{ group.id }}</div>
          </div>
        </VueDraggable>
      </div>

      <template #footer>
        <div class="flex justify-end gap-3 pt-4">
          <button
            @click="closeSortModal"
            type="button"
            class="btn btn-secondary"
          >
            {{ t("common.cancel") }}
          </button>
          <button
            @click="saveSortOrder"
            :disabled="sortSubmitting"
            class="btn btn-primary"
          >
            <svg
              v-if="sortSubmitting"
              class="-ml-1 mr-2 h-4 w-4 animate-spin"
              fill="none"
              viewBox="0 0 24 24"
            >
              <circle
                class="opacity-25"
                cx="12"
                cy="12"
                r="10"
                stroke="currentColor"
                stroke-width="4"
              ></circle>
              <path
                class="opacity-75"
                fill="currentColor"
                d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
              ></path>
            </svg>
            {{ sortSubmitting ? t("common.saving") : t("common.save") }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <CompositeRoutesModal
      :show="showCompositeRoutesModal"
      :group="compositeRoutesGroup"
      @close="showCompositeRoutesModal = false"
      @changed="loadGroups"
    />

    <!-- Group Rate Multipliers Modal -->
    <GroupRateMultipliersModal
      :show="showRateMultipliersModal"
      :group="rateMultipliersGroup"
      @close="showRateMultipliersModal = false"
      @success="loadGroups"
    />

    <!-- Group RPM Overrides Modal -->
    <GroupRPMOverridesModal
      :show="showRPMOverridesModal"
      :group="rpmOverridesGroup"
      @close="showRPMOverridesModal = false"
      @success="loadGroups"
    />

    <QuickBindAccountsModal
      :show="showBindAccountsModal"
      :group="bindAccountsGroup"
      @close="showBindAccountsModal = false"
      @success="loadGroups"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onUnmounted, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useAppStore } from "@/stores/app";
import { adminAPI } from "@/api/admin";
import type { AdminGroup, BillingModelSource, GroupFailoverStrategy, GroupModelRouteHealthView, GroupPlatform, GroupSchedulingMode, SubscriptionType } from "@/types";
import { GROUP_PLATFORM_OPTIONS } from "@/constants/platforms";
import type { Column } from "@/components/common/types";
import AppLayout from "@/components/layout/GatewayLiteLayout.vue";
import TablePageLayout from "@/components/layout/TablePageLayout.vue";
import DataTable from "@/components/common/DataTable.vue";
import Pagination from "@/components/common/Pagination.vue";
import BaseDialog from "@/components/common/BaseDialog.vue";
import ConfirmDialog from "@/components/common/ConfirmDialog.vue";
import EmptyState from "@/components/common/EmptyState.vue";
import Select from "@/components/common/Select.vue";
import PlatformIcon from "@/components/common/PlatformIcon.vue";
import Icon from "@/components/icons/Icon.vue";
import GroupRateMultipliersModal from "@/components/admin/group/GroupRateMultipliersModal.vue";
import GroupRPMOverridesModal from "@/components/admin/group/GroupRPMOverridesModal.vue";
import QuickBindAccountsModal from "@/components/admin/group/QuickBindAccountsModal.vue";
import CompositeRoutesModal from "@/components/admin/group/CompositeRoutesModal.vue";
import GroupOpenAILongContextBillingField from "@/components/admin/group/GroupOpenAILongContextBillingField.vue";
import GroupPeakRatePolicyFields from "@/components/admin/group/GroupPeakRatePolicyFields.vue";
import GroupDisplayIdentityFields from "@/components/admin/group/GroupDisplayIdentityFields.vue";
import GroupModelMappingFields from "@/components/admin/group/GroupModelMappingFields.vue";
import GroupBillingModelSourceFields from "@/components/admin/group/GroupBillingModelSourceFields.vue";
import GroupPricingFields from "@/components/admin/group/GroupPricingFields.vue";
import GroupSchedulingModeFields from "@/components/admin/group/GroupSchedulingModeFields.vue";
import GroupPerUserConcurrencyFields from "@/components/admin/group/GroupPerUserConcurrencyFields.vue";
import GroupResponseModelFields from "@/components/admin/group/GroupResponseModelFields.vue";
import GroupEditorSectionNav, {
  type GroupEditorSection,
} from "@/components/admin/group/GroupEditorSectionNav.vue";
import ReasoningEffortPolicyFields from "@/components/admin/group/ReasoningEffortPolicyFields.vue";
import GroupCapacityBadge from "@/components/common/GroupCapacityBadge.vue";
import { DEFAULT_TIME_ZONE } from "@/utils/timezone";
import { normalizeGroupIngressRequests } from "@/utils/groupCapacity";
import { VueDraggable } from "vue-draggable-plus";
import { createStableObjectKeyResolver } from "@/utils/stableObjectKey";
import { extractApiErrorMessage } from "@/utils/apiError";
import { useKeyedDebouncedSearch } from "@/composables/useKeyedDebouncedSearch";
import { getPersistedPageSize } from "@/composables/usePersistedPageSize";
import { getGatewayRuntimeSummary } from "@/api/admin/runtimeSummary";
import {
  createDefaultMessagesDispatchFormState,
  messagesDispatchConfigToFormState,
  messagesDispatchFormStateToConfig,
  resetMessagesDispatchFormState,
  type MessagesDispatchMappingRow,
} from "./groupsMessagesDispatch";
import {
  buildModelsListConfig,
  createModelsListState as createInitialModelsListState,
  invertModelsListSelection,
  mergeModelsListCandidates,
  moveModelsListItem,
  selectAllModelsListItems,
  setModelsListCandidates,
} from "./groupsModelsList";
import { createModelsListCandidatesTracker } from "./groupsModelsListCandidates";
import { normalizeSupportedModelScopesForPlatform } from "./groupsSupportedModelScopes";
import { summarizeOpenAILongContextBillingAccounts } from "./groupsOpenAILongContextBilling";
import {
  groupModelMappingRowsToRecord,
  groupModelMappingToRows,
  groupWeightedMappingRowsToRecord,
  validateGroupModelMappingRows,
  type GroupModelMappingRow,
} from "./groupsModelMapping";
import {
  normalizeReasoningEffortForPlatform,
  reasoningEffortMappingsToAPI,
  reasoningEffortMappingsToRows,
  type ReasoningEffortMappingRow,
} from "./groupsReasoningEffort";
import {
  createGroupPricingFormState,
  groupPricingConfigToForm,
  groupPricingFormToRequest,
  validateGroupPricingForm,
  type GroupPricingFormState,
} from "@/components/admin/group/groupPricing";

const { t } = useI18n();
const appStore = useAppStore();
const compositeGroupsEnabled = ref(false);
const supportsLivePlatform = (platform: string): boolean =>
  platform === "openai" || platform === "composite";

type GroupEditorSectionID = "basics" | "models" | "media" | "advanced";

const groupEditorSections = computed<GroupEditorSection[]>(() => [
  {
    id: "basics",
    label: t("admin.groups.editorNavigation.basics"),
    description: t("admin.groups.editorNavigation.basicsHint"),
    icon: "cog",
    activeTone: "bg-primary-100 text-primary-700 dark:bg-primary-900/40 dark:text-primary-300",
    idleTone: "bg-gray-100 text-gray-500 dark:bg-dark-600 dark:text-gray-400",
  },
  {
    id: "models",
    label: t("admin.groups.editorNavigation.models"),
    description: t("admin.groups.editorNavigation.modelsHint"),
    icon: "swap",
    activeTone: "bg-violet-100 text-violet-700 dark:bg-violet-900/35 dark:text-violet-300",
    idleTone: "bg-gray-100 text-gray-500 dark:bg-dark-600 dark:text-gray-400",
  },
  {
    id: "media",
    label: t("admin.groups.editorNavigation.media"),
    description: t("admin.groups.editorNavigation.mediaHint"),
    icon: "calculator",
    activeTone: "bg-amber-100 text-amber-700 dark:bg-amber-900/35 dark:text-amber-300",
    idleTone: "bg-gray-100 text-gray-500 dark:bg-dark-600 dark:text-gray-400",
  },
  {
    id: "advanced",
    label: t("admin.groups.editorNavigation.advanced"),
    description: t("admin.groups.editorNavigation.advancedHint"),
    icon: "shield",
    activeTone: "bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-200",
    idleTone: "bg-gray-100 text-gray-500 dark:bg-dark-600 dark:text-gray-400",
  },
]);

const createEditorSection = ref<GroupEditorSectionID>("basics");
const editEditorSection = ref<GroupEditorSectionID>("basics");

const columns = computed<Column[]>(() => [
  { key: "name", label: t("admin.groups.columns.name"), sortable: true },
  { key: "id", label: t("admin.groups.columns.id"), sortable: true },
  {
    key: "platform",
    label: t("admin.groups.columns.platform"),
    sortable: true,
  },
  {
    key: "billing_type",
    label: t("admin.groups.columns.billingType"),
    sortable: true,
  },
  {
    key: "rate_multiplier",
    label: t("admin.groups.columns.rateMultiplier"),
    sortable: true,
  },
  {
    key: "is_exclusive",
    label: t("admin.groups.columns.type"),
    sortable: true,
  },
  {
    key: "account_count",
    label: t("admin.groups.columns.accounts"),
    sortable: true,
  },
  {
    key: "capacity",
    label: t("admin.groups.columns.capacity"),
    sortable: false,
  },
  {
    key: "ingress_requests",
    label: t("admin.groups.columns.ingressRequests"),
    sortable: false,
  },
  { key: "usage", label: t("admin.groups.columns.usage"), sortable: false },
  { key: "status", label: t("admin.groups.columns.status"), sortable: true },
  { key: "actions", label: t("admin.groups.columns.actions"), sortable: false },
]);

const HIDDEN_COLUMNS_KEY = "groups-hidden-columns";
const DEFAULT_HIDDEN_COLUMNS = ["id"];
const COLUMN_SETTINGS_VERSION_KEY = "groups-column-settings-version";
const COLUMN_SETTINGS_VERSION = 3;
const VERSION_NEW_HIDDEN_COLUMNS: Record<number, string[]> = {
  2: ["id"],
};
const VERSION_NEW_VISIBLE_COLUMNS: Record<number, string[]> = {
  // 用量列现在包含每个分组实际接收的请求数，升级后默认展示。
  3: ["usage"],
};
const REQUIRED_COLUMN_KEYS = new Set(["name", "actions"]);
const hiddenColumns = reactive<Set<string>>(new Set());
const showColumnSettingsDropdown = ref(false);
const columnSettingsDropdownRef = ref<HTMLElement | null>(null);

const toggleableColumns = computed(() =>
  columns.value.filter((col) => !REQUIRED_COLUMN_KEYS.has(col.key)),
);

const visibleColumns = computed(() =>
  columns.value.filter(
    (col) => REQUIRED_COLUMN_KEYS.has(col.key) || !hiddenColumns.has(col.key),
  ),
);

const getValidHiddenColumnKeys = () =>
  new Set(toggleableColumns.value.map((column) => column.key));

const loadSavedColumns = () => {
  hiddenColumns.clear();
  try {
    const saved = localStorage.getItem(HIDDEN_COLUMNS_KEY);
    const validKeys = getValidHiddenColumnKeys();

    if (saved) {
      const parsed = JSON.parse(saved);
      if (Array.isArray(parsed)) {
        parsed.forEach((key) => {
          if (typeof key === "string" && validKeys.has(key)) {
            hiddenColumns.add(key);
          }
        });
      }

      const parsedVersion = Number(
        localStorage.getItem(COLUMN_SETTINGS_VERSION_KEY) ?? "1",
      );
      const storedVersion = Number.isFinite(parsedVersion) ? parsedVersion : 1;
      if (storedVersion < COLUMN_SETTINGS_VERSION) {
        for (let version = storedVersion + 1; version <= COLUMN_SETTINGS_VERSION; version++) {
          for (const key of VERSION_NEW_HIDDEN_COLUMNS[version] ?? []) {
            if (validKeys.has(key)) hiddenColumns.add(key);
          }
          for (const key of VERSION_NEW_VISIBLE_COLUMNS[version] ?? []) {
            hiddenColumns.delete(key);
          }
        }
        saveColumnsToStorage();
      }
    } else {
      DEFAULT_HIDDEN_COLUMNS.forEach((key) => {
        if (validKeys.has(key)) hiddenColumns.add(key);
      });
      saveColumnsToStorage();
    }
  } catch (error) {
    console.error("Failed to load group table columns:", error);
    DEFAULT_HIDDEN_COLUMNS.forEach((key) => hiddenColumns.add(key));
  }
};

const saveColumnsToStorage = () => {
  try {
    const validKeys = getValidHiddenColumnKeys();
    const keys = [...hiddenColumns].filter((key) => validKeys.has(key));
    localStorage.setItem(HIDDEN_COLUMNS_KEY, JSON.stringify(keys));
    localStorage.setItem(
      COLUMN_SETTINGS_VERSION_KEY,
      String(COLUMN_SETTINGS_VERSION),
    );
  } catch (error) {
    console.error("Failed to save group table columns:", error);
  }
};

const toggleColumn = (key: string) => {
  if (REQUIRED_COLUMN_KEYS.has(key)) return;
  if (hiddenColumns.has(key)) {
    hiddenColumns.delete(key);
  } else {
    hiddenColumns.add(key);
  }
  saveColumnsToStorage();
};

const isColumnVisible = (key: string) =>
  REQUIRED_COLUMN_KEYS.has(key) || !hiddenColumns.has(key);

// Filter options
const statusOptions = computed(() => [
  { value: "", label: t("admin.groups.allStatus") },
  { value: "active", label: t("admin.accounts.status.active") },
  { value: "inactive", label: t("admin.accounts.status.inactive") },
]);

const exclusiveOptions = computed(() => [
  { value: "", label: t("admin.groups.allGroups") },
  { value: "true", label: t("admin.groups.exclusive") },
  { value: "false", label: t("admin.groups.nonExclusive") },
]);

const platformOptions = computed(() => GROUP_PLATFORM_OPTIONS.map((option) =>
  option.value === "composite"
    ? {
        ...option,
        label: compositeGroupsEnabled.value
          ? option.label
          : `${option.label} · 总开关未启用`,
        disabled: !compositeGroupsEnabled.value,
      }
    : option,
));

const platformFilterOptions = computed(() => [
  { value: "", label: t("admin.groups.allPlatforms") },
  ...GROUP_PLATFORM_OPTIONS,
]);

const editStatusOptions = computed(() => [
  { value: "active", label: t("admin.accounts.status.active") },
  { value: "inactive", label: t("admin.accounts.status.inactive") },
]);

const subscriptionTypeOptions = computed(() => [
  { value: "standard", label: t("admin.groups.subscription.standard") },
  { value: "subscription", label: t("admin.groups.subscription.subscription") },
]);

// 降级分组选项（创建时）- 仅包含 anthropic 平台且未启用 claude_code_only 的分组
const fallbackGroupOptions = computed(() => {
  const options: { value: number | null; label: string }[] = [
    { value: null, label: t("admin.groups.claudeCode.noFallback") },
  ];
  const eligibleGroups = groups.value.filter(
    (g) =>
      g.platform === "anthropic" &&
      !g.claude_code_only &&
      g.status === "active",
  );
  eligibleGroups.forEach((g) => {
    options.push({ value: g.id, label: g.name });
  });
  return options;
});

// 降级分组选项（编辑时）- 排除自身
const fallbackGroupOptionsForEdit = computed(() => {
  const options: { value: number | null; label: string }[] = [
    { value: null, label: t("admin.groups.claudeCode.noFallback") },
  ];
  const currentId = editingGroup.value?.id;
  const eligibleGroups = groups.value.filter(
    (g) =>
      g.platform === "anthropic" &&
      !g.claude_code_only &&
      g.status === "active" &&
      g.id !== currentId,
  );
  eligibleGroups.forEach((g) => {
    options.push({ value: g.id, label: g.name });
  });
  return options;
});

// 无效请求兜底分组选项（创建时）- 仅包含 anthropic 平台、非订阅且未配置兜底的分组
const invalidRequestFallbackOptions = computed(() => {
  const options: { value: number | null; label: string }[] = [
    { value: null, label: t("admin.groups.invalidRequestFallback.noFallback") },
  ];
  const eligibleGroups = groups.value.filter(
    (g) =>
      g.platform === "anthropic" &&
      g.status === "active" &&
      g.subscription_type !== "subscription" &&
      g.fallback_group_id_on_invalid_request === null,
  );
  eligibleGroups.forEach((g) => {
    options.push({ value: g.id, label: g.name });
  });
  return options;
});

// 无效请求兜底分组选项（编辑时）- 排除自身
const invalidRequestFallbackOptionsForEdit = computed(() => {
  const options: { value: number | null; label: string }[] = [
    { value: null, label: t("admin.groups.invalidRequestFallback.noFallback") },
  ];
  const currentId = editingGroup.value?.id;
  const eligibleGroups = groups.value.filter(
    (g) =>
      g.platform === "anthropic" &&
      g.status === "active" &&
      g.subscription_type !== "subscription" &&
      g.fallback_group_id_on_invalid_request === null &&
      g.id !== currentId,
  );
  eligibleGroups.forEach((g) => {
    options.push({ value: g.id, label: g.name });
  });
  return options;
});

const canCopyAccountsFromGroup = (target: GroupPlatform, source: GroupPlatform) =>
  target === "composite" ? true : target === source;

// 聚合分组可点击勾选不同平台的来源分组；普通分组仍只显示同平台。
const copyAccountsGroupOptions = computed(() => {
  const eligibleGroups = groups.value.filter(
    (g) => canCopyAccountsFromGroup(createForm.platform, g.platform) && (g.account_count || 0) > 0,
  );
  return eligibleGroups.map((g) => ({
    value: g.id,
    label: `${g.name} (${g.account_count || 0} 个账号)`,
  }));
});

// 编辑时排除自身。
const copyAccountsGroupOptionsForEdit = computed(() => {
  const currentId = editingGroup.value?.id;
  const eligibleGroups = groups.value.filter(
    (g) =>
      canCopyAccountsFromGroup(editForm.platform, g.platform) &&
      (g.account_count || 0) > 0 &&
      g.id !== currentId,
  );
  return eligibleGroups.map((g) => ({
    value: g.id,
    label: `${g.name} (${g.account_count || 0} 个账号)`,
  }));
});

const groups = ref<AdminGroup[]>([]);
const failoverGroups = ref<AdminGroup[]>([]);
const failoverGroupOptions = computed(() =>
  failoverGroups.value.filter(
    (group) =>
      group.platform === "openai" &&
      group.status === "active" &&
      group.id !== editingGroup.value?.id,
  ),
);
const loading = ref(false);
const usageMap = ref<
  Map<
    number,
    {
      today_cost: number;
      total_cost: number;
      today_requests: number;
      total_requests: number;
      today_rerouted_requests: number;
      total_rerouted_requests: number;
    }
  >
>(new Map());
const usageLoading = ref(false);
const capacityLoaded = ref(false);
const capacityLastUpdated = ref<Date | null>(null);
const capacityMap = ref<
  Map<
    number,
    {
      concurrencyUsed: number;
      concurrencyMax: number;
      sessionsUsed: number;
      sessionsMax: number;
      rpmUsed: number;
      rpmMax: number;
      ingressRequestsUsed: number | null;
    }
  >
>(new Map());
const searchQuery = ref("");
const filters = reactive({
  platform: "",
  status: "",
  is_exclusive: "",
});
const pagination = reactive({
  page: 1,
  page_size: getPersistedPageSize(),
  total: 0,
  pages: 0,
});
const sortState = reactive({
  sort_by: "sort_order",
  sort_order: "asc" as "asc" | "desc",
});

let abortController: AbortController | null = null;
let capacityRefreshTimer: ReturnType<typeof setInterval> | null = null;

const showCreateModal = ref(false);
const showEditModal = ref(false);
const showDeleteDialog = ref(false);
const showDuplicateDialog = ref(false);
const showSortModal = ref(false);
const submitting = ref(false);
const createPricingForm = ref<GroupPricingFormState>(createGroupPricingFormState());
const editPricingForm = ref<GroupPricingFormState>(createGroupPricingFormState());
const editPricingLoading = ref(false);
const editPricingShared = ref(false);
const editPricingReady = ref(false);
const editPricingLoadError = ref(false);
let editSessionSerial = 0;
let editPricingLoadSerial = 0;
const sortSubmitting = ref(false);
const editingGroup = ref<AdminGroup | null>(null);
// 智能分流映射的实时健康状态，仅在编辑 openai 分组弹窗内展示与刷新。
const editRouteHealth = ref<GroupModelRouteHealthView[]>([]);
const deletingGroup = ref<AdminGroup | null>(null);
const duplicatingGroup = ref<AdminGroup | null>(null);
const duplicatingGroupIds = reactive(new Set<number>());
const showRateMultipliersModal = ref(false);
const rateMultipliersGroup = ref<AdminGroup | null>(null);
const showRPMOverridesModal = ref(false);
const rpmOverridesGroup = ref<AdminGroup | null>(null);
const showBindAccountsModal = ref(false);
const bindAccountsGroup = ref<AdminGroup | null>(null);
const showCompositeRoutesModal = ref(false);
const compositeRoutesGroup = ref<AdminGroup | null>(null);
const sortableGroups = ref<AdminGroup[]>([]);
const duplicateConfirmMessage = computed(() => {
  const group = duplicatingGroup.value;
  if (!group) return "";
  return t("admin.groups.duplicateConfirmMessage", {
    name: group.name,
    platform: group.platform,
    count: group.account_count ?? 0,
  });
});
const createMessagesDispatchDefaults = createDefaultMessagesDispatchFormState();
const editMessagesDispatchDefaults = createDefaultMessagesDispatchFormState();
const createModelsListState = reactive(createInitialModelsListState());
const editModelsListState = reactive(createInitialModelsListState());
const createModelsListLoading = ref(false);
const editModelsListLoading = ref(false);
type ReasoningEffortPolicyFieldsExpose = {
  validate: () => boolean;
  resetValidation: () => void;
};
const createReasoningEffortPolicyRef =
  ref<ReasoningEffortPolicyFieldsExpose | null>(null);
const editReasoningEffortPolicyRef =
  ref<ReasoningEffortPolicyFieldsExpose | null>(null);
const modelsListCandidatesTracker = createModelsListCandidatesTracker();
const createModelsListSelectedCount = computed(
  () => createModelsListState.items.filter((item) => item.selected).length,
);
const editModelsListSelectedCount = computed(
  () => editModelsListState.items.filter((item) => item.selected).length,
);
const createModelsListCandidates = computed(() => createModelsListState.items.map((item) => item.id));
const editModelsListCandidates = computed(() => editModelsListState.items.map((item) => item.id));

const exactGroupMappingSources = (rows: GroupModelMappingRow[]) => rows
  .map((row) => row.from.trim())
  .filter((model) => model.length > 0 && !model.includes("*"));

watch(
  () => createForm.model_mapping_rows,
  (rows) => mergeModelsListCandidates(createModelsListState, exactGroupMappingSources(rows)),
  { deep: true },
);

watch(
  () => editForm.model_mapping_rows,
  (rows) => mergeModelsListCandidates(editModelsListState, exactGroupMappingSources(rows)),
  { deep: true },
);

const grokVideoPriceFamilies = ["grok-imagine-video", "grok-imagine-video-1.5"] as const;
const grokVideoPriceTiers = ["480p", "720p", "1080p"] as const;
type GrokVideoPriceMatrix = Record<string, Record<string, number | string | null>>;

const emptyGrokVideoPriceMatrix = (): GrokVideoPriceMatrix =>
  Object.fromEntries(
    grokVideoPriceFamilies.map((family) => [
      family,
      Object.fromEntries(grokVideoPriceTiers.map((tier) => [tier, null])),
    ]),
  );

const normalizeGrokVideoPriceMatrix = (
  value?: GrokVideoPriceMatrix,
): GrokVideoPriceMatrix => {
  const matrix = emptyGrokVideoPriceMatrix();
  for (const family of grokVideoPriceFamilies) {
    for (const tier of grokVideoPriceTiers) {
      matrix[family][tier] = value?.[family]?.[tier] ?? null;
    }
  }
  return matrix;
};

const compactGrokVideoPriceMatrix = (matrix: GrokVideoPriceMatrix) => {
  const output: Record<string, Record<string, number>> = {};
  for (const family of grokVideoPriceFamilies) {
    for (const tier of grokVideoPriceTiers) {
      const raw = matrix[family]?.[tier];
      if (raw === null || raw === "" || raw === undefined) continue;
      const price = Number(raw);
      if (!Number.isFinite(price) || price < 0) continue;
      output[family] ||= {};
      output[family][tier] = price;
    }
  }
  return output;
};

const createForm = reactive({
  name: "",
  description: "",
  platform: "anthropic" as GroupPlatform,
  display_brand: "",
  display_icon: "",
  local_only: false,
  scheduling_mode: "default" as GroupSchedulingMode,
  per_user_concurrency: 0,
  rate_multiplier: 1.0,
  peak_rate_enabled: false,
  peak_start: "18:00",
  peak_end: "22:00",
  peak_rate_multiplier: 1,
  peak_timezone: "Asia/Shanghai",
  is_exclusive: false,
	minimum_tier: null as number | null,
  subscription_type: "standard" as SubscriptionType,
  daily_limit_usd: null as number | null,
  weekly_limit_usd: null as number | null,
  monthly_limit_usd: null as number | null,
  // 图片生成计费配置
  allow_image_generation: false,
  image_rate_independent: false,
  image_rate_multiplier: 1,
  image_price_1k: null as number | null,
  image_price_2k: null as number | null,
  image_price_4k: null as number | null,
  // Grok 视频生成按分辨率、按秒计费
  video_rate_independent: false,
  video_rate_multiplier: 1,
  video_price_480p: null as number | null,
  video_price_720p: null as number | null,
  video_price_1080p: null as number | null,
	video_model_prices: emptyGrokVideoPriceMatrix(),
  web_search_price_per_call: null as number | null,
  search_price_per_1k: null as number | null,
	audio_realtime_price_per_min: null as number | null,
	audio_tts_price_per_million_chars: null as number | null,
	audio_stt_price_per_hour: null as number | null,
  allow_openai_long_context_billing: false,
  allow_blocked_keyword_requests: false,
  model_mapping_rows: [] as GroupModelMappingRow[],
  fallback_model: "",
  failover_group_id: null as number | null,
  failover_strategy: "original_model_first" as GroupFailoverStrategy,
  billing_model_source: "requested" as BillingModelSource,
  response_model_mode: "request" as "request" | "upstream" | "custom",
  response_model_custom: "",
  // Claude Code 客户端限制（仅 anthropic 平台使用）
  claude_code_only: false,
  fallback_group_id: null as number | null,
  fallback_group_id_on_invalid_request: null as number | null,
  // OpenAI Messages 调度配置（仅 openai 平台使用）
  allow_messages_dispatch: false,
	allow_live: false,
  opus_mapped_model: createMessagesDispatchDefaults.opus_mapped_model,
  sonnet_mapped_model: createMessagesDispatchDefaults.sonnet_mapped_model,
  haiku_mapped_model: createMessagesDispatchDefaults.haiku_mapped_model,
  exact_model_mappings: [] as MessagesDispatchMappingRow[],
  // 账号过滤控制（OpenAI/Antigravity 平台）
  require_oauth_only: false,
  require_privacy_set: false,
  // 模型路由开关
  model_routing_enabled: false,
  // 支持的模型系列（仅 antigravity 平台）
  supported_model_scopes: ["claude", "gemini_text", "gemini_image"] as string[],
  // MCP XML 协议注入开关（仅 antigravity 平台）
  mcp_xml_inject: true,
  // 从分组复制账号
  copy_accounts_from_group_ids: [] as number[],
  // 分组级 RPM 限制（每用户每分钟最大请求数；0 = 不限制）
  rpm_limit: 0 as number,
  max_reasoning_effort: "",
  reasoning_effort_mappings: [] as ReasoningEffortMappingRow[],
});

// 简单账号类型（用于模型路由选择）
interface SimpleAccount {
  id: number;
  name: string;
}

// 模型路由规则类型
interface ModelRoutingRule {
  pattern: string;
  accounts: SimpleAccount[]; // 选中的账号对象数组
}

// 创建表单的模型路由规则
const createModelRoutingRules = ref<ModelRoutingRule[]>([]);

// 编辑表单的模型路由规则
const editModelRoutingRules = ref<ModelRoutingRule[]>([]);

// 规则对象稳定 key（避免使用 index 导致状态错位）
const resolveCreateRuleKey =
  createStableObjectKeyResolver<ModelRoutingRule>("create-rule");
const resolveEditRuleKey =
  createStableObjectKeyResolver<ModelRoutingRule>("edit-rule");
const resolveCreateMessagesDispatchRowKey =
  createStableObjectKeyResolver<MessagesDispatchMappingRow>(
    "create-messages-dispatch-row",
  );
const resolveEditMessagesDispatchRowKey =
  createStableObjectKeyResolver<MessagesDispatchMappingRow>(
    "edit-messages-dispatch-row",
  );

const getCreateRuleRenderKey = (rule: ModelRoutingRule) =>
  resolveCreateRuleKey(rule);
const getEditRuleRenderKey = (rule: ModelRoutingRule) =>
  resolveEditRuleKey(rule);
const getCreateMessagesDispatchRowKey = (row: MessagesDispatchMappingRow) =>
  resolveCreateMessagesDispatchRowKey(row);
const getEditMessagesDispatchRowKey = (row: MessagesDispatchMappingRow) =>
  resolveEditMessagesDispatchRowKey(row);

const getCreateRuleSearchKey = (rule: ModelRoutingRule) =>
  `create-${resolveCreateRuleKey(rule)}`;
const getEditRuleSearchKey = (rule: ModelRoutingRule) =>
  `edit-${resolveEditRuleKey(rule)}`;

const getRuleSearchKey = (rule: ModelRoutingRule, isEdit: boolean = false) => {
  return isEdit ? getEditRuleSearchKey(rule) : getCreateRuleSearchKey(rule);
};

// 账号搜索相关状态
const accountSearchKeyword = ref<Record<string, string>>({});
const accountSearchResults = ref<Record<string, SimpleAccount[]>>({});
const showAccountDropdown = ref<Record<string, boolean>>({});

const clearAccountSearchStateByKey = (key: string) => {
  delete accountSearchKeyword.value[key];
  delete accountSearchResults.value[key];
  delete showAccountDropdown.value[key];
};

const clearAllAccountSearchState = () => {
  accountSearchKeyword.value = {};
  accountSearchResults.value = {};
  showAccountDropdown.value = {};
};

const accountSearchRunner = useKeyedDebouncedSearch<SimpleAccount[]>({
  delay: 300,
  search: async (keyword, { signal }) => {
    const res = await adminAPI.accounts.list(
      1,
      20,
      {
        search: keyword,
        platform: "anthropic",
      },
      { signal },
    );
    return res.items.map((account) => ({ id: account.id, name: account.name }));
  },
  onSuccess: (key, result) => {
    accountSearchResults.value[key] = result;
  },
  onError: (key) => {
    accountSearchResults.value[key] = [];
  },
});

// 搜索账号（仅限 anthropic 平台）
const searchAccounts = (key: string) => {
  accountSearchRunner.trigger(key, accountSearchKeyword.value[key] || "");
};

const searchAccountsByRule = (
  rule: ModelRoutingRule,
  isEdit: boolean = false,
) => {
  searchAccounts(getRuleSearchKey(rule, isEdit));
};

// 选择账号
const selectAccount = (
  rule: ModelRoutingRule,
  account: SimpleAccount,
  isEdit: boolean = false,
) => {
  if (!rule) return;

  // 检查是否已选择
  if (!rule.accounts.some((a) => a.id === account.id)) {
    rule.accounts.push(account);
  }

  // 清空搜索
  const key = getRuleSearchKey(rule, isEdit);
  accountSearchKeyword.value[key] = "";
  showAccountDropdown.value[key] = false;
};

// 移除已选账号
const removeSelectedAccount = (
  rule: ModelRoutingRule,
  accountId: number,
  _isEdit: boolean = false,
) => {
  if (!rule) return;

  rule.accounts = rule.accounts.filter((a) => a.id !== accountId);
};

// 切换创建表单的模型系列选择
const toggleCreateScope = (scope: string) => {
  const idx = createForm.supported_model_scopes.indexOf(scope);
  if (idx === -1) {
    createForm.supported_model_scopes.push(scope);
  } else {
    createForm.supported_model_scopes.splice(idx, 1);
  }
};

// 切换编辑表单的模型系列选择
const toggleEditScope = (scope: string) => {
  const idx = editForm.supported_model_scopes.indexOf(scope);
  if (idx === -1) {
    editForm.supported_model_scopes.push(scope);
  } else {
    editForm.supported_model_scopes.splice(idx, 1);
  }
};

// 处理账号搜索输入框聚焦
const onAccountSearchFocus = (
  rule: ModelRoutingRule,
  isEdit: boolean = false,
) => {
  const key = getRuleSearchKey(rule, isEdit);
  showAccountDropdown.value[key] = true;
  // 如果没有搜索结果，触发一次搜索
  if (!accountSearchResults.value[key]?.length) {
    searchAccounts(key);
  }
};

// 添加创建表单的路由规则
const addCreateRoutingRule = () => {
  createModelRoutingRules.value.push({ pattern: "", accounts: [] });
};

// 删除创建表单的路由规则
const removeCreateRoutingRule = (rule: ModelRoutingRule) => {
  const index = createModelRoutingRules.value.indexOf(rule);
  if (index === -1) return;

  const key = getCreateRuleSearchKey(rule);
  accountSearchRunner.clearKey(key);
  clearAccountSearchStateByKey(key);
  createModelRoutingRules.value.splice(index, 1);
};

// 添加编辑表单的路由规则
const addEditRoutingRule = () => {
  editModelRoutingRules.value.push({ pattern: "", accounts: [] });
};

// 删除编辑表单的路由规则
const removeEditRoutingRule = (rule: ModelRoutingRule) => {
  const index = editModelRoutingRules.value.indexOf(rule);
  if (index === -1) return;

  const key = getEditRuleSearchKey(rule);
  accountSearchRunner.clearKey(key);
  clearAccountSearchStateByKey(key);
  editModelRoutingRules.value.splice(index, 1);
};

const resetModelsListState = (
  state: typeof createModelsListState,
  config?: Parameters<typeof createInitialModelsListState>[0],
) => {
  const fresh = createInitialModelsListState(config);
  state.enabled = fresh.enabled;
  state.savedModels = fresh.savedModels;
  state.items = fresh.items;
};

const loadModelsListCandidates = async (
  mode: "create" | "edit",
  groupID: number,
  platform: GroupPlatform,
) => {
  const request = { mode, groupID, platform };
  const requestID = modelsListCandidatesTracker.next(request);
  const state = mode === "create" ? createModelsListState : editModelsListState;
  const loadingRef = mode === "create" ? createModelsListLoading : editModelsListLoading;
  loadingRef.value = true;
  try {
    const models = await adminAPI.groups.getModelsListCandidates(groupID, platform);
    if (!modelsListCandidatesTracker.isCurrent(requestID, request)) {
      return;
    }
    setModelsListCandidates(state, models);
  } catch (error) {
    if (!modelsListCandidatesTracker.isCurrent(requestID, request)) {
      return;
    }
    console.error("Error loading group models list candidates:", error);
  } finally {
    if (modelsListCandidatesTracker.isCurrent(requestID, request)) {
      loadingRef.value = false;
    }
  }
};

const moveCreateModelsListItem = (fromIndex: number, toIndex: number) => {
  moveModelsListItem(createModelsListState, fromIndex, toIndex);
};

const moveEditModelsListItem = (fromIndex: number, toIndex: number) => {
  moveModelsListItem(editModelsListState, fromIndex, toIndex);
};

// 将 UI 格式的路由规则转换为 API 格式
const convertRoutingRulesToApiFormat = (
  rules: ModelRoutingRule[],
): Record<string, number[]> | null => {
  const result: Record<string, number[]> = {};
  let hasValidRules = false;

  for (const rule of rules) {
    const pattern = rule.pattern.trim();
    if (!pattern) continue;

    const accountIds = rule.accounts.map((a) => a.id).filter((id) => id > 0);

    if (accountIds.length > 0) {
      result[pattern] = accountIds;
      hasValidRules = true;
    }
  }

  return hasValidRules ? result : null;
};

// 将 API 格式的路由规则转换为 UI 格式（需要加载账号名称）
const convertApiFormatToRoutingRules = async (
  apiFormat: Record<string, number[]> | null,
): Promise<ModelRoutingRule[]> => {
  if (!apiFormat) return [];

  const rules: ModelRoutingRule[] = [];
  for (const [pattern, accountIds] of Object.entries(apiFormat)) {
    // 加载账号信息
    const accounts: SimpleAccount[] = [];
    for (const id of accountIds) {
      try {
        const account = await adminAPI.accounts.getById(id);
        accounts.push({ id: account.id, name: account.name });
      } catch {
        // 如果账号不存在，仍然显示 ID
        accounts.push({ id, name: `#${id}` });
      }
    }
    rules.push({ pattern, accounts });
  }
  return rules;
};

const editForm = reactive({
  name: "",
  description: "",
  platform: "anthropic" as GroupPlatform,
  display_brand: "",
  display_icon: "",
  local_only: false,
  scheduling_mode: "default" as GroupSchedulingMode,
  per_user_concurrency: 0,
  rate_multiplier: 1.0,
  peak_rate_enabled: false,
  peak_start: "18:00",
  peak_end: "22:00",
  peak_rate_multiplier: 1,
  peak_timezone: "Asia/Shanghai",
  is_exclusive: false,
	minimum_tier: null as number | null,
  status: "active" as "active" | "inactive",
  subscription_type: "standard" as SubscriptionType,
  daily_limit_usd: null as number | null,
  weekly_limit_usd: null as number | null,
  monthly_limit_usd: null as number | null,
  // 图片生成计费配置
  allow_image_generation: false,
  image_rate_independent: false,
  image_rate_multiplier: 1,
  image_price_1k: null as number | null,
  image_price_2k: null as number | null,
  image_price_4k: null as number | null,
  // Grok 视频生成按分辨率、按秒计费
  video_rate_independent: false,
  video_rate_multiplier: 1,
  video_price_480p: null as number | null,
  video_price_720p: null as number | null,
  video_price_1080p: null as number | null,
	video_model_prices: emptyGrokVideoPriceMatrix(),
  web_search_price_per_call: null as number | null,
  search_price_per_1k: null as number | null,
	audio_realtime_price_per_min: null as number | null,
	audio_tts_price_per_million_chars: null as number | null,
	audio_stt_price_per_hour: null as number | null,
  allow_openai_long_context_billing: false,
  allow_blocked_keyword_requests: false,
  model_mapping_rows: [] as GroupModelMappingRow[],
  fallback_model: "",
  failover_group_id: null as number | null,
  failover_strategy: "original_model_first" as GroupFailoverStrategy,
  billing_model_source: "requested" as BillingModelSource,
  response_model_mode: "request" as "request" | "upstream" | "custom",
  response_model_custom: "",
  // Claude Code 客户端限制（仅 anthropic 平台使用）
  claude_code_only: false,
  fallback_group_id: null as number | null,
  fallback_group_id_on_invalid_request: null as number | null,
  // OpenAI Messages 调度配置（仅 openai 平台使用）
  allow_messages_dispatch: false,
	allow_live: false,
  default_mapped_model: '',
  opus_mapped_model: editMessagesDispatchDefaults.opus_mapped_model,
  sonnet_mapped_model: editMessagesDispatchDefaults.sonnet_mapped_model,
  haiku_mapped_model: editMessagesDispatchDefaults.haiku_mapped_model,
  exact_model_mappings: [] as MessagesDispatchMappingRow[],
  // 账号过滤控制（OpenAI/Antigravity 平台）
  require_oauth_only: false,
  require_privacy_set: false,
  // 模型路由开关
  model_routing_enabled: false,
  // 支持的模型系列（仅 antigravity 平台）
  supported_model_scopes: ["claude", "gemini_text", "gemini_image"] as string[],
  // MCP XML 协议注入开关（仅 antigravity 平台）
  mcp_xml_inject: true,
  // 从分组复制账号
  copy_accounts_from_group_ids: [] as number[],
  // 分组级 RPM 限制（每用户每分钟最大请求数；0 = 不限制）
  rpm_limit: 0 as number,
  max_reasoning_effort: "",
  reasoning_effort_mappings: [] as ReasoningEffortMappingRow[],
});

const minimumTierOptions = [
  { value: null, label: "继承节点" },
  { value: 0, label: "T0" },
  { value: 1, label: "T1" },
  { value: 2, label: "T2" },
];

const createOpenAILongContextBillingSummary = computed(() =>
  summarizeOpenAILongContextBillingAccounts(
    groups.value,
    createForm.copy_accounts_from_group_ids,
  ),
);

const editOpenAILongContextBillingSummary = computed(() =>
  summarizeOpenAILongContextBillingAccounts(
    groups.value,
    editForm.copy_accounts_from_group_ids,
    editingGroup.value,
  ),
);

type ImagePricingFormState = {
  rate_multiplier: number;
  image_rate_independent: boolean;
  image_rate_multiplier: number;
  image_price_1k: number | string | null;
  image_price_2k: number | string | null;
  image_price_4k: number | string | null;
};

type VideoPricingFormState = {
  rate_multiplier: number;
  video_rate_independent: boolean;
  video_rate_multiplier: number;
  video_price_480p: number | string | null;
  video_price_720p: number | string | null;
  video_price_1080p: number | string | null;
};

const imagePricingTiers = [
  { key: "image_price_1k", label: "1K" },
  { key: "image_price_2k", label: "2K" },
  { key: "image_price_4k", label: "4K" },
] as const;

const videoPricingTiers = [
  { key: "video_price_480p", label: "480p" },
  { key: "video_price_720p", label: "720p" },
  { key: "video_price_1080p", label: "1080p" },
] as const;

const normalizePreviewNumber = (value: number | string | null | undefined, fallback = 0) => {
  if (value === null || value === undefined || value === "") {
    return fallback;
  }
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : fallback;
};

const formatImagePricePreview = (value: number | string | null | undefined) => {
  if (value === null || value === undefined || value === "") {
    return t("admin.groups.imagePricing.notConfigured");
  }
  const price = Number(value);
  if (!Number.isFinite(price) || price < 0) {
    return t("admin.groups.imagePricing.notConfigured");
  }
  return `$${price.toFixed(6).replace(/0+$/, "").replace(/\.$/, "")}`;
};

const formatVideoPricePreview = (value: number | string | null | undefined) => {
  if (value === null || value === undefined || value === "") {
    return t("admin.groups.videoPricing.useCatalogPrice");
  }
  const price = Number(value);
  if (!Number.isFinite(price) || price < 0) {
    return t("admin.groups.videoPricing.useCatalogPrice");
  }
  return `$${price.toFixed(6).replace(/0+$/, "").replace(/\.$/, "")}`;
};

const buildImageFinalPricePreview = (form: ImagePricingFormState) => {
  const multiplier = form.image_rate_independent
    ? normalizePreviewNumber(form.image_rate_multiplier, 1)
    : normalizePreviewNumber(form.rate_multiplier, 1);
  return imagePricingTiers.map((tier) => {
    const basePrice = normalizePreviewNumber(form[tier.key]);
    return {
      label: tier.label,
      value: basePrice > 0
        ? formatImagePricePreview(basePrice * multiplier)
        : t("admin.groups.imagePricing.notConfigured"),
    };
  });
};

const buildVideoFinalPricePreview = (form: VideoPricingFormState) => {
  const multiplier = form.video_rate_independent
    ? normalizePreviewNumber(form.video_rate_multiplier, 1)
    : normalizePreviewNumber(form.rate_multiplier, 1);
  return videoPricingTiers.map((tier) => {
    const rawPrice = form[tier.key];
    if (rawPrice === null || rawPrice === undefined || rawPrice === "") {
      return {
        label: tier.label,
        value: t("admin.groups.videoPricing.useCatalogPrice"),
      };
    }
    const basePrice = Number(rawPrice);
    return {
      label: tier.label,
      value: Number.isFinite(basePrice) && basePrice >= 0
        ? formatVideoPricePreview(basePrice * multiplier)
        : t("admin.groups.videoPricing.useCatalogPrice"),
    };
  });
};

const createImageFinalPricePreview = computed(() =>
  buildImageFinalPricePreview(createForm).filter(
    (item) => createForm.platform !== "grok" || item.label !== "4K",
  ),
);
const editImageFinalPricePreview = computed(() =>
  buildImageFinalPricePreview(editForm).filter(
    (item) => editForm.platform !== "grok" || item.label !== "4K",
  ),
);
const createVideoFinalPricePreview = computed(() =>
  buildVideoFinalPricePreview(createForm),
);
const editVideoFinalPricePreview = computed(() =>
  buildVideoFinalPricePreview(editForm),
);

const DEFAULT_WEB_SEARCH_PRICE_PER_CALL = 0.01;
const DEFAULT_GROK_SEARCH_PRICE_PER_1K = 10;

const buildWebSearchFinalPricePreview = (form: {
  web_search_price_per_call: number | string | null;
  rate_multiplier: number | string | null;
}) => {
  const configuredPrice = form.web_search_price_per_call;
  const basePrice =
    configuredPrice === null || configuredPrice === ""
      ? DEFAULT_WEB_SEARCH_PRICE_PER_CALL
      : normalizePreviewNumber(configuredPrice, DEFAULT_WEB_SEARCH_PRICE_PER_CALL);
  const multiplier = normalizePreviewNumber(form.rate_multiplier, 1);
  return formatImagePricePreview(basePrice * multiplier);
};

const createWebSearchFinalPricePreview = computed(() =>
  buildWebSearchFinalPricePreview(createForm),
);
const editWebSearchFinalPricePreview = computed(() =>
  buildWebSearchFinalPricePreview(editForm),
);

const buildGrokSearchFinalPricePreview = (form: {
  search_price_per_1k: number | string | null;
  rate_multiplier: number | string | null;
}) => {
  const configuredPrice = form.search_price_per_1k;
  const basePrice =
    configuredPrice === null || configuredPrice === ""
      ? DEFAULT_GROK_SEARCH_PRICE_PER_1K
      : normalizePreviewNumber(configuredPrice, DEFAULT_GROK_SEARCH_PRICE_PER_1K);
  return formatImagePricePreview(basePrice * normalizePreviewNumber(form.rate_multiplier, 1));
};

const createGrokSearchFinalPricePreview = computed(() =>
  buildGrokSearchFinalPricePreview(createForm),
);
const editGrokSearchFinalPricePreview = computed(() =>
  buildGrokSearchFinalPricePreview(editForm),
);

// 根据分组类型返回不同的删除确认消息
const deleteConfirmMessage = computed(() => {
  if (!deletingGroup.value) {
    return "";
  }
  if (deletingGroup.value.subscription_type === "subscription") {
    return t("admin.groups.deleteConfirmSubscription", {
      name: deletingGroup.value.name,
    });
  }
  return t("admin.groups.deleteConfirm", { name: deletingGroup.value.name });
});

const loadGroups = async () => {
  if (abortController) {
    abortController.abort();
  }
  const currentController = new AbortController();
  abortController = currentController;
  const { signal } = currentController;
  loading.value = true;
  try {
    const response = await adminAPI.groups.list(
      pagination.page,
      pagination.page_size,
      {
        platform: (filters.platform as GroupPlatform) || undefined,
        status: filters.status as any,
        is_exclusive: filters.is_exclusive
          ? filters.is_exclusive === "true"
          : undefined,
        search: searchQuery.value.trim() || undefined,
        sort_by: sortState.sort_by,
        sort_order: sortState.sort_order,
      },
      { signal },
    );
    if (signal.aborted) return;
    groups.value = response.items;
    pagination.total = response.total;
    pagination.pages = response.pages;
    loadUsageSummary();
    loadCapacitySummary();
  } catch (error: any) {
    if (
      signal.aborted ||
      error?.name === "AbortError" ||
      error?.code === "ERR_CANCELED"
    ) {
      return;
    }
    appStore.showError(t("admin.groups.failedToLoad"));
    console.error("Error loading groups:", error);
  } finally {
    if (abortController === currentController && !signal.aborted) {
      loading.value = false;
    }
  }
};

const formatCost = (cost: number): string => {
  if (cost >= 1000) return cost.toFixed(0);
  if (cost >= 100) return cost.toFixed(1);
  return cost.toFixed(2);
};

const formatRequestCount = (count: number): string =>
  Math.max(0, Number(count) || 0).toLocaleString();

const loadUsageSummary = async () => {
  usageLoading.value = true;
  try {
    const data = await adminAPI.groups.getUsageSummary(DEFAULT_TIME_ZONE);
    const map = new Map<number, {
      today_cost: number;
      total_cost: number;
      today_requests: number;
      total_requests: number;
      today_rerouted_requests: number;
      total_rerouted_requests: number;
    }>();
    for (const item of data) {
      map.set(item.group_id, {
        today_cost: item.today_cost,
        total_cost: item.total_cost,
        today_requests: item.today_requests ?? 0,
        total_requests: item.total_requests ?? 0,
        today_rerouted_requests: item.today_rerouted_requests ?? 0,
        total_rerouted_requests: item.total_rerouted_requests ?? 0,
      });
    }
    usageMap.value = map;
  } catch (error) {
    console.error("Error loading group usage summary:", error);
  } finally {
    usageLoading.value = false;
  }
};

const loadCapacitySummary = async () => {
  try {
    const data = await adminAPI.groups.getCapacitySummary();
    const map = new Map<
      number,
      {
        concurrencyUsed: number;
        concurrencyMax: number;
        sessionsUsed: number;
        sessionsMax: number;
        rpmUsed: number;
        rpmMax: number;
        ingressRequestsUsed: number | null;
      }
    >();
    for (const item of data) {
      map.set(item.group_id, {
        concurrencyUsed: item.concurrency_used,
        concurrencyMax: item.concurrency_max,
        sessionsUsed: item.sessions_used,
        sessionsMax: item.sessions_max,
        rpmUsed: item.rpm_used,
        rpmMax: item.rpm_max,
        ingressRequestsUsed: normalizeGroupIngressRequests(item.ingress_requests_used),
      });
    }
    capacityMap.value = map;
    capacityLoaded.value = true;
    capacityLastUpdated.value = new Date();
  } catch (error) {
    capacityMap.value = new Map();
    capacityLoaded.value = false;
    console.error("Error loading group capacity summary:", error);
  }
};

const getIngressRequestsUsed = (groupID: number): number | null =>
  capacityMap.value.get(groupID)?.ingressRequestsUsed ?? null;

const capacityLastUpdatedLabel = computed(() => {
  if (!capacityLastUpdated.value) return "";
  return `${t("admin.groups.capacityUpdatedAt")} ${capacityLastUpdated.value.toLocaleTimeString()}`;
});

let searchTimeout: ReturnType<typeof setTimeout>;
const handleSearch = () => {
  clearTimeout(searchTimeout);
  searchTimeout = setTimeout(() => {
    pagination.page = 1;
    loadGroups();
  }, 300);
};

const handlePageChange = (page: number) => {
  pagination.page = page;
  loadGroups();
};

const handlePageSizeChange = (pageSize: number) => {
  pagination.page_size = pageSize;
  pagination.page = 1;
  loadGroups();
};

const handleSort = (key: string, order: 'asc' | 'desc') => {
  sortState.sort_by = key;
  sortState.sort_order = order;
  pagination.page = 1;
  loadGroups();
};

const openCreateModal = () => {
  createPricingForm.value = createGroupPricingFormState();
  createEditorSection.value = "basics";
  showCreateModal.value = true;
  loadFailoverGroups();
  loadModelsListCandidates("create", 0, createForm.platform);
};

const loadFailoverGroups = async () => {
  try {
    failoverGroups.value = await adminAPI.groups.getAll("openai");
  } catch (error) {
    failoverGroups.value = [];
    console.error("Error loading failover groups:", error);
  }
};

const closeCreateModal = () => {
  showCreateModal.value = false;
  createEditorSection.value = "basics";
  createModelRoutingRules.value.forEach((rule) => {
    accountSearchRunner.clearKey(getCreateRuleSearchKey(rule));
  });
  clearAllAccountSearchState();
  createForm.name = "";
  createForm.description = "";
  createForm.platform = "anthropic";
  createForm.display_brand = "";
  createForm.display_icon = "";
  createForm.local_only = false;
  createForm.scheduling_mode = "default";
  createForm.per_user_concurrency = 0;
  createForm.rate_multiplier = 1.0;
  createForm.peak_rate_enabled = false;
  createForm.peak_start = "18:00";
  createForm.peak_end = "22:00";
  createForm.peak_rate_multiplier = 1;
  createForm.peak_timezone = "Asia/Shanghai";
  createForm.is_exclusive = false;
	createForm.minimum_tier = null;
  createForm.subscription_type = "standard";
  createForm.daily_limit_usd = null;
  createForm.weekly_limit_usd = null;
  createForm.monthly_limit_usd = null;
  createForm.allow_image_generation = false;
  createForm.image_rate_independent = false;
  createForm.image_rate_multiplier = 1;
  createForm.image_price_1k = null;
  createForm.image_price_2k = null;
  createForm.image_price_4k = null;
  createForm.video_rate_independent = false;
  createForm.video_rate_multiplier = 1;
  createForm.video_price_480p = null;
  createForm.video_price_720p = null;
  createForm.video_price_1080p = null;
	createForm.video_model_prices = emptyGrokVideoPriceMatrix();
  createForm.web_search_price_per_call = null;
  createForm.search_price_per_1k = null;
	createForm.audio_realtime_price_per_min = null;
	createForm.audio_tts_price_per_million_chars = null;
	createForm.audio_stt_price_per_hour = null;
  createForm.allow_openai_long_context_billing = false;
  createForm.allow_blocked_keyword_requests = false;
  createForm.fallback_model = "";
  createForm.failover_group_id = null;
  createForm.failover_strategy = "original_model_first";
  createForm.model_mapping_rows = [];
  createForm.billing_model_source = "requested";
	createForm.allow_live = false;
  createForm.claude_code_only = false;
  createForm.fallback_group_id = null;
  createForm.fallback_group_id_on_invalid_request = null;
  resetMessagesDispatchFormState(createForm);
  createForm.require_oauth_only = false;
  createForm.require_privacy_set = false;
  createForm.supported_model_scopes = ["claude", "gemini_text", "gemini_image"];
  createForm.mcp_xml_inject = true;
  createForm.copy_accounts_from_group_ids = [];
  createForm.rpm_limit = 0;
  createForm.max_reasoning_effort = "";
  createForm.reasoning_effort_mappings = [];
  createReasoningEffortPolicyRef.value?.resetValidation();
  resetModelsListState(createModelsListState);
  createModelRoutingRules.value = [];
  createPricingForm.value = createGroupPricingFormState();
};

const normalizeOptionalLimit = (
  value: number | string | null | undefined,
): number | null => {
  if (value === null || value === undefined) {
    return null;
  }

  if (typeof value === "string") {
    const trimmed = value.trim();
    if (!trimmed) {
      return null;
    }
    const parsed = Number(trimmed);
    return Number.isFinite(parsed) && parsed > 0 ? parsed : null;
  }

  return Number.isFinite(value) && value > 0 ? value : null;
};

const normalizeImageRateMultiplier = (
  value: number | string | null | undefined,
): number => {
  if (value === null || value === undefined || value === "") {
    return 1;
  }
  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : 1;
};

const handleCreateGroup = async () => {
  if (!createForm.name.trim()) {
    appStore.showError(t("admin.groups.nameRequired"));
    return;
  }
  if (createForm.platform === "composite" && !compositeGroupsEnabled.value) {
    appStore.showError(t("admin.groups.compositeDisabledSaveHint"));
    return;
  }
  const modelMappingError = validateGroupModelMappingRows(
    createForm.model_mapping_rows,
    createForm.platform,
  );
  if (modelMappingError) {
    appStore.showError(t(`admin.groups.modelMapping.${modelMappingError}`));
    return;
  }
  const pricingError = validateGroupPricingForm(createPricingForm.value, createForm.platform);
  if (pricingError) {
    appStore.showError(t(`admin.groups.pricing.errors.${pricingError.split(':')[0]}`));
    return;
  }
  if (createModelsListState.enabled && createModelsListSelectedCount.value === 0) {
    appStore.showError(t("admin.groups.modelsList.selectionRequired"));
    return;
  }
  if (
    (createForm.platform === "openai" || createForm.platform === "composite") &&
    createReasoningEffortPolicyRef.value &&
    !createReasoningEffortPolicyRef.value.validate()
  ) {
    return;
  }
  submitting.value = true;
  try {
    // 构建请求数据，包含模型路由配置
    const requestData = {
      ...createForm,
      model_mapping: groupModelMappingRowsToRecord(createForm.model_mapping_rows),
      weighted_model_mapping: groupWeightedMappingRowsToRecord(
        createForm.model_mapping_rows,
      ),
      daily_limit_usd: normalizeOptionalLimit(
        createForm.daily_limit_usd as number | string | null,
      ),
      weekly_limit_usd: normalizeOptionalLimit(
        createForm.weekly_limit_usd as number | string | null,
      ),
      monthly_limit_usd: normalizeOptionalLimit(
        createForm.monthly_limit_usd as number | string | null,
      ),
      model_routing: convertRoutingRulesToApiFormat(
        createModelRoutingRules.value,
      ),
      models_list_config: {
        ...buildModelsListConfig(createModelsListState),
        response_model_mode: createForm.response_model_mode,
        response_model_custom: createForm.response_model_custom.trim(),
      },
      supported_model_scopes: normalizeSupportedModelScopesForPlatform(
        createForm.platform,
        createForm.supported_model_scopes,
      ),
      messages_dispatch_model_config:
        createForm.platform === "openai"
          ? messagesDispatchFormStateToConfig({
              allow_messages_dispatch: createForm.allow_messages_dispatch,
              opus_mapped_model: createForm.opus_mapped_model,
              sonnet_mapped_model: createForm.sonnet_mapped_model,
              haiku_mapped_model: createForm.haiku_mapped_model,
              exact_model_mappings: createForm.exact_model_mappings,
            })
          : undefined,
      reasoning_effort_mappings: reasoningEffortMappingsToAPI(
        createForm.reasoning_effort_mappings,
      ),
    };
    delete (requestData as Record<string, unknown>).model_mapping_rows;
    // v-model.number 清空输入框时产生 ""，转为 null 让后端设为无限制
    const emptyToNull = (v: any) => (v === "" ? null : v);
    requestData.daily_limit_usd = emptyToNull(requestData.daily_limit_usd);
    requestData.weekly_limit_usd = emptyToNull(requestData.weekly_limit_usd);
    requestData.monthly_limit_usd = emptyToNull(requestData.monthly_limit_usd);
    requestData.web_search_price_per_call = emptyToNull(
      requestData.web_search_price_per_call,
    );
		requestData.search_price_per_1k = emptyToNull(requestData.search_price_per_1k);
		requestData.audio_realtime_price_per_min = emptyToNull(requestData.audio_realtime_price_per_min);
		requestData.audio_tts_price_per_million_chars = emptyToNull(requestData.audio_tts_price_per_million_chars);
		requestData.audio_stt_price_per_hour = emptyToNull(requestData.audio_stt_price_per_hour);
		requestData.video_model_prices = compactGrokVideoPriceMatrix(createForm.video_model_prices);
    requestData.image_rate_multiplier = normalizeImageRateMultiplier(
      requestData.image_rate_multiplier,
    );
    requestData.video_rate_multiplier = normalizeImageRateMultiplier(
      requestData.video_rate_multiplier,
    );
    const customPricingEnabled = createPricingForm.value.enabled;
    // 自定义价格必须先保存到停用草稿，避免分组在价格尚未落库时短暂对外生效。
    const created = await adminAPI.groups.create({
      ...requestData,
      status: customPricingEnabled ? "inactive" : "active",
    });
    if (customPricingEnabled) {
      try {
        await adminAPI.groups.updatePricing(
          created.id,
          groupPricingFormToRequest(createPricingForm.value, createForm.platform, created.id),
        );
        await adminAPI.groups.update(created.id, { status: "active" });
      } catch (pricingSaveError) {
        appStore.showError(t("admin.groups.pricing.draftSaved", { name: created.name }));
        closeCreateModal();
        await loadGroups();
        await handleEdit({ ...created, status: "inactive" });
        console.error("Error saving group pricing draft:", pricingSaveError);
        return;
      }
    }
    appStore.showSuccess(t("admin.groups.groupCreated"));
    closeCreateModal();
    loadGroups();
  } catch (error: any) {
    appStore.showError(
      error.response?.data?.detail || t("admin.groups.failedToCreate"),
    );
    console.error("Error creating group:", error);
    // Don't advance tour on error
  } finally {
    submitting.value = false;
  }
};

// 手动清除某个分流目标的冷却状态：管理员确认上游恢复后立即放行探测。
const handleResetRouteHealth = async (publicModel: string, targetModel: string) => {
  const groupID = editingGroup.value?.id;
  if (!groupID) {
    return;
  }
  try {
    await adminAPI.groups.resetModelRouteHealth(groupID, publicModel, targetModel);
    editRouteHealth.value = await adminAPI.groups.getModelRouteHealth(groupID);
    appStore.showSuccess(t("admin.groups.modelMapping.resetHealthDone"));
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, t("admin.groups.modelMapping.resetHealthFailed")));
  }
};

const handleEdit = async (group: AdminGroup) => {
  const sessionSerial = ++editSessionSerial;
  editEditorSection.value = "basics";
  editingGroup.value = group;
  // 智能分流健康徽标：openai 分组才有数据，非阻塞加载避免拖慢弹窗。
  editRouteHealth.value = [];
  if (group.platform === "openai" && group.id) {
    const healthGroupID = group.id;
    void adminAPI.groups
      .getModelRouteHealth(healthGroupID)
      .then((items) => {
        if (sessionSerial === editSessionSerial) {
          editRouteHealth.value = items ?? [];
        }
      })
      .catch(() => {
        // 健康数据不可用不影响编辑，仅不展示徽标。
      });
  }
  editForm.name = group.name;
  editForm.description = group.description || "";
  editForm.platform = group.platform;
  editForm.display_brand = group.display_brand || "";
  editForm.display_icon = group.display_icon || "";
  editForm.local_only = group.local_only ?? false;
  editForm.scheduling_mode = group.scheduling_mode || "default";
  editForm.per_user_concurrency = group.per_user_concurrency || 0;
  editForm.rate_multiplier = group.rate_multiplier;
  editForm.peak_rate_enabled = group.peak_rate_enabled ?? false;
  editForm.peak_start = group.peak_start || "18:00";
  editForm.peak_end = group.peak_end || "22:00";
  editForm.peak_rate_multiplier = group.peak_rate_multiplier ?? 1;
  editForm.peak_timezone = group.peak_timezone || "Asia/Shanghai";
  editForm.is_exclusive = group.is_exclusive;
	editForm.minimum_tier = group.minimum_tier ?? null;
  editForm.status = group.status;
  editForm.subscription_type = group.subscription_type || "standard";
  editForm.daily_limit_usd = group.daily_limit_usd;
  editForm.weekly_limit_usd = group.weekly_limit_usd;
  editForm.monthly_limit_usd = group.monthly_limit_usd;
  editForm.allow_image_generation = group.allow_image_generation ?? false;
  editForm.image_rate_independent = group.image_rate_independent ?? false;
  editForm.image_rate_multiplier = group.image_rate_multiplier ?? 1;
  editForm.image_price_1k = group.image_price_1k;
  editForm.image_price_2k = group.image_price_2k;
  editForm.image_price_4k = group.image_price_4k;
  editForm.video_rate_independent = group.video_rate_independent ?? false;
  editForm.video_rate_multiplier = group.video_rate_multiplier ?? 1;
  editForm.video_price_480p = group.video_price_480p;
  editForm.video_price_720p = group.video_price_720p;
  editForm.video_price_1080p = group.video_price_1080p;
	editForm.video_model_prices = normalizeGrokVideoPriceMatrix(group.video_model_prices);
  editForm.web_search_price_per_call = group.web_search_price_per_call ?? null;
	editForm.search_price_per_1k = group.search_price_per_1k ?? null;
	editForm.audio_realtime_price_per_min = group.audio_realtime_price_per_min ?? null;
	editForm.audio_tts_price_per_million_chars = group.audio_tts_price_per_million_chars ?? null;
	editForm.audio_stt_price_per_hour = group.audio_stt_price_per_hour ?? null;
  editForm.allow_openai_long_context_billing =
    group.allow_openai_long_context_billing ?? false;
  editForm.allow_blocked_keyword_requests =
    group.allow_blocked_keyword_requests ?? false;
  editForm.model_mapping_rows = groupModelMappingToRows(
    group.model_mapping,
    group.weighted_model_mapping,
  );
  editForm.fallback_model = group.fallback_model || "";
  editForm.failover_group_id = group.failover_group_id ?? null;
  editForm.failover_strategy = group.failover_strategy || "original_model_first";
  editForm.billing_model_source = group.billing_model_source || "requested";
	editForm.allow_live = group.allow_live ?? false;
  editForm.claude_code_only = group.claude_code_only || false;
  editForm.fallback_group_id = group.fallback_group_id;
  editForm.fallback_group_id_on_invalid_request =
    group.fallback_group_id_on_invalid_request;
  const messagesDispatchFormState = messagesDispatchConfigToFormState(
    group.messages_dispatch_model_config,
  );
  editForm.allow_messages_dispatch =
    group.allow_messages_dispatch ||
    messagesDispatchFormState.allow_messages_dispatch;
  editForm.opus_mapped_model = messagesDispatchFormState.opus_mapped_model;
  editForm.sonnet_mapped_model = messagesDispatchFormState.sonnet_mapped_model;
  editForm.haiku_mapped_model = messagesDispatchFormState.haiku_mapped_model;
  editForm.exact_model_mappings =
    messagesDispatchFormState.exact_model_mappings;
  editForm.require_oauth_only = group.require_oauth_only ?? false;
  editForm.require_privacy_set = group.require_privacy_set ?? false;
  editForm.model_routing_enabled = group.model_routing_enabled || false;
  editForm.supported_model_scopes = group.supported_model_scopes || [
    "claude",
    "gemini_text",
    "gemini_image",
  ];
  editForm.mcp_xml_inject = group.mcp_xml_inject ?? true;
  editForm.copy_accounts_from_group_ids = []; // 复制账号字段每次编辑时重置为空
  editForm.rpm_limit = group.rpm_limit ?? 0;
  editForm.max_reasoning_effort = normalizeReasoningEffortForPlatform(
    group.platform,
    group.max_reasoning_effort,
  );
  editForm.reasoning_effort_mappings = reasoningEffortMappingsToRows(
    group.reasoning_effort_mappings,
    group.platform,
  );
  resetModelsListState(editModelsListState, group.models_list_config);
  editForm.response_model_mode = group.models_list_config?.response_model_mode || "request";
  editForm.response_model_custom = group.models_list_config?.response_model_custom || "";
  editPricingForm.value = createGroupPricingFormState();
  editPricingShared.value = false;
  editPricingReady.value = false;
  editPricingLoadError.value = false;
  editPricingLoading.value = true;
  // 加载模型路由规则（异步加载账号名称）
  const modelRoutingRules = await convertApiFormatToRoutingRules(
    group.model_routing,
  );
  if (sessionSerial !== editSessionSerial || editingGroup.value?.id !== group.id) return;
  editModelRoutingRules.value = modelRoutingRules;
  loadModelsListCandidates("edit", group.id, group.platform);
  loadFailoverGroups();
  showEditModal.value = true;
  await loadEditPricing(group, sessionSerial);
};

const loadEditPricing = async (group: AdminGroup, sessionSerial = editSessionSerial) => {
  const loadSerial = ++editPricingLoadSerial;
  editPricingLoading.value = true;
  editPricingReady.value = false;
  editPricingLoadError.value = false;
  try {
    const pricing = await adminAPI.groups.getPricing(group.id);
    if (
      sessionSerial !== editSessionSerial ||
      loadSerial !== editPricingLoadSerial ||
      !showEditModal.value ||
      editingGroup.value?.id !== group.id
    ) {
      return;
    }
    editPricingForm.value = groupPricingConfigToForm(pricing, group.platform);
    editPricingShared.value = pricing.shared;
    editPricingReady.value = true;
  } catch (error) {
    if (
      sessionSerial !== editSessionSerial ||
      loadSerial !== editPricingLoadSerial ||
      !showEditModal.value ||
      editingGroup.value?.id !== group.id
    ) {
      return;
    }
    editPricingLoadError.value = true;
    appStore.showError(extractApiErrorMessage(error, t("admin.groups.pricing.loadFailed")));
  } finally {
    if (sessionSerial === editSessionSerial && loadSerial === editPricingLoadSerial) {
      editPricingLoading.value = false;
    }
  }
};

const retryEditPricing = async () => {
  if (editingGroup.value) await loadEditPricing(editingGroup.value);
};

const closeEditModal = () => {
  // 关闭或切换分组后，让迟到的异步响应失效，避免把旧分组价格写入当前表单。
  editSessionSerial += 1;
  editPricingLoadSerial += 1;
  editRouteHealth.value = [];
  editModelRoutingRules.value.forEach((rule) => {
    accountSearchRunner.clearKey(getEditRuleSearchKey(rule));
  });
  clearAllAccountSearchState();
  showEditModal.value = false;
  editEditorSection.value = "basics";
  editingGroup.value = null;
  editForm.max_reasoning_effort = "";
  editForm.reasoning_effort_mappings = [];
  editReasoningEffortPolicyRef.value?.resetValidation();
  editModelRoutingRules.value = [];
  editForm.copy_accounts_from_group_ids = [];
  editForm.video_rate_independent = false;
  editForm.video_rate_multiplier = 1;
  editForm.video_price_480p = null;
  editForm.video_price_720p = null;
  editForm.video_price_1080p = null;
	editForm.video_model_prices = emptyGrokVideoPriceMatrix();
  editForm.web_search_price_per_call = null;
	editForm.search_price_per_1k = null;
	editForm.audio_realtime_price_per_min = null;
	editForm.audio_tts_price_per_million_chars = null;
	editForm.audio_stt_price_per_hour = null;
  editForm.allow_openai_long_context_billing = false;
  editForm.allow_blocked_keyword_requests = false;
  editForm.fallback_model = "";
  editForm.failover_group_id = null;
  editForm.failover_strategy = "original_model_first";
  editForm.model_mapping_rows = [];
  editForm.billing_model_source = "requested";
  editForm.local_only = false;
  editForm.scheduling_mode = "default";
  editForm.per_user_concurrency = 0;
  resetMessagesDispatchFormState(editForm);
  resetModelsListState(editModelsListState);
  editPricingForm.value = createGroupPricingFormState();
  editPricingLoading.value = false;
  editPricingShared.value = false;
  editPricingReady.value = false;
  editPricingLoadError.value = false;
};

const handleUpdateGroup = async () => {
  if (!editingGroup.value) return;
  if (!editPricingReady.value || editPricingLoading.value) {
    appStore.showError(t("admin.groups.pricing.notReady"));
    return;
  }
  if (!editForm.name.trim()) {
    appStore.showError(t("admin.groups.nameRequired"));
    return;
  }
  if (editForm.platform === "composite" && !compositeGroupsEnabled.value) {
    appStore.showError(t("admin.groups.compositeDisabledSaveHint"));
    return;
  }
  const modelMappingError = validateGroupModelMappingRows(
    editForm.model_mapping_rows,
    editForm.platform,
  );
  if (modelMappingError) {
    appStore.showError(t(`admin.groups.modelMapping.${modelMappingError}`));
    return;
  }
  const pricingError = validateGroupPricingForm(editPricingForm.value, editForm.platform);
  if (pricingError) {
    appStore.showError(t(`admin.groups.pricing.errors.${pricingError.split(':')[0]}`));
    return;
  }
  if (editModelsListState.enabled && editModelsListSelectedCount.value === 0) {
    appStore.showError(t("admin.groups.modelsList.selectionRequired"));
    return;
  }
  if (
    (editForm.platform === "openai" || editForm.platform === "composite") &&
    editReasoningEffortPolicyRef.value &&
    !editReasoningEffortPolicyRef.value.validate()
  ) {
    return;
  }

  submitting.value = true;
  let pricingSaved = false;
  try {
    // 转换 fallback_group_id: null -> 0 (后端使用 0 表示清除)
    const payload = {
      ...editForm,
      model_mapping: groupModelMappingRowsToRecord(editForm.model_mapping_rows),
      weighted_model_mapping: groupWeightedMappingRowsToRecord(
        editForm.model_mapping_rows,
      ),
      daily_limit_usd: normalizeOptionalLimit(
        editForm.daily_limit_usd as number | string | null,
      ),
      weekly_limit_usd: normalizeOptionalLimit(
        editForm.weekly_limit_usd as number | string | null,
      ),
      monthly_limit_usd: normalizeOptionalLimit(
        editForm.monthly_limit_usd as number | string | null,
      ),
      fallback_group_id:
        editForm.fallback_group_id === null ? 0 : editForm.fallback_group_id,
      fallback_group_id_on_invalid_request:
        editForm.fallback_group_id_on_invalid_request === null
          ? 0
          : editForm.fallback_group_id_on_invalid_request,
      failover_group_id:
        editForm.failover_group_id === null ? 0 : editForm.failover_group_id,
      model_routing: convertRoutingRulesToApiFormat(
        editModelRoutingRules.value,
      ),
      models_list_config: {
        ...buildModelsListConfig(editModelsListState),
        response_model_mode: editForm.response_model_mode,
        response_model_custom: editForm.response_model_custom.trim(),
      },
      supported_model_scopes: normalizeSupportedModelScopesForPlatform(
        editForm.platform,
        editForm.supported_model_scopes,
      ),
      messages_dispatch_model_config:
        editForm.platform === "openai"
          ? messagesDispatchFormStateToConfig({
              allow_messages_dispatch: editForm.allow_messages_dispatch,
              opus_mapped_model: editForm.opus_mapped_model,
              sonnet_mapped_model: editForm.sonnet_mapped_model,
              haiku_mapped_model: editForm.haiku_mapped_model,
              exact_model_mappings: editForm.exact_model_mappings,
            })
          : undefined,
      reasoning_effort_mappings: reasoningEffortMappingsToAPI(
        editForm.reasoning_effort_mappings,
      ),
    };
    delete (payload as Record<string, unknown>).model_mapping_rows;
    // v-model.number 清空输入框时产生 ""，转为 null 让后端设为无限制
    const emptyToNull = (v: any) => (v === "" ? null : v);
    payload.daily_limit_usd = emptyToNull(payload.daily_limit_usd);
    payload.weekly_limit_usd = emptyToNull(payload.weekly_limit_usd);
    payload.monthly_limit_usd = emptyToNull(payload.monthly_limit_usd);
    payload.web_search_price_per_call = emptyToNull(
      payload.web_search_price_per_call,
    );
		payload.search_price_per_1k = emptyToNull(payload.search_price_per_1k);
		payload.audio_realtime_price_per_min = emptyToNull(payload.audio_realtime_price_per_min);
		payload.audio_tts_price_per_million_chars = emptyToNull(payload.audio_tts_price_per_million_chars);
		payload.audio_stt_price_per_hour = emptyToNull(payload.audio_stt_price_per_hour);
		payload.video_model_prices = compactGrokVideoPriceMatrix(editForm.video_model_prices);
    payload.image_rate_multiplier = normalizeImageRateMultiplier(
      payload.image_rate_multiplier,
    );
    payload.video_rate_multiplier = normalizeImageRateMultiplier(
      payload.video_rate_multiplier,
    );
    await adminAPI.groups.updatePricing(
      editingGroup.value.id,
      groupPricingFormToRequest(
        editPricingForm.value,
        editForm.platform,
        editingGroup.value.id,
      ),
    );
    pricingSaved = true;
    await adminAPI.groups.update(editingGroup.value.id, payload);
    appStore.showSuccess(t("admin.groups.groupUpdated"));
    closeEditModal();
    loadGroups();
  } catch (error: any) {
    appStore.showError(
      pricingSaved
        ? // 定价已落库而基础设置被拒时，必须把后端拒绝原因（如分流目标
          // 平台不符）拼进提示，否则用户只看到“请重试”却不知道改什么。
          `${t("admin.groups.pricing.baseSaveFailedAfterPricing")} ${extractApiErrorMessage(error, "")}`.trim()
        : extractApiErrorMessage(error, t("admin.groups.failedToUpdate")),
    );
    console.error("Error updating group:", error);
  } finally {
    submitting.value = false;
  }
};

const addCreateMessagesDispatchMapping = () => {
  createForm.exact_model_mappings.push({ claude_model: "", target_model: "" });
};

const removeCreateMessagesDispatchMapping = (
  row: MessagesDispatchMappingRow,
) => {
  const index = createForm.exact_model_mappings.indexOf(row);
  if (index !== -1) {
    createForm.exact_model_mappings.splice(index, 1);
  }
};

const addEditMessagesDispatchMapping = () => {
  editForm.exact_model_mappings.push({ claude_model: "", target_model: "" });
};

const removeEditMessagesDispatchMapping = (row: MessagesDispatchMappingRow) => {
  const index = editForm.exact_model_mappings.indexOf(row);
  if (index !== -1) {
    editForm.exact_model_mappings.splice(index, 1);
  }
};

const handleRateMultipliers = (group: AdminGroup) => {
  rateMultipliersGroup.value = group;
  showRateMultipliersModal.value = true;
};

const handleBindAccounts = (group: AdminGroup) => {
  bindAccountsGroup.value = group;
  showBindAccountsModal.value = true;
};

const handleRPMOverrides = (group: AdminGroup) => {
  rpmOverridesGroup.value = group;
  showRPMOverridesModal.value = true;
};

const handleCompositeRoutes = (group: AdminGroup) => {
  compositeRoutesGroup.value = group;
  showCompositeRoutesModal.value = true;
};

const handleDuplicate = (group: AdminGroup) => {
  if (duplicatingGroupIds.has(group.id)) return;
  duplicatingGroup.value = group;
  showDuplicateDialog.value = true;
};

const closeDuplicateDialog = () => {
  showDuplicateDialog.value = false;
  duplicatingGroup.value = null;
};

const confirmDuplicate = async () => {
  const source = duplicatingGroup.value;
  if (!source || duplicatingGroupIds.has(source.id)) return;

  showDuplicateDialog.value = false;
  duplicatingGroupIds.add(source.id);
  try {
    const duplicate = await adminAPI.groups.duplicate(source.id);
    appStore.showSuccess(
      t("admin.groups.duplicateSuccess", { name: duplicate.name }),
    );
    await loadGroups();
    // 副本默认停用；成功后直接进入编辑页，引导管理员确认配置后再启用。
    await handleEdit(duplicate);
  } catch (error: unknown) {
    appStore.showError(
      extractApiErrorMessage(error, t("admin.groups.duplicateFailed")),
    );
  } finally {
    duplicatingGroupIds.delete(source.id);
    duplicatingGroup.value = null;
  }
};

const handleDelete = (group: AdminGroup) => {
  deletingGroup.value = group;
  showDeleteDialog.value = true;
};

const confirmDelete = async () => {
  if (!deletingGroup.value) return;

  try {
    await adminAPI.groups.delete(deletingGroup.value.id);
    appStore.showSuccess(t("admin.groups.groupDeleted"));
    showDeleteDialog.value = false;
    deletingGroup.value = null;
    loadGroups();
  } catch (error: any) {
    appStore.showError(
      error.response?.data?.detail || t("admin.groups.failedToDelete"),
    );
    console.error("Error deleting group:", error);
  }
};

// 监听 subscription_type 变化，订阅模式时 is_exclusive 默认为 true
watch(
  () => createForm.subscription_type,
  (newVal) => {
    if (newVal === "subscription") {
      createForm.is_exclusive = true;
      createForm.fallback_group_id_on_invalid_request = null;
    }
  },
);

watch(
  () => createForm.platform,
  (newVal) => {
    if (!["anthropic", "antigravity"].includes(newVal)) {
      createForm.fallback_group_id_on_invalid_request = null;
    }
    if (newVal !== "openai") {
      resetMessagesDispatchFormState(createForm);
      createForm.allow_openai_long_context_billing = false;
    }
    if (!supportsLivePlatform(newVal)) {
      createForm.allow_live = false;
    }
    createForm.max_reasoning_effort = normalizeReasoningEffortForPlatform(
      newVal,
      createForm.max_reasoning_effort,
    );
    createForm.reasoning_effort_mappings = reasoningEffortMappingsToRows(
      reasoningEffortMappingsToAPI(createForm.reasoning_effort_mappings),
      newVal,
    );
    createReasoningEffortPolicyRef.value?.resetValidation();
    if (!["openai", "antigravity", "anthropic", "gemini"].includes(newVal)) {
      createForm.require_oauth_only = false;
      createForm.require_privacy_set = false;
    }
    resetModelsListState(createModelsListState);
    loadModelsListCandidates("create", 0, newVal);
  },
);

watch(
  () => editForm.platform,
  (newVal) => {
    if (!["anthropic", "antigravity"].includes(newVal)) {
      editForm.fallback_group_id_on_invalid_request = null;
    }
    if (newVal !== "openai") {
      resetMessagesDispatchFormState(editForm);
      editForm.allow_openai_long_context_billing = false;
    }
    if (!supportsLivePlatform(newVal)) {
      editForm.allow_live = false;
    }
    editForm.max_reasoning_effort = normalizeReasoningEffortForPlatform(
      newVal,
      editForm.max_reasoning_effort,
    );
    editForm.reasoning_effort_mappings = reasoningEffortMappingsToRows(
      reasoningEffortMappingsToAPI(editForm.reasoning_effort_mappings),
      newVal,
    );
    editReasoningEffortPolicyRef.value?.resetValidation();
    if (!["openai", "antigravity", "anthropic", "gemini"].includes(newVal)) {
      editForm.require_oauth_only = false;
      editForm.require_privacy_set = false;
    }
    if (editingGroup.value) {
      resetModelsListState(editModelsListState, editForm.platform === editingGroup.value.platform ? editingGroup.value.models_list_config : undefined);
      loadModelsListCandidates("edit", editingGroup.value.id, newVal);
    }
  },
);

watch(
  () => createOpenAILongContextBillingSummary.value.canEnable,
  (canEnable) => {
    if (!canEnable) {
      createForm.allow_openai_long_context_billing = false;
    }
  },
);

watch(
  () => editOpenAILongContextBillingSummary.value.canEnable,
  (canEnable) => {
    if (!canEnable) {
      editForm.allow_openai_long_context_billing = false;
    }
  },
);

watch(
  () => editForm.platform,
  (newVal) => {
    if (!['anthropic', 'antigravity'].includes(newVal)) {
      editForm.fallback_group_id_on_invalid_request = null
    }
    if (newVal !== 'openai') {
      editForm.allow_messages_dispatch = false
      editForm.default_mapped_model = ''
    }
  }
)

// 点击外部关闭账号搜索下拉框
const handleClickOutside = (event: MouseEvent) => {
  const target = event.target as HTMLElement;
  if (
    columnSettingsDropdownRef.value &&
    !columnSettingsDropdownRef.value.contains(target)
  ) {
    showColumnSettingsDropdown.value = false;
  }
  // 检查是否点击在下拉框或输入框内
  if (!target.closest(".account-search-container")) {
    Object.keys(showAccountDropdown.value).forEach((key) => {
      showAccountDropdown.value[key] = false;
    });
  }
};

// 打开排序弹窗
const openSortModal = async () => {
  try {
    // 获取所有分组（不分页）
    const allGroups = await adminAPI.groups.getAll();
    // 按 sort_order 排序
    sortableGroups.value = [...allGroups].sort(
      (a, b) => a.sort_order - b.sort_order,
    );
    showSortModal.value = true;
  } catch (error) {
    appStore.showError(t("admin.groups.failedToLoad"));
    console.error("Error loading groups for sorting:", error);
  }
};

// 关闭排序弹窗
const closeSortModal = () => {
  showSortModal.value = false;
  sortableGroups.value = [];
};

// 保存排序
const saveSortOrder = async () => {
  sortSubmitting.value = true;
  try {
    const updates = sortableGroups.value.map((g, index) => ({
      id: g.id,
      sort_order: index * 10,
    }));
    await adminAPI.groups.updateSortOrder(updates);
    appStore.showSuccess(t("admin.groups.sortOrderUpdated"));
    closeSortModal();
    loadGroups();
  } catch (error: any) {
    appStore.showError(
      error.response?.data?.detail || t("admin.groups.failedToUpdateSortOrder"),
    );
    console.error("Error updating sort order:", error);
  } finally {
    sortSubmitting.value = false;
  }
};

onMounted(async () => {
  loadSavedColumns();
  const runtimeSummaryPromise = getGatewayRuntimeSummary()
    .then((runtimeSummary) => {
      compositeGroupsEnabled.value = runtimeSummary.composite_groups_enabled === true;
    })
    .catch((error) => {
      console.error("Error loading composite group runtime switch:", error);
    });
  await Promise.allSettled([loadGroups(), runtimeSummaryPromise]);
  capacityRefreshTimer = setInterval(() => {
    if (!loading.value) {
      loadCapacitySummary();
    }
  }, 5000);
  loadModelsListCandidates("create", 0, createForm.platform);
  document.addEventListener("click", handleClickOutside);
});

onUnmounted(() => {
  document.removeEventListener("click", handleClickOutside);
  if (capacityRefreshTimer) {
    clearInterval(capacityRefreshTimer);
    capacityRefreshTimer = null;
  }
  accountSearchRunner.clearAll();
  clearAllAccountSearchState();
});
</script>
