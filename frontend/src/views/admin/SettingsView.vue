<template>
  <AppLayout>
    <div class="mx-auto max-w-6xl space-y-6">
      <div v-if="loading" class="flex items-center justify-center py-12">
        <div class="h-8 w-8 animate-spin rounded-full border-b-2 border-primary-600"></div>
      </div>

      <form v-else class="space-y-6" novalidate @submit.prevent="saveSettings">
        <div class="settings-tabs-shell">
          <nav class="settings-tabs-scroll" role="tablist" aria-label="Gateway settings">
            <div class="settings-tabs">
              <button
                v-for="tab in settingsTabs"
                :id="`settings-tab-${tab.key}`"
                :key="tab.key"
                type="button"
                role="tab"
                :aria-selected="activeTab === tab.key"
                :tabindex="activeTab === tab.key ? 0 : -1"
                :class="['settings-tab', activeTab === tab.key && 'settings-tab-active']"
                @click="selectSettingsTab(tab.key)"
                @keydown="handleSettingsTabKeydown($event, tab.key)"
              >
                <span class="settings-tab-icon">
                  <Icon :name="tab.icon" size="sm" />
                </span>
                <span class="settings-tab-label">{{ tab.label }}</span>
              </button>
            </div>
          </nav>
        </div>

        <div v-show="activeTab === 'general'" class="space-y-6">
          <section class="card">
            <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
              <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
                {{ localText("基础信息", "General") }}
              </h2>
              <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
                {{ localText("只保留网关后台需要展示和复用的站点配置。", "Gateway admin display settings.") }}
              </p>
            </div>
            <div class="grid gap-5 p-6 md:grid-cols-2">
              <label class="field">
                <span>{{ localText("站点名称", "Site name") }}</span>
                <input v-model.trim="form.site_name" class="input" placeholder="JnmGatewayApi" />
              </label>
              <label class="field">
                <span>{{ localText("站点副标题", "Site subtitle") }}</span>
                <input v-model.trim="form.site_subtitle" class="input" />
              </label>
              <label class="field">
                <span>{{ localText("API 基础地址", "API base URL") }}</span>
                <input v-model.trim="form.api_base_url" class="input" placeholder="https://api.example.com" />
              </label>
              <label class="field">
                <span>{{ localText("文档地址", "Documentation URL") }}</span>
                <input v-model.trim="form.doc_url" class="input" placeholder="https://docs.example.com" />
              </label>
              <label class="field md:col-span-2">
                <span>{{ localText("Logo 地址", "Logo URL") }}</span>
                <input v-model.trim="form.site_logo" class="input" />
              </label>
              <label class="field md:col-span-2">
                <span>{{ localText("联系方式", "Contact information") }}</span>
                <textarea v-model.trim="form.contact_info" class="textarea min-h-[88px]" />
              </label>
            </div>
          </section>

          <section class="card">
            <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
              <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
                {{ localText("后台体验", "Admin experience") }}
              </h2>
              <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
                {{ localText("保留表格和后台模式相关设置，不再展示客户侧首页、自定义菜单和注册入口。", "Gateway admin table and backend mode settings.") }}
              </p>
            </div>
            <div class="space-y-5 p-6">
              <div class="setting-row">
                <div>
                  <h3>{{ localText("后台模式", "Backend mode") }}</h3>
                  <p>{{ localText("启用后以后台管理体验为主。", "Use admin-first behavior.") }}</p>
                </div>
                <Toggle v-model="form.backend_mode_enabled" />
              </div>
              <div class="setting-row">
                <div>
                  <h3>{{ localText("隐藏 CCS 导入按钮", "Hide CCS import button") }}</h3>
                  <p>{{ localText("减少轻量网关后台里不需要的导入入口。", "Remove unused import entry from gateway admin.") }}</p>
                </div>
                <Toggle v-model="form.hide_ccs_import_button" />
              </div>
              <div class="grid gap-5 md:grid-cols-2">
                <label class="field">
                  <span>{{ localText("默认分页大小", "Default page size") }}</span>
                  <input
                    v-model.number="form.table_default_page_size"
                    class="input"
                    type="number"
                    min="5"
                    max="1000"
                  />
                </label>
                <label class="field">
                  <span>{{ localText("分页选项", "Page size options") }}</span>
                  <input v-model.trim="tablePageSizeOptionsInput" class="input" placeholder="10, 20, 50, 100" />
                </label>
              </div>
            </div>
          </section>
        </div>

        <div v-show="activeTab === 'features'" class="space-y-6">
          <section class="card">
            <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
              <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
                {{ localText("网关功能开关", "Gateway feature switches") }}
              </h2>
              <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
                {{ localText("这里只保留网关真实需要的运行开关。", "Only runtime switches used by gateway-lite are kept.") }}
              </p>
            </div>
            <div class="space-y-5 p-6">
              <div class="setting-row" data-testid="composite-groups-switch-row">
                <div>
                  <h3>{{ localText("聚合分组（多平台）", "Composite groups (multi-platform)") }}</h3>
                  <p v-if="compositeGroupsLoading">
                    {{ localText("正在读取当前运行状态。", "Loading the current runtime state.") }}
                  </p>
                  <p v-else-if="!compositeGroupsLoaded">
                    {{ localText("读取失败，请刷新后重试。", "Loading failed. Refresh to retry.") }}
                  </p>
                  <p v-else-if="compositeGroupsSaving">
                    {{ localText("正在保存并应用到当前网关。", "Saving and applying to this gateway.") }}
                  </p>
                  <p v-else-if="compositeGroupsForm.enabled">
                    {{ localText("已允许创建聚合分组，并可按模型路由到不同平台账号。", "Composite groups can be created and models can route to accounts on different platforms.") }}
                  </p>
                  <p v-else>
                    {{ localText("关闭时保留已有配置，但已有聚合 Key 的新请求会返回 503。", "Existing configuration is kept while disabled, but new requests using composite keys return 503.") }}
                  </p>
                </div>
                <Toggle
                  data-testid="composite-groups-switch"
                  :model-value="compositeGroupsForm.enabled"
                  :disabled="!compositeGroupsLoaded || compositeGroupsSaving"
                  :class="{ 'cursor-not-allowed opacity-50': !compositeGroupsLoaded || compositeGroupsSaving }"
                  :aria-disabled="!compositeGroupsLoaded || compositeGroupsSaving"
                  @update:model-value="saveCompositeGroupsSwitch"
                />
              </div>
              <div class="setting-row" data-testid="codex-tail-burst-switch-row">
                <div>
                  <h3>{{ localText("Codex 超额（Tail-Burst）", "Codex tail-burst") }}</h3>
                  <p v-if="codexTailBurstLoading">
                    {{ localText("正在读取当前运行状态。", "Loading the current runtime state.") }}
                  </p>
                  <p v-else-if="!codexTailBurstLoaded">
                    {{ localText("读取失败，请刷新后重试。", "Loading failed. Refresh to retry.") }}
                  </p>
                  <p v-else-if="codexTailBurstSaving">
                    {{ localText("正在保存并应用到当前网关。", "Saving and applying to this gateway.") }}
                  </p>
                  <p v-else-if="codexTailBurstForm.enabled">
                    {{ localText("账号用量贴近窗口上限或窗口/订阅即将重置时，跳过本地预防性停调，继续承接请求直到上游返回权威配额拒绝才真正冻结。", "When an account is near the window cap or its window/subscription is about to reset, skip the local preventive pause and keep serving until the upstream returns an authoritative quota rejection.") }}
                  </p>
                  <p v-else>
                    {{ localText("关闭时行为与普通配额阈值一致：达到阈值即停调，不做尾部放行。", "When disabled, behavior matches ordinary quota thresholds: pause at the threshold, no tail burst.") }}
                  </p>
                </div>
                <Toggle
                  data-testid="codex-tail-burst-switch"
                  :model-value="codexTailBurstForm.enabled"
                  :disabled="!codexTailBurstLoaded || codexTailBurstSaving"
                  :class="{ 'cursor-not-allowed opacity-50': !codexTailBurstLoaded || codexTailBurstSaving }"
                  :aria-disabled="!codexTailBurstLoaded || codexTailBurstSaving"
                  @update:model-value="saveCodexTailBurstSwitch"
                />
              </div>
              <div class="setting-row">
                <div>
                  <h3>{{ localText("渠道监控", "Channel monitor") }}</h3>
                  <p>{{ localText("可选择主动探测，或根据真实网关流量被动统计。", "Choose active probes or passive statistics from real gateway traffic.") }}</p>
                </div>
                <Toggle v-model="form.channel_monitor_enabled" />
              </div>
              <template v-if="form.channel_monitor_enabled">
                <div class="grid gap-3 md:grid-cols-2">
                  <button
                    type="button"
                    class="rounded-xl border p-4 text-left transition-colors"
                    :class="form.channel_monitor_mode === 'v2' ? 'border-primary-500 bg-primary-50 text-primary-800 dark:bg-primary-500/10 dark:text-primary-200' : 'border-gray-200 bg-white text-gray-700 hover:border-primary-300 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-300'"
                    @click="form.channel_monitor_mode = 'v2'"
                  >
                    <strong class="block text-sm">{{ localText("V2 被动统计（推荐）", "V2 passive statistics (recommended)") }}</strong>
                    <span class="mt-1 block text-xs opacity-75">{{ localText("只聚合真实请求，不额外向上游发送探活请求。", "Aggregates real traffic without sending extra probes upstream.") }}</span>
                  </button>
                  <button
                    type="button"
                    class="rounded-xl border p-4 text-left transition-colors"
                    :class="form.channel_monitor_mode === 'v1' ? 'border-primary-500 bg-primary-50 text-primary-800 dark:bg-primary-500/10 dark:text-primary-200' : 'border-gray-200 bg-white text-gray-700 hover:border-primary-300 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-300'"
                    @click="form.channel_monitor_mode = 'v1'"
                  >
                    <strong class="block text-sm">{{ localText("V1 主动探测", "V1 active probes") }}</strong>
                    <span class="mt-1 block text-xs opacity-75">{{ localText("按计划向上游发送小型测活请求。", "Sends small scheduled health-check requests upstream.") }}</span>
                  </button>
                </div>
                <label v-if="form.channel_monitor_mode === 'v1'" class="field max-w-xs">
                  <span>{{ localText("默认监控间隔（秒）", "Default interval seconds") }}</span>
                  <input
                    v-model.number="form.channel_monitor_default_interval_seconds"
                    class="input"
                    type="number"
                    min="10"
                    max="86400"
                  />
                </label>
                <div v-if="form.channel_monitor_mode === 'v2'" class="setting-row">
                  <div>
                    <h3>{{ localText("向普通用户隐藏 RPM/TPM", "Hide RPM/TPM from regular users") }}</h3>
                    <p>{{ localText("管理员始终可以查看吞吐数据。", "Administrators can always view throughput metrics.") }}</p>
                  </div>
                  <Toggle v-model="form.channel_monitor_hide_throughput" />
                </div>
              </template>
              <div class="setting-row" data-testid="billing-settlement-switch-row">
                <div>
                  <h3>{{ localText("防重复结算（推荐）", "Duplicate-safe settlement (recommended)") }}</h3>
                  <p v-if="billingSettlementLoading">
                    {{ localText("正在读取当前结算模式。", "Loading the current settlement mode.") }}
                  </p>
                  <p v-else-if="!billingSettlementLoaded">
                    {{ localText("读取失败，请刷新页面后重试。", "Loading failed. Refresh the page to retry.") }}
                  </p>
                  <p v-else-if="billingSettlementSaving">
                    {{ localText("正在切换结算模式。", "Switching settlement mode.") }}
                  </p>
                  <p v-else-if="billingSettlementForm.idempotent_enabled">
                    {{ localText("同一请求即使重复回调，也只推进一次不足一美分累计。", "Repeated callbacks for the same request advance the sub-cent balance only once.") }}
                  </p>
                  <p v-else>
                    {{ localText("使用旧版累计逻辑，仅建议用于临时回退。", "Uses legacy accumulation and is intended only for temporary rollback.") }}
                  </p>
                </div>
                <Toggle
                  data-testid="billing-settlement-switch"
                  :model-value="billingSettlementForm.idempotent_enabled"
                  :class="{ 'pointer-events-none opacity-50': !billingSettlementLoaded || billingSettlementSaving }"
                  :aria-disabled="!billingSettlementLoaded || billingSettlementSaving"
                  :tabindex="!billingSettlementLoaded || billingSettlementSaving ? -1 : 0"
                  @update:model-value="saveBillingSettlementSwitch"
                />
              </div>
              <div
                v-if="billingSettlementLoaded && !billingSettlementForm.idempotent_enabled && billingSettlementForm.legacy_warning"
                data-testid="billing-settlement-legacy-warning"
                class="rounded-lg border border-amber-200 bg-amber-50 p-4 text-sm text-amber-700 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-300"
              >
                {{ localText("当前为旧版结算：同一请求重复进入结算时，可能再次累计不足一美分余额并造成多扣。", "Legacy settlement is active: retrying the same request may accumulate its sub-cent balance again and overcharge.") }}
              </div>
              <div class="setting-row">
                <div>
                  <h3>{{ localText("风控中心", "Risk control") }}</h3>
                  <p>{{ localText("启用请求拦截、限流和安全策略配置。", "Enable request blocking and risk policies.") }}</p>
                </div>
                <Toggle v-model="form.risk_control_enabled" />
              </div>
              <div class="setting-row">
                <div>
                  <h3>{{ localText("会话阻断", "Cyber session block") }}</h3>
                  <p>{{ localText("对命中风控的会话设置临时阻断时间。", "Temporarily block sessions matched by risk rules.") }}</p>
                </div>
                <Toggle v-model="form.cyber_session_block_enabled" />
              </div>
              <div class="setting-row">
                <div>
                  <h3>{{ localText("ChatGPT Live（实时会话）", "ChatGPT Live") }}</h3>
                  <p>{{ localText("节点总开关；OpenAI 分组还需单独允许。Linux 无设备证明时会安全拒绝。", "Node-wide gate; the OpenAI group must also opt in. Linux safely rejects when device attestation is unavailable.") }}</p>
                </div>
                <Toggle v-model="gatewayLiteConfigForm.allow_live" @update:model-value="saveGatewayLiteLiveSwitch" />
              </div>
              <label v-if="form.cyber_session_block_enabled" class="field max-w-xs">
                <span>{{ localText("阻断时长（秒）", "Block TTL seconds") }}</span>
                <input
                  v-model.number="form.cyber_session_block_ttl_seconds"
                  class="input"
                  type="number"
                  min="60"
                  max="604800"
                />
              </label>
            </div>
          </section>
        </div>

        <div v-show="activeTab === 'controlPlane'" class="space-y-6">
          <section class="card">
            <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
              <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
                {{ localText("主站连接", "Control plane") }}
              </h2>
              <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
                {{ localText("保存后会写入 config.yaml，并立即应用到当前运行中的轻量网关进程。", "Saving writes config.yaml and applies the settings to the running gateway-lite process immediately.") }}
              </p>
            </div>
            <div class="space-y-5 p-6">
              <div v-if="gatewayLiteConfigLoading" class="inline-loading">
                <span class="spinner-sm"></span>
                {{ localText("正在读取主站配置", "Loading control plane config") }}
              </div>
              <template v-else>
                <div class="grid gap-5 md:grid-cols-2">
                  <label class="field">
                    <span>{{ localText("主站 API 地址", "Control plane URL") }}</span>
                    <input v-model.trim="gatewayLiteConfigForm.control_plane_url" class="input" placeholder="http://127.0.0.1:8088" />
                  </label>
                  <label class="field">
                    <span>{{ localText("主站内部令牌", "Control plane token") }}</span>
                    <input
                      v-model.trim="gatewayLiteConfigForm.control_plane_token"
                      class="input"
                      type="password"
                      :placeholder="gatewayLiteConfigForm.control_plane_token_configured ? localText('已配置，留空表示不修改', 'Configured; leave blank to keep') : localText('未配置', 'Not configured')"
                    />
                  </label>
                  <label class="field">
                    <span>{{ localText("网关编号", "Gateway code") }}</span>
                    <input v-model.trim="gatewayLiteConfigForm.gateway_code" class="input" placeholder="openai-hk-t1" />
                  </label>
                  <label class="field">
                    <span>{{ localText("Redis 前缀", "Redis prefix") }}</span>
                    <input v-model.trim="gatewayLiteConfigForm.redis_prefix" class="input" placeholder="jnm:gateway-lite" />
                  </label>
                  <label class="field">
                    <span>{{ localText("主站请求超时（毫秒）", "Timeout milliseconds") }}</span>
                    <input v-model.number="gatewayLiteConfigForm.control_plane_timeout_ms" class="input" type="number" min="100" max="30000" />
                  </label>
                  <label class="field">
                    <span>{{ localText("运行心跳间隔（秒）", "Runtime heartbeat seconds") }}</span>
                    <input v-model.number="gatewayLiteConfigForm.runtime_health_interval_seconds" class="input" type="number" min="5" max="3600" />
                  </label>
                  <label class="field">
                    <span>{{ localText("活跃窗口（秒）", "Active window seconds") }}</span>
                    <input v-model.number="gatewayLiteConfigForm.runtime_active_window_seconds" class="input" type="number" min="30" max="86400" />
                  </label>
                  <label class="field">
                    <span>{{ localText("主站配置同步间隔（秒）", "Config sync seconds") }}</span>
                    <input v-model.number="gatewayLiteConfigForm.config_sync_interval_seconds" class="input" type="number" min="5" max="3600" />
                  </label>
                  <label class="field">
                    <span>{{ localText("缓存失效同步间隔（秒）", "Cache invalidation seconds") }}</span>
                    <input v-model.number="gatewayLiteConfigForm.cache_invalidation_interval_seconds" class="input" type="number" min="1" max="3600" />
                  </label>
                  <label class="field">
                    <span>{{ localText("用量队列积压告警阈值", "Pending queue alert threshold") }}</span>
                    <input v-model.number="gatewayLiteConfigForm.usage_queue_pending_alert_threshold" class="input" type="number" min="1" />
                  </label>
                  <label class="field">
                    <span>{{ localText("死信队列告警阈值", "Dead queue alert threshold") }}</span>
                    <input v-model.number="gatewayLiteConfigForm.usage_queue_dead_alert_threshold" class="input" type="number" min="1" />
                  </label>
                </div>
                <div
                  class="rounded-lg border p-4 text-sm"
                  :class="gatewayLiteConfigForm.restart_required
                    ? 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-300'
                    : 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-800 dark:bg-emerald-900/20 dark:text-emerald-300'"
                >
                  {{ localText("配置文件：", "Config file:") }}{{ gatewayLiteConfigForm.config_path || "config.yaml" }}。
                  <span v-if="gatewayLiteConfigForm.restart_required">
                    {{ localText("配置已保存，当前版本会尽量热更新；如遇旧队列或旧缓存残留，可手动同步一次。", "Config saved and hot-applied where possible; manually sync once if old queue or cache state remains.") }}
                  </span>
                  <span v-else>
                    {{ localText("所有主站连接配置已即时生效，并已触发一次配置同步。", "All control-plane settings applied immediately and triggered a config sync.") }}
                  </span>
                </div>
                <div class="flex justify-end">
                  <button type="button" class="btn btn-secondary btn-sm" :disabled="gatewayLiteConfigSaving" @click="saveGatewayLiteConfig">
                    <span v-if="gatewayLiteConfigSaving" class="spinner-sm"></span>
                    <Icon v-else name="checkCircle" size="xs" />
                    {{ gatewayLiteConfigSaving ? localText("保存中", "Saving") : localText("保存主站配置", "Save control plane config") }}
                  </button>
                </div>
              </template>
            </div>
          </section>
        </div>

        <div v-show="activeTab === 'security'" class="space-y-6">
          <ProfileTotpCard />

          <section class="card">
            <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
              <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
                {{ localText("私有登录入口", "Private login entry") }}
              </h2>
              <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
                {{ localText("只有正确入口会显示管理员登录表单，其他地址统一显示 404。", "Only the configured entry renders the administrator login form; other paths show 404.") }}
              </p>
            </div>
            <div class="space-y-5 p-6">
              <div v-if="gatewayLiteConfigLoading" class="inline-loading">
                <span class="spinner-sm"></span>
                {{ localText("正在读取登录配置", "Loading login settings") }}
              </div>
              <template v-else>
                <div class="grid gap-5 md:grid-cols-2">
                  <label class="field">
                    <span>{{ localText("私有入口路径", "Private entry path") }}</span>
                    <input v-model.trim="gatewayLiteConfigForm.admin_login_path" class="input font-mono" placeholder="/gateway-admin" autocomplete="off" />
                  </label>
                  <label class="field">
                    <span>{{ localText("登录标题", "Login title") }}</span>
                    <input v-model.trim="gatewayLiteConfigForm.login_title" class="input" maxlength="100" />
                  </label>
                  <label class="field md:col-span-2">
                    <span>{{ localText("登录说明", "Login subtitle") }}</span>
                    <input v-model.trim="gatewayLiteConfigForm.login_subtitle" class="input" maxlength="180" />
                  </label>
                </div>
                <div class="rounded-lg border border-amber-200 bg-amber-50 p-4 text-sm text-amber-700 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-300">
                  {{ localText("修改入口后旧地址立即失效。请保存新地址；私有路径只用于防止误入，不能替代强密码、人机验证和双因素认证。", "The old entry stops working immediately after a change. Keep the new address; a private path does not replace a strong password, human verification, or two-factor authentication.") }}
                </div>
                <div class="flex justify-end">
                  <button type="button" class="btn btn-primary btn-sm" :disabled="gatewayLiteConfigSaving" @click="saveGatewayLiteLoginConfig">
                    <span v-if="gatewayLiteConfigSaving" class="spinner-sm"></span>
                    {{ gatewayLiteConfigSaving ? localText("保存中", "Saving") : localText("保存登录入口", "Save login entry") }}
                  </button>
                </div>
              </template>
            </div>
          </section>

          <section class="card">
            <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
              <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
                {{ localText("修改管理员密码", "Change administrator password") }}
              </h2>
              <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
                {{ localText("修改成功后所有旧登录令牌立即失效，并返回私有登录入口。", "All existing login tokens are revoked after a successful change, then the private login entry is reopened.") }}
              </p>
            </div>
            <form class="space-y-5 p-6" @submit.prevent="changeAdminPassword">
              <div class="grid gap-5 md:grid-cols-3">
                <label class="field">
                  <span>{{ localText("当前密码", "Current password") }}</span>
                  <input v-model="passwordForm.old_password" class="input" type="password" autocomplete="current-password" required />
                </label>
                <label class="field">
                  <span>{{ localText("新密码", "New password") }}</span>
                  <input v-model="passwordForm.new_password" class="input" type="password" autocomplete="new-password" minlength="8" required />
                </label>
                <label class="field">
                  <span>{{ localText("确认新密码", "Confirm new password") }}</span>
                  <input v-model="passwordForm.confirm_password" class="input" type="password" autocomplete="new-password" minlength="8" required />
                </label>
              </div>
              <div class="flex justify-end">
                <button type="submit" class="btn btn-primary btn-sm" :disabled="passwordChanging">
                  <span v-if="passwordChanging" class="spinner-sm"></span>
                  {{ passwordChanging ? localText("修改中", "Changing") : localText("修改密码", "Change password") }}
                </button>
              </div>
            </form>
          </section>

          <section class="card">
            <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
              <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
                {{ localText("管理员 API Key", "Admin API Key") }}
              </h2>
              <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
                {{ localText("用于脚本或运维工具调用后台管理接口。", "Used by admin scripts and operation tooling.") }}
              </p>
            </div>
            <div class="space-y-4 p-6">
              <div class="rounded-lg border border-blue-200 bg-blue-50 p-4 dark:border-blue-800 dark:bg-blue-900/20">
                <div class="flex items-start gap-3">
                  <Icon name="infoCircle" size="md" class="mt-0.5 flex-shrink-0 text-blue-500" />
                  <p class="text-sm text-blue-700 dark:text-blue-300">
                    {{ localText("密钥默认隐藏，可随时查看或复制。", "The key is hidden by default and can be viewed or copied at any time.") }}
                  </p>
                </div>
              </div>
              <div v-if="adminApiKeyLoading" class="inline-loading">
                <span class="spinner-sm"></span>
                {{ localText("正在读取密钥状态", "Loading key status") }}
              </div>
              <div v-else-if="!adminApiKeyExists" class="flex items-center justify-between gap-4">
                <span class="text-sm text-gray-500 dark:text-gray-400">
                  {{ localText("当前未配置管理员 API Key。", "No admin API key configured.") }}
                </span>
                <button type="button" class="btn btn-primary btn-sm" :disabled="adminApiKeyOperating" @click="createAdminApiKey">
                  {{ adminApiKeyOperating ? localText("生成中", "Creating") : localText("生成密钥", "Create key") }}
                </button>
              </div>
              <div v-else class="space-y-4">
                <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                  <code data-testid="admin-api-key-value" class="min-w-0 select-all break-all rounded bg-gray-100 px-2 py-1 font-mono text-sm text-gray-900 dark:bg-dark-700 dark:text-gray-100">
                    {{ adminApiKeyVisible ? adminApiKeyPlaintext : adminApiKeyMasked }}
                  </code>
                  <div class="flex flex-wrap gap-2">
                    <button data-testid="admin-api-key-reveal" type="button" class="btn btn-secondary btn-sm" :disabled="adminApiKeyOperating" @click="toggleAdminApiKeyVisibility">
                      <Icon :name="adminApiKeyVisible ? 'eyeOff' : 'eye'" size="xs" />
                      {{ adminApiKeyVisible ? localText("隐藏", "Hide") : localText("查看", "View") }}
                    </button>
                    <button data-testid="admin-api-key-copy" type="button" class="btn btn-secondary btn-sm" :disabled="adminApiKeyOperating" @click="copyAdminApiKey">
                      <Icon name="copy" size="xs" />
                      {{ localText("复制", "Copy") }}
                    </button>
                    <button type="button" class="btn btn-secondary btn-sm" :disabled="adminApiKeyOperating" @click="regenerateAdminApiKey">
                      {{ localText("重新生成", "Regenerate") }}
                    </button>
                    <button
                      type="button"
                      class="btn btn-secondary btn-sm text-red-600 hover:text-red-700 dark:text-red-400"
                      :disabled="adminApiKeyOperating"
                      @click="deleteAdminApiKey"
                    >
                      {{ localText("删除", "Delete") }}
                    </button>
                  </div>
                </div>
              </div>
            </div>
          </section>

          <section class="card">
            <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
              <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
                {{ localText("访问安全", "Access security") }}
              </h2>
              <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
                {{ localText("保留管理员登录和 API Key 鉴权相关安全项。", "Security settings used by admin login and API key access.") }}
              </p>
            </div>
            <div class="space-y-5 p-6">
              <div class="setting-row">
                <div>
                  <h3>{{ localText("双因素认证功能", "Two-factor authentication") }}</h3>
                  <p>{{ localText("允许管理员启用 TOTP，并用于导出、备份、秘密配置和权限提升等敏感操作的现场二次验证。", "Allow administrators to enable TOTP for one-time verification of exports, backups, secret settings, and role promotion.") }}</p>
                </div>
                <Toggle v-model="form.totp_enabled" />
              </div>
              <div class="setting-row">
                <div>
                  <h3>{{ localText("Cloudflare Turnstile 人机验证", "Cloudflare Turnstile") }}</h3>
                  <p>{{ localText("用于登录页的人机验证。", "Human verification on the login page.") }}</p>
                </div>
                <Toggle v-model="form.turnstile_enabled" />
              </div>
              <div v-if="form.turnstile_enabled" class="grid gap-5 md:grid-cols-2">
                <label class="field">
                  <span>{{ localText("Site Key（站点密钥）", "Site key") }}</span>
                  <input v-model.trim="form.turnstile_site_key" class="input" />
                </label>
                <label class="field">
                  <span>{{ localText("Secret Key（私密密钥）", "Secret key") }}</span>
                  <input
                    v-model.trim="form.turnstile_secret_key"
                    class="input"
                    type="password"
                    :placeholder="form.turnstile_secret_key_configured ? localText('已配置，留空表示不修改', 'Configured. Leave blank to keep it.') : ''"
                  />
                </label>
              </div>
              <div class="setting-row">
                <div>
                  <h3>{{ localText("信任转发 IP", "Trust forwarded IP") }}</h3>
                  <p>{{ localText("网关前有可信反向代理时，允许 API Key ACL、后台会话绑定和操作审计使用转发来源 IP；修改后已登录管理员需要重新登录。", "Allow API key ACL, browser-session binding, and operation audit to use forwarded client IP behind a trusted proxy; signed-in administrators must log in again after changing it.") }}</p>
                </div>
                <Toggle v-model="form.api_key_acl_trust_forwarded_ip" />
              </div>
              <div
                v-if="form.api_key_acl_trust_forwarded_ip"
                class="rounded-xl border border-gray-200 bg-gray-50/70 p-4 dark:border-dark-700 dark:bg-dark-800/60"
              >
                <div class="flex flex-wrap items-start justify-between gap-3">
                  <div>
                    <h3 class="text-sm font-medium text-gray-900 dark:text-white">
                      {{ localText("客户端 IP 请求头", "Client IP headers") }}
                    </h3>
                    <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                      {{ localText("先点常用代理；只有直接连接来源命中 server.trusted_proxies 时才读取这些请求头。", "Choose common proxies first. These headers are read only when the direct peer matches server.trusted_proxies.") }}
                    </p>
                  </div>
                  <span class="rounded-full bg-gray-200 px-2.5 py-1 text-xs text-gray-600 dark:bg-dark-700 dark:text-gray-300">
                    {{ form.forwarded_client_ip_headers.length }}/16
                  </span>
                </div>
                <div class="mt-4 flex flex-wrap gap-2">
                  <button
                    v-for="header in forwardedIPHeaderPresets"
                    :key="header"
                    type="button"
                    class="rounded-full border px-3 py-1.5 text-xs font-medium transition-colors"
                    :class="hasForwardedIPHeader(header)
                      ? 'border-primary-500 bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300'
                      : 'border-gray-200 bg-white text-gray-600 hover:border-primary-300 dark:border-dark-600 dark:bg-dark-900 dark:text-gray-300'"
                    @click="toggleForwardedIPHeader(header)"
                  >
                    {{ header }}
                  </button>
                </div>
                <div v-if="form.forwarded_client_ip_headers.length" class="mt-3 flex flex-wrap gap-2">
                  <button
                    v-for="header in form.forwarded_client_ip_headers"
                    :key="`selected-${header}`"
                    type="button"
                    class="inline-flex items-center gap-1 rounded-md bg-white px-2.5 py-1.5 font-mono text-xs text-gray-700 ring-1 ring-gray-200 hover:text-red-600 dark:bg-dark-900 dark:text-gray-200 dark:ring-dark-600"
                    :aria-label="localText(`删除 ${header}`, `Remove ${header}`)"
                    @click="removeForwardedIPHeader(header)"
                  >
                    {{ header }} <span aria-hidden="true">×</span>
                  </button>
                </div>
                <div class="mt-4 flex max-w-xl gap-2">
                  <input
                    v-model.trim="customForwardedIPHeader"
                    class="input font-mono"
                    :placeholder="localText('其他代理请求头（可选）', 'Other proxy header (optional)')"
                    maxlength="80"
                    @keydown.enter.prevent="addCustomForwardedIPHeader"
                  />
                  <button type="button" class="btn btn-secondary btn-sm shrink-0" @click="addCustomForwardedIPHeader">
                    {{ localText("添加", "Add") }}
                  </button>
                </div>
              </div>
            </div>
          </section>
        </div>

        <div v-show="activeTab === 'gateway'" class="space-y-6">
          <section class="card">
            <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
              <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
                {{ localText("网关转发策略", "Gateway forwarding") }}
              </h2>
              <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
                {{ localText("保留模型兜底、身份补丁、缓存控制和客户端兼容策略。", "Fallback, identity patch, cache and client compatibility policies.") }}
              </p>
            </div>
            <div class="space-y-6 p-6">
              <label class="field">
                <span>{{ localText("客户端统一错误文案", "Custom client error message") }}</span>
                <textarea
                  v-model="form.gateway_custom_error_message"
                  class="textarea min-h-[112px]"
                  maxlength="500"
                  :placeholder="localText('留空时返回真实错误；填写后，客户看到的错误说明统一替换为此文案。', 'Leave blank to return the original error. When set, clients see this text for gateway errors.')"
                />
                <small class="flex items-center justify-between gap-3">
                  <span>{{ localText("只替换说明文字，HTTP 状态码和错误代码保持真实，内部日志仍记录原始原因。", "Only the human-readable text changes. HTTP status, error code and internal logs remain original.") }}</span>
                  <span class="shrink-0 font-mono">{{ Array.from(form.gateway_custom_error_message).length }}/500</span>
                </small>
              </label>

              <div class="setting-row">
                <div>
                  <h3>{{ localText("启用模型兜底", "Enable model fallback") }}</h3>
                  <p>{{ localText("当请求模型需要重写时使用对应平台兜底模型。", "Use platform fallback models when rewrite is needed.") }}</p>
                </div>
                <Toggle v-model="form.enable_model_fallback" />
              </div>
              <div v-if="form.enable_model_fallback" class="grid gap-5 md:grid-cols-2">
                <label class="field">
                  <span>Anthropic fallback（Anthropic 兜底模型）</span>
                  <input v-model.trim="form.fallback_model_anthropic" class="input" />
                </label>
                <label class="field">
                  <span>OpenAI fallback（OpenAI 兜底模型）</span>
                  <input v-model.trim="form.fallback_model_openai" class="input" />
                </label>
                <label class="field">
                  <span>Gemini fallback（Gemini 兜底模型）</span>
                  <input v-model.trim="form.fallback_model_gemini" class="input" />
                </label>
                <label class="field">
                  <span>Antigravity fallback（Antigravity 兜底模型）</span>
                  <input v-model.trim="form.fallback_model_antigravity" class="input" />
                </label>
              </div>

              <div class="setting-row">
                <div>
                  <h3>{{ localText("身份补丁", "Identity patch") }}</h3>
                  <p>{{ localText("用于 Claude 到 Gemini 等场景的系统提示补丁。", "System prompt patch for Claude-to-Gemini style routing.") }}</p>
                </div>
                <Toggle v-model="form.enable_identity_patch" />
              </div>
              <label v-if="form.enable_identity_patch" class="field">
                <span>{{ localText("身份补丁提示词", "Identity patch prompt") }}</span>
                <textarea v-model="form.identity_patch_prompt" class="textarea min-h-[140px] font-mono text-xs" />
              </label>

              <div class="grid gap-5 md:grid-cols-2">
                <label class="field">
                  <span>{{ localText("最低 Claude Code 版本", "Minimum Claude Code version") }}</span>
                  <input v-model.trim="form.min_claude_code_version" class="input" placeholder="1.0.0" />
                </label>
                <label class="field">
                  <span>{{ localText("最高 Claude Code 版本", "Maximum Claude Code version") }}</span>
                  <input v-model.trim="form.max_claude_code_version" class="input" placeholder="2.0.0" />
                </label>
                <label class="field">
                  <span>{{ localText("Antigravity User-Agent 版本", "Antigravity User-Agent version") }}</span>
                  <input v-model.trim="form.antigravity_user_agent_version" class="input" />
                </label>
                <label class="field">
                  <span>{{ localText("OpenAI Codex User-Agent", "OpenAI Codex User-Agent") }}</span>
                  <input v-model.trim="form.openai_codex_user_agent" class="input" />
                </label>
                <label class="field">
                  <span>{{ localText("固定 Codex 版本（可留空）", "Pinned Codex version (optional)") }}</span>
                  <input v-model.trim="form.openai_codex_client_version" class="input" placeholder="0.146.0" />
                  <small>{{ localText("留空时使用下方自动同步版本。", "Leave empty to use the synchronized version below.") }}</small>
                </label>
                <label class="field">
                  <span>{{ localText("当前自动同步版本", "Current synchronized version") }}</span>
                  <input :value="form.openai_codex_client_version_synced || '-'" class="input" disabled />
                </label>
              </div>

              <div class="setting-row">
                <div>
                  <h3>{{ localText("自动同步 Codex 稳定版本", "Synchronize stable Codex version") }}</h3>
                  <p>{{ localText("默认每 6 小时检查一次；失败保留旧值，只向前升级。", "Checks every 6 hours by default, keeps the old value on failure, and never downgrades.") }}</p>
                </div>
                <Toggle v-model="form.openai_codex_version_auto_sync_enabled" />
              </div>

              <CodexClientAdmissionCard />

              <div class="grid gap-4 md:grid-cols-2">
                <SwitchCard v-model="form.allow_ungrouped_key_scheduling" :title="localText('允许未分组 Key 调度', 'Allow ungrouped key scheduling')" />
                <SwitchCard v-model="form.enable_fingerprint_unification" :title="localText('统一指纹', 'Fingerprint unification')" />
                <SwitchCard v-model="form.enable_metadata_passthrough" :title="localText('透传 metadata', 'Metadata passthrough')" />
                <SwitchCard v-model="form.enable_cch_signing" :title="localText('CCH 签名（已废弃）', 'CCH signing (deprecated)')" />
                <SwitchCard v-model="form.enable_anthropic_cache_ttl_1h_injection" :title="localText('注入 1h 缓存 TTL', 'Inject 1h cache TTL')" />
                <SwitchCard v-model="form.rewrite_message_cache_control" :title="localText('重写消息缓存控制', 'Rewrite message cache control')" />
                <SwitchCard v-model="form.openai_allow_claude_code_codex_plugin" :title="localText('允许 Claude Code Codex 插件', 'Allow Claude Code Codex plugin')" />
              </div>

              <div class="upstream-billing-panel">
                <div class="grid gap-5 md:grid-cols-2">
                  <label class="field">
                    <span>{{ localText("Grok 默认文本模型", "Grok default text model") }}</span>
                    <input v-model.trim="form.grok_default_text_model" class="input font-mono" placeholder="grok-4.5" />
                    <small>{{ localText("仅用于没有账号级映射的 Grok 账号。", "Used only when a Grok account has no account-level mapping.") }}</small>
                  </label>
                  <div class="setting-row rounded-xl border border-gray-200 px-4 dark:border-dark-700">
                    <div>
                      <h3>{{ localText("跨客户端模型映射", "Cross-client model mapping") }}</h3>
                      <p>{{ localText("把 GPT、Codex、o 系列和 Claude 模型映射到上面的 Grok 模型；默认关闭。", "Map GPT, Codex, o-series and Claude models to the Grok model above; disabled by default.") }}</p>
                    </div>
                    <Toggle v-model="form.grok_cross_client_model_map_enabled" />
                  </div>
				  <label class="field md:col-span-2">
					<span>{{ localText("Grok 上游配额停调阈值", "Grok upstream quota scheduling threshold") }}</span>
					<input v-model.number="form.grok_scheduling_threshold_percent" class="input" type="number" min="1" max="100" />
					<small>{{ localText("1–99% 会在上游配额达到阈值后暂停账号，100% 表示关闭；只读取真实上游响应头。", "1–99% pauses the account at the threshold; 100% disables it. Only real upstream headers are used.") }}</small>
				  </label>
                </div>
              </div>

              <div class="upstream-billing-panel">
                <div class="setting-row">
                  <div>
                    <h3>{{ localText("Sub2API 采购倍率自动探测", "Sub2API upstream rate auto probe") }}</h3>
                    <p>{{ localText("默认关闭。只探测账号实际采购倍率，不改变用户价格；多节点只由一个节点执行。", "Disabled by default. It only probes upstream purchase rates and never changes customer pricing; a single node leads in a cluster.") }}</p>
                  </div>
                  <Toggle v-model="form.upstream_billing_auto_probe_enabled" />
                </div>
                <div v-if="form.upstream_billing_auto_probe_enabled" class="preset-grid">
                  <div class="preset-group">
                    <span>{{ localText("探测周期", "Probe interval") }}</span>
                    <div class="preset-buttons">
                      <button v-for="minutes in [15, 30, 60, 180]" :key="minutes" type="button" :class="['preset-button', { active: form.upstream_billing_probe_interval_minutes === minutes }]" @click="form.upstream_billing_probe_interval_minutes = minutes">
                        {{ minutes < 60 ? `${minutes}${localText(' 分钟', ' min')}` : `${minutes / 60}${localText(' 小时', ' h')}` }}
                      </button>
                    </div>
                  </div>
                  <div class="preset-group">
                    <span>{{ localText("单批账号", "Accounts per batch") }}</span>
                    <div class="preset-buttons">
                      <button v-for="size in [5, 10, 20]" :key="size" type="button" :class="['preset-button', { active: form.upstream_billing_probe_batch_size === size }]" @click="form.upstream_billing_probe_batch_size = size">{{ size }}</button>
                    </div>
                  </div>
                  <div class="preset-group">
                    <span>{{ localText("并发探测", "Probe concurrency") }}</span>
                    <div class="preset-buttons">
                      <button v-for="count in [1, 2, 3, 4]" :key="count" type="button" :class="['preset-button', { active: form.upstream_billing_probe_concurrency === count }]" @click="form.upstream_billing_probe_concurrency = count">{{ count }}</button>
                    </div>
                  </div>
                </div>

                <div class="setting-row upstream-scheduler-row">
                  <div>
                    <h3>{{ localText("高级调度与采购成本排序", "Advanced scheduling and purchase-cost ordering") }}</h3>
                    <p>{{ localText("调度开关、Top-K、订阅池偏好和全部权重已集中到运行配置页，使用修订号原子保存，避免两个页面互相覆盖。", "Scheduler switches, Top-K, subscription preference, and all weights are centralized on the runtime page with revision-based atomic saves.") }}</p>
                  </div>
                  <a href="/admin/runtime" class="btn btn-secondary btn-sm shrink-0">
                    {{ localText("打开运行策略", "Open runtime policy") }}
                  </a>
                </div>
              </div>

              <div class="setting-row">
                <div>
                  <h3>{{ localText("客户端日期行指纹归一化", "Client dateline normalization") }}</h3>
                  <p>
                    {{
                      localText(
                        "默认开启。只规范 Anthropic OAuth 和 Setup Token 请求中系统提示里的日期行，清除非官方地址触发的隐藏指纹；API Key 账号不受影响。",
                        "Enabled by default. It only normalizes date lines in Anthropic OAuth and Setup Token system prompts to remove hidden fingerprints triggered by non-official base URLs; API key accounts are unaffected.",
                      )
                    }}
                  </p>
                </div>
                <Toggle v-model="form.enable_client_dateline_normalization" />
              </div>

              <div class="priority-policy-panel">
                <div class="priority-policy-head">
                  <div>
                    <h3>{{ localText("OpenAI Fast / Flex 策略", "OpenAI Fast / Flex policy") }}</h3>
                    <p>{{ localText("按用户、账号类型、模型和 service_tier 顺序匹配；用户专属规则优先，同层只执行第一条命中规则。", "Match by user, account type, model, and service_tier. User-specific rules take priority and only the first matching rule in each layer runs.") }}</p>
                  </div>
                  <span class="priority-policy-badge">{{ localText("官方规则", "Official rules") }}</span>
                </div>

                <div class="priority-detection-grid">
                  <div class="priority-detection-card">
                    <div>
                      <div class="priority-detection-title">
                        <strong>{{ localText("根据请求判断", "Detect from request") }}</strong>
                        <HelpTooltip :content="localText('使用规则处理后最终发送给上游的 service_tier 判断计费模式。不会读取已删除的 API Key 偏好。', 'Use the final service_tier sent upstream after policy processing. The retired API key preference is never read.')" />
                      </div>
                      <p>{{ localText("响应缺失时，可按实际发送值作为证据。", "Use the actual sent value as evidence when the response omits the tier.") }}</p>
                    </div>
                    <Toggle
                      :model-value="openAIFastPolicyForm.priority_detection_by_request_enabled"
                      @update:model-value="setPriorityDetectionSource('request', $event)"
                    />
                  </div>
                  <div class="priority-detection-card">
                    <div>
                      <div class="priority-detection-title">
                        <strong>{{ localText("根据响应判断", "Detect from response") }}</strong>
                        <HelpTooltip :content="localText('优先使用上游响应明确返回的 service_tier。响应为 default 时按标准计费，返回 priority 时按优先处理计费。', 'Prefer the explicit service_tier returned by the upstream. default uses standard billing and priority uses Priority billing.')" />
                      </div>
                      <p>{{ localText("双开时响应优先；非法响应会记录异常后回退请求。", "When both are enabled, response evidence wins; invalid values are recorded before falling back to the request.") }}</p>
                    </div>
                    <Toggle
                      :model-value="openAIFastPolicyForm.priority_detection_by_response_enabled"
                      @update:model-value="setPriorityDetectionSource('response', $event)"
                    />
                  </div>
                </div>

                <div class="priority-rules-head">
                  <div>
                    <strong>{{ localText("处理规则", "Processing rules") }}</strong>
                    <p>{{ localText("上下移动会改变真实匹配顺序。无规则时只透传客户已经提供的合法 service_tier。", "Moving rules changes the real match order. With no rules, valid service_tier values already supplied by the client pass through.") }}</p>
                  </div>
                  <button type="button" class="btn btn-secondary btn-sm" @click="addOpenAIFastPolicyRule">
                    <Icon name="plus" size="xs" />
                    {{ localText("添加规则", "Add rule") }}
                  </button>
                </div>

                <div v-if="openAIFastPolicyForm.rules.length === 0" class="empty-hint mt-4">
                  {{ localText("当前没有 Fast / Flex 规则。", "No Fast / Flex rules configured.") }}
                </div>

                <div v-else class="priority-rule-list">
                  <div v-for="(rule, index) in openAIFastPolicyForm.rules" :key="rule.id || index" class="priority-rule-card">
                    <div class="priority-rule-head">
                      <div>
                        <strong>{{ localText(`规则 ${index + 1}`, `Rule ${index + 1}`) }}</strong>
                        <code v-if="rule.id">{{ rule.id }}</code>
                      </div>
                      <div class="priority-rule-actions">
                        <button type="button" class="icon-button" :disabled="index === 0" :title="localText('上移', 'Move up')" @click="moveOpenAIFastPolicyRule(index, -1)">
                          <Icon name="arrowUp" size="xs" />
                        </button>
                        <button type="button" class="icon-button" :disabled="index === openAIFastPolicyForm.rules.length - 1" :title="localText('下移', 'Move down')" @click="moveOpenAIFastPolicyRule(index, 1)">
                          <Icon name="arrowDown" size="xs" />
                        </button>
                        <button type="button" class="icon-button text-red-600 dark:text-red-400" :title="localText('删除规则', 'Delete rule')" @click="removeOpenAIFastPolicyRule(index)">
                          <Icon name="trash" size="xs" />
                        </button>
                      </div>
                    </div>

                    <div class="grid gap-4 md:grid-cols-3">
                      <label class="field">
                        <span>service_tier</span>
                        <BaseSelect v-model="rule.service_tier" :options="openAIFastPolicyTierOptions" :searchable="false" />
                      </label>
                      <label class="field">
                        <span>{{ localText("动作", "Action") }}</span>
                        <BaseSelect v-model="rule.action" :options="openAIFastPolicyActionOptions" :searchable="false" />
                      </label>
                      <label class="field">
                        <span>{{ localText("账号范围", "Account scope") }}</span>
                        <BaseSelect v-model="rule.scope" :options="openAIFastPolicyScopeOptions" :searchable="false" />
                      </label>
                    </div>

                    <label class="field mt-4">
                      <span>{{ localText("指定用户", "Specific users") }}</span>
                      <small>{{ localText("留空表示所有用户；指定用户规则会优先于全局规则。", "Leave empty for all users. User-specific rules take priority over global rules.") }}</small>
                      <OpenAIFastPolicyUserSelector
                        :model-value="rule.user_ids || []"
                        @update:model-value="rule.user_ids = $event"
                      />
                    </label>

                    <div class="grid gap-4 md:grid-cols-2">
                      <label class="field">
                        <span>{{ localText("模型白名单，逗号分隔", "Model whitelist, comma-separated") }}</span>
                        <input :value="(rule.model_whitelist || []).join(', ')" class="input" placeholder="gpt-5.5, gpt-5.6-*" @input="updateOpenAIFastPolicyWhitelist(rule, $event)" />
                      </label>
                      <label class="field">
                        <span>{{ localText("白名单未命中时", "When whitelist does not match") }}</span>
                        <BaseSelect v-model="rule.fallback_action" :options="openAIFastPolicyFallbackOptions" :searchable="false" />
                      </label>
                    </div>

                    <div v-if="rule.action === 'block' || rule.fallback_action === 'block'" class="grid gap-4 md:grid-cols-2">
                      <label v-if="rule.action === 'block'" class="field">
                        <span>{{ localText("阻断提示", "Block message") }}</span>
                        <input v-model.trim="rule.error_message" class="input" />
                      </label>
                      <label v-if="rule.fallback_action === 'block'" class="field">
                        <span>{{ localText("兜底阻断提示", "Fallback block message") }}</span>
                        <input v-model.trim="rule.fallback_error_message" class="input" />
                      </label>
                    </div>
                  </div>
                </div>
              </div>

              <div class="setting-row">
                <div>
                  <h3>{{ localText("Claude OAuth 系统提示词注入", "Claude OAuth system prompt injection") }}</h3>
                  <p>{{ localText("保留网关转发层的系统提示词注入能力。", "Keep gateway-level system prompt injection.") }}</p>
                </div>
                <Toggle v-model="form.enable_claude_oauth_system_prompt_injection" />
              </div>
              <div v-if="form.enable_claude_oauth_system_prompt_injection" class="space-y-5">
                <label class="field">
                  <span>{{ localText("系统提示词", "System prompt") }}</span>
                  <textarea v-model="form.claude_oauth_system_prompt" class="textarea min-h-[140px] font-mono text-xs" />
                </label>
                <label class="field">
                  <span>{{ localText("系统提示词 blocks JSON", "System prompt blocks JSON") }}</span>
                  <textarea v-model="form.claude_oauth_system_prompt_blocks" class="textarea min-h-[160px] font-mono text-xs" />
                </label>
              </div>
            </div>
          </section>
        </div>

        <div v-show="activeTab === 'policy'" class="space-y-6">
          <PolicyCard
            :title="localText('529 过载冷却', '529 overload cooldown')"
            :description="localText('上游过载后临时冷却，避免持续打满故障渠道。', 'Temporarily cool down overloaded upstream channels.')"
            :loading="overloadCooldownLoading"
            :saving="overloadCooldownSaving"
            @save="saveOverloadCooldownSettings"
          >
            <div class="setting-row">
              <div>
                <h3>{{ localText("启用", "Enabled") }}</h3>
              </div>
              <Toggle v-model="overloadCooldownForm.enabled" />
            </div>
            <label v-if="overloadCooldownForm.enabled" class="field max-w-xs">
              <span>{{ localText("冷却分钟", "Cooldown minutes") }}</span>
              <input v-model.number="overloadCooldownForm.cooldown_minutes" class="input" type="number" min="1" max="120" />
            </label>
          </PolicyCard>

          <PolicyCard
            :title="localText('429 限流冷却', '429 rate-limit cooldown')"
            :description="localText('请求被限流后短暂冷却，减少无效重试。', 'Cool down briefly after rate-limit responses.')"
            :loading="rateLimit429CooldownLoading"
            :saving="rateLimit429CooldownSaving"
            @save="saveRateLimit429CooldownSettings"
          >
            <div class="setting-row">
              <div>
                <h3>{{ localText("启用", "Enabled") }}</h3>
              </div>
              <Toggle v-model="rateLimit429CooldownForm.enabled" />
            </div>
            <label v-if="rateLimit429CooldownForm.enabled" class="field max-w-xs">
              <span>{{ localText("冷却秒数", "Cooldown seconds") }}</span>
              <input v-model.number="rateLimit429CooldownForm.cooldown_seconds" class="input" type="number" min="1" max="3600" />
            </label>
          </PolicyCard>

          <PolicyCard
            :title="localText('账号兜底排队', 'Account fallback queue')"
            :description="localText('所有可用账号并发满时，允许请求短暂排队等待账号槽位；关闭后满并发会立即返回并发限制。', 'When all eligible accounts are full, queue briefly for an account slot; disabled means fail fast on concurrency limits.')"
            :loading="fallbackQueueLoading"
            :saving="fallbackQueueSaving"
            @save="saveFallbackQueueSettings"
          >
            <div class="setting-row">
              <div>
                <h3>{{ localText("启用", "Enabled") }}</h3>
                <p>{{ localText("仅影响账号级兜底排队，不改变套餐并发和单账号并发上限。", "Only affects account-level fallback queueing, not plan concurrency or per-account concurrency.") }}</p>
              </div>
              <Toggle v-model="fallbackQueueForm.enabled" />
            </div>
            <label v-if="fallbackQueueForm.enabled" class="field max-w-xs">
              <span>{{ localText("单账号最大排队请求数", "Max queued requests per account") }}</span>
              <input v-model.number="fallbackQueueForm.max_waiting" class="input" type="number" min="1" max="100000" />
            </label>
            <label v-if="fallbackQueueForm.enabled" class="field max-w-xs">
              <span>{{ localText("最长等待秒数", "Max wait seconds") }}</span>
              <input v-model.number="fallbackQueueForm.wait_timeout_seconds" class="input" type="number" min="1" max="3600" />
            </label>
          </PolicyCard>

          <PolicyCard
            :title="localText('流超时处理', 'Stream timeout')"
            :description="localText('连续流式超时后执行临时下线、报错或忽略。', 'Handle repeated stream timeouts.')"
            :loading="streamTimeoutLoading"
            :saving="streamTimeoutSaving"
            @save="saveStreamTimeoutSettings"
          >
            <div class="setting-row">
              <div>
                <h3>{{ localText("启用", "Enabled") }}</h3>
              </div>
              <Toggle v-model="streamTimeoutForm.enabled" />
            </div>
            <div v-if="streamTimeoutForm.enabled" class="grid gap-5 md:grid-cols-2">
              <label class="field">
                <span>{{ localText("动作", "Action") }}</span>
                <select v-model="streamTimeoutForm.action" class="input">
                  <option value="temp_unsched">{{ localText("临时下线", "Temporary unschedule") }}</option>
                  <option value="error">{{ localText("直接报错", "Return error") }}</option>
                  <option value="none">{{ localText("仅记录", "Record only") }}</option>
                </select>
              </label>
              <label class="field">
                <span>{{ localText("临时下线分钟", "Unschedule minutes") }}</span>
                <input v-model.number="streamTimeoutForm.temp_unsched_minutes" class="input" type="number" min="1" max="1440" />
              </label>
              <label class="field">
                <span>{{ localText("阈值次数", "Threshold count") }}</span>
                <input v-model.number="streamTimeoutForm.threshold_count" class="input" type="number" min="1" max="100" />
              </label>
              <label class="field">
                <span>{{ localText("统计窗口分钟", "Window minutes") }}</span>
                <input v-model.number="streamTimeoutForm.threshold_window_minutes" class="input" type="number" min="1" max="1440" />
              </label>
            </div>
          </PolicyCard>

          <PolicyCard
            :title="localText('修正器', 'Rectifier')"
            :description="localText('修正上游响应中的 thinking、budget 和 API Key 签名问题。', 'Rectify thinking, budget and API key signature issues.')"
            :loading="rectifierLoading"
            :saving="rectifierSaving"
            @save="saveRectifierSettings"
          >
            <div class="grid gap-4 md:grid-cols-2">
              <SwitchCard v-model="rectifierForm.enabled" :title="localText('启用修正器', 'Enable rectifier')" />
              <SwitchCard v-model="rectifierForm.thinking_signature_enabled" :title="localText('Thinking 签名修正', 'Thinking signature rectifier')" />
              <SwitchCard v-model="rectifierForm.thinking_budget_enabled" :title="localText('Thinking budget 修正', 'Thinking budget rectifier')" />
              <SwitchCard v-model="rectifierForm.apikey_signature_enabled" :title="localText('API Key 签名修正', 'API key signature rectifier')" />
            </div>
            <label v-if="rectifierForm.apikey_signature_enabled" class="field">
              <span>{{ localText("API Key 签名模式，每行一个", "API key signature patterns, one per line") }}</span>
              <textarea v-model="rectifierPatternsInput" class="textarea min-h-[120px] font-mono text-xs" />
            </label>
          </PolicyCard>

          <PolicyCard
            :title="localText('Beta 策略', 'Beta policy')"
            :description="localText('控制 Anthropic beta token 的透传、过滤或阻断。', 'Pass, filter or block Anthropic beta tokens.')"
            :loading="betaPolicyLoading"
            :saving="betaPolicySaving"
            @save="saveBetaPolicySettings"
          >
            <div class="space-y-4">
              <button type="button" class="btn btn-secondary btn-sm" @click="addBetaPolicyRule">
                <Icon name="plus" size="xs" />
                {{ localText("添加规则", "Add rule") }}
              </button>
              <div v-if="betaPolicyForm.rules.length === 0" class="empty-hint">
                {{ localText("当前没有 beta 策略规则。", "No beta policy rules configured.") }}
              </div>
              <div
                v-for="(rule, index) in betaPolicyForm.rules"
                :key="index"
                class="rounded-lg border border-gray-200 p-4 dark:border-dark-700"
              >
                <div class="grid gap-4 md:grid-cols-4">
                  <label class="field md:col-span-2">
                    <span>Beta token</span>
                    <input v-model.trim="rule.beta_token" class="input" placeholder="context-1m-2025-08-07" />
                  </label>
                  <label class="field">
                    <span>{{ localText("动作", "Action") }}</span>
                    <select v-model="rule.action" class="input">
                      <option value="pass">{{ localText("透传", "Pass") }}</option>
                      <option value="filter">{{ localText("过滤", "Filter") }}</option>
                      <option value="block">{{ localText("阻断", "Block") }}</option>
                    </select>
                  </label>
                  <label class="field">
                    <span>{{ localText("范围", "Scope") }}</span>
                    <select v-model="rule.scope" class="input">
                      <option value="all">{{ localText("全部", "All") }}</option>
                      <option value="oauth">OAuth</option>
                      <option value="apikey">API Key</option>
                      <option value="bedrock">Bedrock</option>
                    </select>
                  </label>
                  <label class="field md:col-span-2">
                    <span>{{ localText("模型白名单，逗号分隔", "Model whitelist, comma-separated") }}</span>
                    <input :value="(rule.model_whitelist || []).join(', ')" class="input" @input="updateBetaWhitelist(rule, $event)" />
                  </label>
                  <label class="field">
                    <span>{{ localText("兜底动作", "Fallback action") }}</span>
                    <select v-model="rule.fallback_action" class="input">
                      <option value="pass">{{ localText("透传", "Pass") }}</option>
                      <option value="filter">{{ localText("过滤", "Filter") }}</option>
                      <option value="block">{{ localText("阻断", "Block") }}</option>
                    </select>
                  </label>
                  <label class="field">
                    <span>{{ localText("错误提示", "Error message") }}</span>
                    <input v-model.trim="rule.error_message" class="input" />
                  </label>
                </div>
                <div class="mt-4 flex justify-end">
                  <button type="button" class="btn btn-secondary btn-sm text-red-600 dark:text-red-400" @click="removeBetaPolicyRule(index)">
                    <Icon name="trash" size="xs" />
                    {{ localText("删除规则", "Delete rule") }}
                  </button>
                </div>
              </div>
            </div>
          </PolicyCard>
        </div>

        <div class="flex justify-end">
          <button type="submit" :disabled="saving || loadFailed" class="btn btn-primary">
            <span v-if="saving" class="spinner-sm"></span>
            <Icon v-else name="checkCircle" size="xs" />
            {{ saving ? localText("保存中", "Saving") : localText("保存设置", "Save settings") }}
          </button>
        </div>
      </form>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, defineComponent, h, onMounted, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { adminAPI, authAPI } from "@/api";
import AppLayout from "@/components/layout/GatewayLiteLayout.vue";
import Icon from "@/components/icons/Icon.vue";
import Toggle from "@/components/common/Toggle.vue";
import BaseSelect from "@/components/common/Select.vue";
import HelpTooltip from "@/components/common/HelpTooltip.vue";
import ProfileTotpCard from "@/components/user/profile/ProfileTotpCard.vue";
import OpenAIFastPolicyUserSelector from "@/views/admin/settings/OpenAIFastPolicyUserSelector.vue";
import CodexClientAdmissionCard from "@/views/admin/settings/CodexClientAdmissionCard.vue";
import type {
  BetaPolicyRule,
  BillingSettlementSettings,
  CompositeGroupsSettings,
  GatewayLiteControlPlaneConfig,
  OpenAIFastPolicyRule,
  OpenAIFastPolicySettings,
  SystemSettings,
  UpdateGatewayLiteControlPlaneConfig,
  UpdateSettingsRequest,
} from "@/api/admin/settings";
import type { OpsCodexTailBurstSettings } from "@/api/admin/ops";
import { extractApiErrorMessage } from "@/utils/apiError";
import { useAppStore } from "@/stores/app";
import { useAdminSettingsStore } from "@/stores/adminSettings";
import { useAuthStore } from "@/stores/auth";
import {
  redirectToGatewayLiteLogin,
  rememberGatewayLiteLoginPath,
} from "@/utils/gatewayLiteLogin";

type SettingsTab = "general" | "features" | "controlPlane" | "security" | "gateway" | "policy";
type IconName = InstanceType<typeof Icon>["$props"]["name"];

interface GatewaySettingsForm {
  site_name: string;
  site_logo: string;
  site_subtitle: string;
  api_base_url: string;
  contact_info: string;
  doc_url: string;
  hide_ccs_import_button: boolean;
  table_default_page_size: number;
  table_page_size_options: number[];
  backend_mode_enabled: boolean;
  totp_enabled: boolean;
  turnstile_enabled: boolean;
  turnstile_site_key: string;
  turnstile_secret_key: string;
  turnstile_secret_key_configured: boolean;
  api_key_acl_trust_forwarded_ip: boolean;
  forwarded_client_ip_headers: string[];
  enable_model_fallback: boolean;
  fallback_model_anthropic: string;
  fallback_model_openai: string;
  fallback_model_gemini: string;
  fallback_model_antigravity: string;
  enable_identity_patch: boolean;
  identity_patch_prompt: string;
  min_claude_code_version: string;
  max_claude_code_version: string;
  allow_ungrouped_key_scheduling: boolean;
  gateway_custom_error_message: string;
  enable_fingerprint_unification: boolean;
  enable_metadata_passthrough: boolean;
  enable_cch_signing: boolean;
  enable_claude_oauth_system_prompt_injection: boolean;
  claude_oauth_system_prompt: string;
  claude_oauth_system_prompt_blocks: string;
  enable_anthropic_cache_ttl_1h_injection: boolean;
  rewrite_message_cache_control: boolean;
  enable_client_dateline_normalization: boolean;
  antigravity_user_agent_version: string;
  openai_codex_user_agent: string;
  openai_codex_client_version: string;
  openai_codex_client_version_synced: string;
  openai_codex_version_auto_sync_enabled: boolean;
  openai_allow_claude_code_codex_plugin: boolean;
  openai_advanced_scheduler_enabled: boolean;
  grok_default_text_model: string;
  grok_cross_client_model_map_enabled: boolean;
  grok_scheduling_threshold_percent: number;
  upstream_billing_auto_probe_enabled: boolean;
  upstream_billing_probe_interval_minutes: number;
  upstream_billing_probe_batch_size: number;
  upstream_billing_probe_concurrency: number;
  openai_upstream_cost_scheduling_enabled: boolean;
  openai_upstream_cost_weight: number;
  openai_quota_headroom_weight: number;
  openai_prefer_soonest_reset_weight: number;
  risk_control_enabled: boolean;
  cyber_session_block_enabled: boolean;
  cyber_session_block_ttl_seconds: number;
  channel_monitor_enabled: boolean;
  channel_monitor_mode: 'v1' | 'v2';
  channel_monitor_default_interval_seconds: number;
  channel_monitor_hide_throughput: boolean;
}

interface GatewayLiteConfigForm extends GatewayLiteControlPlaneConfig {
  control_plane_token: string;
}

const { locale } = useI18n();
const appStore = useAppStore();
const adminSettingsStore = useAdminSettingsStore();
const authStore = useAuthStore();

const activeTab = ref<SettingsTab>("general");
const loading = ref(true);
const loadFailed = ref(false);
const saving = ref(false);
const tablePageSizeOptionsInput = ref("10, 20, 50, 100");
const customForwardedIPHeader = ref("");
const forwardedIPHeaderPresets = ["CF-Connecting-IP", "X-Real-IP", "True-Client-IP", "X-Forwarded-For"];
const gatewayLiteConfigLoading = ref(true);
const gatewayLiteConfigSaving = ref(false);
const billingSettlementLoading = ref(true);
const billingSettlementLoaded = ref(false);
const billingSettlementSaving = ref(false);
const compositeGroupsLoading = ref(true);
const compositeGroupsLoaded = ref(false);
const compositeGroupsSaving = ref(false);
const passwordChanging = ref(false);
const passwordForm = reactive({
  old_password: "",
  new_password: "",
  confirm_password: "",
});

const form = reactive<GatewaySettingsForm>({
  site_name: "JnmGatewayApi",
  site_logo: "",
  site_subtitle: "",
  api_base_url: "",
  contact_info: "",
  doc_url: "",
  hide_ccs_import_button: false,
  table_default_page_size: 20,
  table_page_size_options: [10, 20, 50, 100],
  backend_mode_enabled: true,
  totp_enabled: true,
  turnstile_enabled: false,
  turnstile_site_key: "",
  turnstile_secret_key: "",
  turnstile_secret_key_configured: false,
  api_key_acl_trust_forwarded_ip: false,
  forwarded_client_ip_headers: [],
  enable_model_fallback: false,
  fallback_model_anthropic: "",
  fallback_model_openai: "",
  fallback_model_gemini: "",
  fallback_model_antigravity: "",
  enable_identity_patch: false,
  identity_patch_prompt: "",
  min_claude_code_version: "",
  max_claude_code_version: "",
  allow_ungrouped_key_scheduling: false,
  gateway_custom_error_message: "",
  enable_fingerprint_unification: false,
  enable_metadata_passthrough: false,
  enable_cch_signing: false,
  enable_claude_oauth_system_prompt_injection: false,
  claude_oauth_system_prompt: "",
  claude_oauth_system_prompt_blocks: "",
  enable_anthropic_cache_ttl_1h_injection: false,
  rewrite_message_cache_control: false,
  enable_client_dateline_normalization: true,
  antigravity_user_agent_version: "",
  openai_codex_user_agent: "",
  openai_codex_client_version: "",
  openai_codex_client_version_synced: "",
  openai_codex_version_auto_sync_enabled: true,
  openai_allow_claude_code_codex_plugin: false,
  openai_advanced_scheduler_enabled: false,
  grok_default_text_model: "grok-4.5",
  grok_cross_client_model_map_enabled: false,
  grok_scheduling_threshold_percent: 100,
  upstream_billing_auto_probe_enabled: false,
  upstream_billing_probe_interval_minutes: 30,
  upstream_billing_probe_batch_size: 20,
  upstream_billing_probe_concurrency: 4,
  openai_upstream_cost_scheduling_enabled: false,
  openai_upstream_cost_weight: 0,
  openai_quota_headroom_weight: 0,
  openai_prefer_soonest_reset_weight: 0,
  risk_control_enabled: true,
  cyber_session_block_enabled: false,
  cyber_session_block_ttl_seconds: 3600,
  channel_monitor_enabled: true,
  channel_monitor_mode: 'v1',
  channel_monitor_default_interval_seconds: 60,
  channel_monitor_hide_throughput: true,
});

const openAIFastPolicyForm = reactive<OpenAIFastPolicySettings>({
  version: 1,
  priority_detection_by_request_enabled: true,
  priority_detection_by_response_enabled: true,
  rules: [],
});

const gatewayLiteConfigForm = reactive<GatewayLiteConfigForm>({
  region: "default",
  gateway_code: "",
  redis_prefix: "",
  admin_login_path: "/gateway-admin",
  login_title: "轻量网关管理后台",
  login_subtitle: "仅限授权管理员访问",
  control_plane_url: "",
  control_plane_token: "",
  control_plane_token_configured: false,
  control_plane_timeout_ms: 1000,
  runtime_health_interval_seconds: 15,
  runtime_active_window_seconds: 300,
  config_sync_interval_seconds: 30,
  cache_invalidation_interval_seconds: 5,
  usage_queue_pending_alert_threshold: 1000,
  usage_queue_dead_alert_threshold: 1,
  allow_live: false,
  config_path: "config.yaml",
  restart_required: false,
});

const billingSettlementForm = reactive<BillingSettlementSettings>({
  idempotent_enabled: false,
  mode: "legacy",
  legacy_warning: true,
});

const compositeGroupsForm = reactive<CompositeGroupsSettings>({
  enabled: false,
});

const codexTailBurstForm = reactive({ enabled: false });
const codexTailBurstLoading = ref(false);
const codexTailBurstLoaded = ref(false);
const codexTailBurstSaving = ref(false);
let codexTailBurstCached: OpsCodexTailBurstSettings | null = null;

const settingsTabs = computed<Array<{ key: SettingsTab; label: string; icon: IconName }>>(() => [
  { key: "general", label: localText("基础", "General"), icon: "cog" },
  { key: "features", label: localText("功能", "Features"), icon: "bolt" },
  { key: "controlPlane", label: localText("主站", "Control"), icon: "server" },
  { key: "security", label: localText("安全", "Security"), icon: "shield" },
  { key: "gateway", label: localText("网关", "Gateway"), icon: "database" },
  { key: "policy", label: localText("策略", "Policy"), icon: "clock" },
]);

const openAIFastPolicyTierOptions = computed(() => [
  { value: "all", label: localText("全部已识别层级", "All recognized tiers") },
  { value: "priority", label: localText("Priority（优先处理）", "Priority") },
  { value: "flex", label: "Flex" },
]);

const openAIFastPolicyActionOptions = computed(() => [
  { value: "pass", label: localText("透传", "Pass") },
  { value: "filter", label: localText("过滤字段", "Filter field") },
  { value: "block", label: localText("阻断请求", "Block request") },
  { value: "force_priority", label: localText("改写已有层级为 Priority", "Rewrite existing tier to Priority") },
]);

const openAIFastPolicyFallbackOptions = computed(() => [
  { value: "", label: localText("默认透传", "Default pass") },
  ...openAIFastPolicyActionOptions.value,
]);

const openAIFastPolicyScopeOptions = computed(() => [
  { value: "all", label: localText("全部账号", "All accounts") },
  { value: "oauth", label: "OAuth" },
  { value: "apikey", label: "API Key" },
  { value: "bedrock", label: "Bedrock" },
]);

const settingsTabKeyboardActions = {
  ArrowLeft: -1,
  ArrowUp: -1,
  ArrowRight: 1,
  ArrowDown: 1,
  Home: "first",
  End: "last",
} as const;

const adminApiKeyLoading = ref(true);
const adminApiKeyExists = ref(false);
const adminApiKeyMasked = ref("");
const adminApiKeyOperating = ref(false);
const adminApiKeyPlaintext = ref("");
const adminApiKeyVisible = ref(false);

const overloadCooldownLoading = ref(true);
const overloadCooldownSaving = ref(false);
const overloadCooldownForm = reactive({
  enabled: true,
  cooldown_minutes: 10,
});

const rateLimit429CooldownLoading = ref(true);
const rateLimit429CooldownSaving = ref(false);
const rateLimit429CooldownForm = reactive({
  enabled: true,
  cooldown_seconds: 5,
});

const fallbackQueueLoading = ref(true);
const fallbackQueueSaving = ref(false);
const fallbackQueueForm = reactive({
  enabled: true,
  max_waiting: 100,
  wait_timeout_seconds: 120,
});

const streamTimeoutLoading = ref(true);
const streamTimeoutSaving = ref(false);
const streamTimeoutForm = reactive({
  enabled: true,
  action: "temp_unsched" as "temp_unsched" | "error" | "none",
  temp_unsched_minutes: 5,
  threshold_count: 3,
  threshold_window_minutes: 10,
});

const rectifierLoading = ref(true);
const rectifierSaving = ref(false);
const rectifierPatternsInput = ref("");
const rectifierForm = reactive({
  enabled: true,
  thinking_signature_enabled: true,
  thinking_budget_enabled: true,
  apikey_signature_enabled: false,
  apikey_signature_patterns: [] as string[],
});

const betaPolicyLoading = ref(true);
const betaPolicySaving = ref(false);
const betaPolicyForm = reactive({
  rules: [] as BetaPolicyRule[],
});

function localText(zh: string, en: string): string {
  return locale.value.startsWith("zh") ? zh : en;
}

function hasForwardedIPHeader(header: string): boolean {
  const wanted = header.toLowerCase();
  return form.forwarded_client_ip_headers.some((item) => item.toLowerCase() === wanted);
}

function removeForwardedIPHeader(header: string): void {
  const wanted = header.toLowerCase();
  form.forwarded_client_ip_headers = form.forwarded_client_ip_headers.filter((item) => item.toLowerCase() !== wanted);
}

function addForwardedIPHeader(header: string): void {
  const normalized = header.trim();
  if (!normalized || hasForwardedIPHeader(normalized)) return;
  if (form.forwarded_client_ip_headers.length >= 16) {
    appStore.showError(localText("客户端 IP 请求头最多 16 个", "At most 16 client IP headers are allowed"));
    return;
  }
  if (!/^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/.test(normalized)) {
    appStore.showError(localText("请求头名称格式不正确", "Invalid HTTP header name"));
    return;
  }
  form.forwarded_client_ip_headers = [...form.forwarded_client_ip_headers, normalized];
}

function toggleForwardedIPHeader(header: string): void {
  if (hasForwardedIPHeader(header)) {
    removeForwardedIPHeader(header);
  } else {
    addForwardedIPHeader(header);
  }
}

function addCustomForwardedIPHeader(): void {
  addForwardedIPHeader(customForwardedIPHeader.value);
  customForwardedIPHeader.value = "";
}

function cloneOpenAIFastPolicyRules(rules: OpenAIFastPolicyRule[] | undefined): OpenAIFastPolicyRule[] {
  return (rules || []).map((rule) => ({
    ...rule,
    user_ids: Array.from(new Set((rule.user_ids || []).filter((id) => Number.isInteger(id) && id > 0))),
    model_whitelist: [...(rule.model_whitelist || [])],
  }));
}

function setPriorityDetectionSource(source: "request" | "response", enabled: boolean): void {
  const otherEnabled = source === "request"
    ? openAIFastPolicyForm.priority_detection_by_response_enabled
    : openAIFastPolicyForm.priority_detection_by_request_enabled;
  if (!enabled && !otherEnabled) {
    appStore.showError(localText("根据请求和根据响应至少要开启一个。", "At least one of request or response detection must remain enabled."));
    return;
  }
  if (source === "request") {
    openAIFastPolicyForm.priority_detection_by_request_enabled = enabled;
  } else {
    openAIFastPolicyForm.priority_detection_by_response_enabled = enabled;
  }
}

function addOpenAIFastPolicyRule(): void {
  openAIFastPolicyForm.rules.push({
    id: `rule_ui_${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 8)}`,
    service_tier: "priority",
    action: "pass",
    scope: "all",
    user_ids: [],
    model_whitelist: [],
    fallback_action: "pass",
  });
}

function removeOpenAIFastPolicyRule(index: number): void {
  openAIFastPolicyForm.rules.splice(index, 1);
}

function moveOpenAIFastPolicyRule(index: number, direction: -1 | 1): void {
  const target = index + direction;
  if (target < 0 || target >= openAIFastPolicyForm.rules.length) return;
  const [rule] = openAIFastPolicyForm.rules.splice(index, 1);
  if (rule) openAIFastPolicyForm.rules.splice(target, 0, rule);
}

function updateOpenAIFastPolicyWhitelist(rule: OpenAIFastPolicyRule, event: Event): void {
  const value = (event.target as HTMLInputElement | null)?.value || "";
  rule.model_whitelist = Array.from(new Set(value.split(",").map((item) => item.trim()).filter(Boolean)));
}

function buildOpenAIFastPolicySettings(): OpenAIFastPolicySettings {
  return {
    version: 1,
    priority_detection_by_request_enabled: openAIFastPolicyForm.priority_detection_by_request_enabled,
    priority_detection_by_response_enabled: openAIFastPolicyForm.priority_detection_by_response_enabled,
    rules: openAIFastPolicyForm.rules.map((rule) => ({
      id: rule.id?.trim() || undefined,
      service_tier: rule.service_tier,
      action: rule.action,
      scope: rule.scope,
      user_ids: Array.from(new Set((rule.user_ids || []).filter((id) => Number.isInteger(id) && id > 0))),
      error_message: rule.error_message?.trim() || undefined,
      model_whitelist: Array.from(new Set((rule.model_whitelist || []).map((item) => item.trim()).filter(Boolean))),
      fallback_action: rule.fallback_action || undefined,
      fallback_error_message: rule.fallback_error_message?.trim() || undefined,
    })),
  };
}

function selectSettingsTab(tab: SettingsTab): void {
  activeTab.value = tab;
}

function handleSettingsTabKeydown(event: KeyboardEvent, tab: SettingsTab): void {
  const action = settingsTabKeyboardActions[event.key as keyof typeof settingsTabKeyboardActions];
  if (action === undefined) return;

  event.preventDefault();
  const visibleTabs = settingsTabs.value;
  const currentIndex = visibleTabs.findIndex((item) => item.key === tab);
  let nextIndex = currentIndex < 0 ? 0 : currentIndex;

  if (action === "first") {
    nextIndex = 0;
  } else if (action === "last") {
    nextIndex = visibleTabs.length - 1;
  } else {
    nextIndex = (nextIndex + action + visibleTabs.length) % visibleTabs.length;
  }

  const nextTab = visibleTabs[nextIndex]?.key;
  if (!nextTab) return;
  activeTab.value = nextTab;
  window.requestAnimationFrame(() => {
    document.getElementById(`settings-tab-${nextTab}`)?.focus();
  });
}

function applySettings(settings: SystemSettings): void {
  const next: Partial<GatewaySettingsForm> = {
    site_name: settings.site_name || "JnmGatewayApi",
    site_logo: settings.site_logo || "",
    site_subtitle: settings.site_subtitle || "",
    api_base_url: settings.api_base_url || "",
    contact_info: settings.contact_info || "",
    doc_url: settings.doc_url || "",
    hide_ccs_import_button: Boolean(settings.hide_ccs_import_button),
    table_default_page_size: normalizeNumber(settings.table_default_page_size, 20, 5, 1000),
    table_page_size_options: normalizePageSizeOptions(settings.table_page_size_options),
    backend_mode_enabled: settings.backend_mode_enabled !== false,
    totp_enabled: Boolean(settings.totp_enabled),
    turnstile_enabled: Boolean(settings.turnstile_enabled),
    turnstile_site_key: settings.turnstile_site_key || "",
    turnstile_secret_key: "",
    turnstile_secret_key_configured: Boolean(settings.turnstile_secret_key_configured),
    api_key_acl_trust_forwarded_ip: Boolean(settings.api_key_acl_trust_forwarded_ip),
    forwarded_client_ip_headers: Array.from(new Set((settings.forwarded_client_ip_headers || []).map((item) => item.trim()).filter(Boolean))),
    enable_model_fallback: Boolean(settings.enable_model_fallback),
    fallback_model_anthropic: settings.fallback_model_anthropic || "",
    fallback_model_openai: settings.fallback_model_openai || "",
    fallback_model_gemini: settings.fallback_model_gemini || "",
    fallback_model_antigravity: settings.fallback_model_antigravity || "",
    enable_identity_patch: Boolean(settings.enable_identity_patch),
    identity_patch_prompt: settings.identity_patch_prompt || "",
    min_claude_code_version: settings.min_claude_code_version || "",
    max_claude_code_version: settings.max_claude_code_version || "",
    allow_ungrouped_key_scheduling: Boolean(settings.allow_ungrouped_key_scheduling),
    gateway_custom_error_message: settings.gateway_custom_error_message || "",
    enable_fingerprint_unification: Boolean(settings.enable_fingerprint_unification),
    enable_metadata_passthrough: Boolean(settings.enable_metadata_passthrough),
    enable_cch_signing: Boolean(settings.enable_cch_signing),
    enable_claude_oauth_system_prompt_injection: Boolean(settings.enable_claude_oauth_system_prompt_injection),
    claude_oauth_system_prompt: settings.claude_oauth_system_prompt || "",
    claude_oauth_system_prompt_blocks: settings.claude_oauth_system_prompt_blocks || "",
    enable_anthropic_cache_ttl_1h_injection: Boolean(settings.enable_anthropic_cache_ttl_1h_injection),
    rewrite_message_cache_control: Boolean(settings.rewrite_message_cache_control),
    enable_client_dateline_normalization: settings.enable_client_dateline_normalization !== false,
    antigravity_user_agent_version: settings.antigravity_user_agent_version || "",
    openai_codex_user_agent: settings.openai_codex_user_agent || "",
    openai_codex_client_version: settings.openai_codex_client_version || "",
    openai_codex_client_version_synced: settings.openai_codex_client_version_synced || "",
    openai_codex_version_auto_sync_enabled: settings.openai_codex_version_auto_sync_enabled !== false,
    openai_allow_claude_code_codex_plugin: Boolean(settings.openai_allow_claude_code_codex_plugin),
    openai_advanced_scheduler_enabled: Boolean(settings.openai_advanced_scheduler_enabled),
    grok_default_text_model: settings.grok_default_text_model || "grok-4.5",
    grok_cross_client_model_map_enabled: Boolean(settings.grok_cross_client_model_map_enabled),
    grok_scheduling_threshold_percent: normalizeNumber(settings.grok_scheduling_threshold_percent, 100, 1, 100),
    upstream_billing_auto_probe_enabled: Boolean(settings.upstream_billing_auto_probe_enabled),
    upstream_billing_probe_interval_minutes: normalizeNumber(settings.upstream_billing_probe_interval_minutes, 30, 5, 1440),
    upstream_billing_probe_batch_size: normalizeNumber(settings.upstream_billing_probe_batch_size, 20, 1, 20),
    upstream_billing_probe_concurrency: normalizeNumber(settings.upstream_billing_probe_concurrency, 4, 1, 4),
    openai_upstream_cost_scheduling_enabled: Boolean(settings.openai_upstream_cost_scheduling_enabled),
    openai_upstream_cost_weight: normalizeDecimal(settings.openai_upstream_cost_weight, 0, 0, 2),
    openai_quota_headroom_weight: normalizeDecimal(settings.openai_quota_headroom_weight, 0, 0, 2),
    openai_prefer_soonest_reset_weight: normalizeDecimal(settings.openai_prefer_soonest_reset_weight, 0, 0, 2),
    risk_control_enabled: settings.risk_control_enabled !== false,
    cyber_session_block_enabled: Boolean(settings.cyber_session_block_enabled),
    cyber_session_block_ttl_seconds: normalizeNumber(settings.cyber_session_block_ttl_seconds, 3600, 60, 604800),
    channel_monitor_enabled: settings.channel_monitor_enabled !== false,
    channel_monitor_mode: settings.channel_monitor_mode === 'v2' ? 'v2' : 'v1',
    channel_monitor_default_interval_seconds: normalizeNumber(settings.channel_monitor_default_interval_seconds, 60, 10, 86400),
    channel_monitor_hide_throughput: settings.channel_monitor_hide_throughput !== false,
  };

  Object.assign(form, next);
  const fastPolicy = settings.openai_fast_policy_settings;
  openAIFastPolicyForm.version = fastPolicy?.version || 1;
  openAIFastPolicyForm.priority_detection_by_request_enabled = fastPolicy?.priority_detection_by_request_enabled !== false;
  openAIFastPolicyForm.priority_detection_by_response_enabled = fastPolicy?.priority_detection_by_response_enabled !== false;
  if (!openAIFastPolicyForm.priority_detection_by_request_enabled && !openAIFastPolicyForm.priority_detection_by_response_enabled) {
    openAIFastPolicyForm.priority_detection_by_request_enabled = true;
    openAIFastPolicyForm.priority_detection_by_response_enabled = true;
  }
  openAIFastPolicyForm.rules = cloneOpenAIFastPolicyRules(fastPolicy?.rules);
  tablePageSizeOptionsInput.value = form.table_page_size_options.join(", ");
}

async function loadSettings(): Promise<void> {
  loading.value = true;
  loadFailed.value = false;
  try {
    const settings = await adminAPI.settings.getSettings();
    applySettings(settings);
  } catch (error: unknown) {
    loadFailed.value = true;
    appStore.showError(extractApiErrorMessage(error, localText("读取设置失败", "Failed to load settings")));
  } finally {
    loading.value = false;
  }
}

function applyGatewayLiteConfig(settings: GatewayLiteControlPlaneConfig): void {
  Object.assign(gatewayLiteConfigForm, {
    ...settings,
    control_plane_token: "",
  });
}

async function loadGatewayLiteConfig(): Promise<void> {
  gatewayLiteConfigLoading.value = true;
  try {
    const settings = await adminAPI.settings.getGatewayLiteControlPlaneConfig();
    applyGatewayLiteConfig(settings);
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, localText("读取主站配置失败", "Failed to load control plane config")));
  } finally {
    gatewayLiteConfigLoading.value = false;
  }
}

async function loadBillingSettlementSettings(): Promise<void> {
  billingSettlementLoading.value = true;
  billingSettlementLoaded.value = false;
  try {
    const settings = await adminAPI.settings.getBillingSettlementSettings();
    Object.assign(billingSettlementForm, settings);
    billingSettlementLoaded.value = true;
  } catch (error: unknown) {
    appStore.showError(
      extractApiErrorMessage(error, localText("读取结算模式失败", "Failed to load settlement mode")),
    );
  } finally {
    billingSettlementLoading.value = false;
  }
}

async function loadCompositeGroupsSettings(): Promise<void> {
  compositeGroupsLoading.value = true;
  compositeGroupsLoaded.value = false;
  try {
    const settings = await adminAPI.settings.getCompositeGroupsSettings();
    Object.assign(compositeGroupsForm, settings);
    compositeGroupsLoaded.value = true;
  } catch (error: unknown) {
    appStore.showError(
      extractApiErrorMessage(error, localText("读取聚合分组开关失败", "Failed to load the composite groups switch")),
    );
  } finally {
    compositeGroupsLoading.value = false;
  }
}

async function loadCodexTailBurstSettings(): Promise<void> {
  codexTailBurstLoading.value = true;
  codexTailBurstLoaded.value = false;
  try {
    const settings = await adminAPI.ops.getCodexTailBurstSettings();
    codexTailBurstCached = settings;
    codexTailBurstForm.enabled = settings.enabled ?? false;
    codexTailBurstLoaded.value = true;
  } catch (error: unknown) {
    appStore.showError(
      extractApiErrorMessage(error, localText("读取超额开关失败", "Failed to load the tail-burst switch")),
    );
  } finally {
    codexTailBurstLoading.value = false;
  }
}

async function saveCodexTailBurstSwitch(enabled: boolean): Promise<void> {
  if (!codexTailBurstLoaded.value || codexTailBurstSaving.value || !codexTailBurstCached) return;
  if (enabled === codexTailBurstForm.enabled) return;

  const previous = codexTailBurstForm.enabled;
  codexTailBurstForm.enabled = enabled;
  codexTailBurstSaving.value = true;
  try {
    const updated = await adminAPI.ops.updateCodexTailBurstSettings(enabled);
    // 兼容旧网关省略响应字段：专用接口的请求本身已明确保存目标值。
    codexTailBurstCached = {
      ...updated,
      enabled: updated?.enabled ?? enabled,
    };
    codexTailBurstForm.enabled = codexTailBurstCached.enabled ?? enabled;
    appStore.showSuccess(
      enabled
        ? localText(
            "超额已开启：账号贴近窗口上限或窗口/订阅即将重置时放行，直到上游权威拒绝",
            "Tail-burst enabled: accounts near the window cap or reset keep serving until the upstream rejects",
          )
        : localText(
            "超额已关闭：达到配额阈值即停调，不做尾部放行",
            "Tail-burst disabled: accounts pause at quota thresholds as usual",
          ),
    );
  } catch (error: unknown) {
    codexTailBurstForm.enabled = previous;
    appStore.showError(
      extractApiErrorMessage(
        error,
        localText("切换超额开关失败，已恢复原状态", "Failed to switch tail-burst; the previous state was restored"),
      ),
    );
  } finally {
    codexTailBurstSaving.value = false;
  }
}

async function saveCompositeGroupsSwitch(enabled: boolean): Promise<void> {
  if (!compositeGroupsLoaded.value || compositeGroupsSaving.value) return;
  if (enabled === compositeGroupsForm.enabled) return;

  const previous: CompositeGroupsSettings = { ...compositeGroupsForm };
  compositeGroupsForm.enabled = enabled;
  compositeGroupsSaving.value = true;
  try {
    const updated = await adminAPI.settings.updateCompositeGroupsSettings({ enabled });
    Object.assign(compositeGroupsForm, updated);
    appStore.showSuccess(
      updated.enabled
        ? localText("聚合分组已开启并即时生效", "Composite groups enabled and applied immediately")
        : localText("聚合分组已关闭，新请求将被拒绝", "Composite groups disabled; new requests will be rejected"),
    );
  } catch (error: unknown) {
    Object.assign(compositeGroupsForm, previous);
    appStore.showError(
      extractApiErrorMessage(
        error,
        localText("切换聚合分组失败，已恢复原状态", "Failed to switch composite groups; the previous state was restored"),
      ),
    );
  } finally {
    compositeGroupsSaving.value = false;
  }
}

async function saveBillingSettlementSwitch(enabled: boolean): Promise<void> {
  if (!billingSettlementLoaded.value || billingSettlementSaving.value) return;
  if (enabled === billingSettlementForm.idempotent_enabled) return;

  // 计费开关采用乐观显示；保存失败必须恢复完整服务端状态，不能让界面误报已生效。
  const previous: BillingSettlementSettings = { ...billingSettlementForm };
  billingSettlementForm.idempotent_enabled = enabled;
  billingSettlementForm.mode = enabled ? "idempotent_v2" : "legacy";
  billingSettlementSaving.value = true;
  try {
    const updated = await adminAPI.settings.updateBillingSettlementSettings({
      idempotent_enabled: enabled,
    });
    Object.assign(billingSettlementForm, updated);
    appStore.showSuccess(
      updated.idempotent_enabled
        ? localText("防重复结算已开启", "Duplicate-safe settlement enabled")
        : localText("已切换到旧版结算", "Legacy settlement enabled"),
    );
  } catch (error: unknown) {
    Object.assign(billingSettlementForm, previous);
    appStore.showError(
      extractApiErrorMessage(error, localText("切换结算模式失败，已恢复原状态", "Failed to switch settlement mode; the previous state was restored")),
    );
  } finally {
    billingSettlementSaving.value = false;
  }
}

function buildGatewayLiteConfigPayload(): UpdateGatewayLiteControlPlaneConfig {
  const payload: UpdateGatewayLiteControlPlaneConfig = {
    region: gatewayLiteConfigForm.region.trim() || "default",
    gateway_code: gatewayLiteConfigForm.gateway_code.trim(),
    redis_prefix: gatewayLiteConfigForm.redis_prefix.trim(),
    admin_login_path: gatewayLiteConfigForm.admin_login_path.trim(),
    login_title: gatewayLiteConfigForm.login_title.trim(),
    login_subtitle: gatewayLiteConfigForm.login_subtitle.trim(),
    control_plane_url: gatewayLiteConfigForm.control_plane_url.trim(),
    control_plane_timeout_ms: normalizeNumber(gatewayLiteConfigForm.control_plane_timeout_ms, 1000, 100, 30000),
    runtime_health_interval_seconds: normalizeNumber(gatewayLiteConfigForm.runtime_health_interval_seconds, 15, 5, 3600),
    runtime_active_window_seconds: normalizeNumber(gatewayLiteConfigForm.runtime_active_window_seconds, 300, 30, 86400),
    config_sync_interval_seconds: normalizeNumber(gatewayLiteConfigForm.config_sync_interval_seconds, 30, 5, 3600),
    cache_invalidation_interval_seconds: normalizeNumber(gatewayLiteConfigForm.cache_invalidation_interval_seconds, 5, 1, 3600),
    usage_queue_pending_alert_threshold: normalizeNumber(gatewayLiteConfigForm.usage_queue_pending_alert_threshold, 1000, 1, 10000000),
    usage_queue_dead_alert_threshold: normalizeNumber(gatewayLiteConfigForm.usage_queue_dead_alert_threshold, 1, 1, 10000000),
    allow_live: Boolean(gatewayLiteConfigForm.allow_live),
  };
  if (gatewayLiteConfigForm.control_plane_token.trim()) {
    payload.control_plane_token = gatewayLiteConfigForm.control_plane_token.trim();
  }
  return payload;
}

async function saveGatewayLiteConfig(): Promise<void> {
	await persistGatewayLiteConfig("control-plane");
}

async function saveGatewayLiteLiveSwitch(): Promise<void> {
  await persistGatewayLiteConfig("control-plane");
}

async function saveGatewayLiteLoginConfig(): Promise<void> {
	if (!/^\/[A-Za-z0-9][A-Za-z0-9_-]{2,63}$/.test(gatewayLiteConfigForm.admin_login_path.trim())) {
		appStore.showError(
			localText(
				"私有入口必须是以 / 开头的单段路径，只能包含字母、数字、下划线和连字符。",
				"The private entry must be one path segment beginning with / and may contain only letters, numbers, underscores, and hyphens.",
			),
		);
		return;
	}
	await persistGatewayLiteConfig("login");
}

async function persistGatewayLiteConfig(source: "control-plane" | "login"): Promise<void> {
  gatewayLiteConfigSaving.value = true;
  try {
    const updated = await adminAPI.settings.updateGatewayLiteControlPlaneConfig(buildGatewayLiteConfigPayload());
    applyGatewayLiteConfig(updated);
		rememberGatewayLiteLoginPath(updated.admin_login_path);
		appStore.showSuccess(
			source === "login"
				? localText("私有登录入口已保存并立即生效", "Private login entry saved and applied immediately")
				: updated.restart_required
					? localText("配置已保存并已热更新，建议手动同步确认最新状态", "Config saved and hot-applied; manually sync to confirm latest state")
					: localText("配置已保存并即时生效，已触发一次配置同步", "Config saved and applied immediately; sync triggered"),
		);
  } catch (error: unknown) {
		appStore.showError(
			extractApiErrorMessage(
				error,
				source === "login"
					? localText("保存登录入口失败", "Failed to save login entry")
					: localText("保存主站配置失败", "Failed to save control plane config"),
			),
		);
  } finally {
    gatewayLiteConfigSaving.value = false;
  }
}

async function changeAdminPassword(): Promise<void> {
	if (passwordForm.new_password.length < 8) {
		appStore.showError(localText("新密码至少需要 8 个字符", "The new password must contain at least 8 characters"));
		return;
	}
	if (passwordForm.new_password !== passwordForm.confirm_password) {
		appStore.showError(localText("两次输入的新密码不一致", "The new passwords do not match"));
		return;
	}
	passwordChanging.value = true;
	try {
		await authAPI.changePassword({
			old_password: passwordForm.old_password,
			new_password: passwordForm.new_password,
		});
		sessionStorage.setItem("gateway_lite_password_changed", "1");
		await authStore.logout();
		redirectToGatewayLiteLogin();
	} catch (error: unknown) {
		appStore.showError(extractApiErrorMessage(error, localText("修改密码失败", "Failed to change password")));
	} finally {
		passwordChanging.value = false;
	}
}

function buildSettingsPayload(): UpdateSettingsRequest {
  const pageSizeOptions = normalizePageSizeOptions(parsePageSizeOptionsInput(tablePageSizeOptionsInput.value));
  const defaultPageSize = pageSizeOptions.includes(form.table_default_page_size)
    ? form.table_default_page_size
    : pageSizeOptions[0] || 20;

  form.table_page_size_options = pageSizeOptions;
  form.table_default_page_size = defaultPageSize;
  tablePageSizeOptionsInput.value = pageSizeOptions.join(", ");

  const payload: UpdateSettingsRequest = {
    site_name: form.site_name.trim(),
    site_logo: form.site_logo.trim(),
    site_subtitle: form.site_subtitle.trim(),
    api_base_url: form.api_base_url.trim(),
    contact_info: form.contact_info.trim(),
    doc_url: form.doc_url.trim(),
    hide_ccs_import_button: form.hide_ccs_import_button,
    table_default_page_size: form.table_default_page_size,
    table_page_size_options: form.table_page_size_options,
    backend_mode_enabled: form.backend_mode_enabled,
    totp_enabled: form.totp_enabled,
    turnstile_enabled: form.turnstile_enabled,
    turnstile_site_key: form.turnstile_site_key.trim(),
    api_key_acl_trust_forwarded_ip: form.api_key_acl_trust_forwarded_ip,
    forwarded_client_ip_headers: [...form.forwarded_client_ip_headers],
    enable_model_fallback: form.enable_model_fallback,
    fallback_model_anthropic: form.fallback_model_anthropic.trim(),
    fallback_model_openai: form.fallback_model_openai.trim(),
    fallback_model_gemini: form.fallback_model_gemini.trim(),
    fallback_model_antigravity: form.fallback_model_antigravity.trim(),
    enable_identity_patch: form.enable_identity_patch,
    identity_patch_prompt: form.identity_patch_prompt,
    min_claude_code_version: form.min_claude_code_version.trim(),
    max_claude_code_version: form.max_claude_code_version.trim(),
    allow_ungrouped_key_scheduling: form.allow_ungrouped_key_scheduling,
    gateway_custom_error_message: form.gateway_custom_error_message.trim(),
    enable_fingerprint_unification: form.enable_fingerprint_unification,
    enable_metadata_passthrough: form.enable_metadata_passthrough,
    enable_cch_signing: form.enable_cch_signing,
    enable_claude_oauth_system_prompt_injection: form.enable_claude_oauth_system_prompt_injection,
    claude_oauth_system_prompt: form.claude_oauth_system_prompt.trim() ? form.claude_oauth_system_prompt : undefined,
    claude_oauth_system_prompt_blocks: form.claude_oauth_system_prompt_blocks.trim()
      ? form.claude_oauth_system_prompt_blocks
      : undefined,
    enable_anthropic_cache_ttl_1h_injection: form.enable_anthropic_cache_ttl_1h_injection,
    rewrite_message_cache_control: form.rewrite_message_cache_control,
    enable_client_dateline_normalization: form.enable_client_dateline_normalization,
    antigravity_user_agent_version: form.antigravity_user_agent_version.trim(),
    openai_codex_user_agent: form.openai_codex_user_agent.trim(),
    openai_codex_client_version: form.openai_codex_client_version.trim(),
    openai_codex_version_auto_sync_enabled: form.openai_codex_version_auto_sync_enabled,
    openai_allow_claude_code_codex_plugin: form.openai_allow_claude_code_codex_plugin,
    grok_default_text_model: form.grok_default_text_model.trim() || "grok-4.5",
    grok_cross_client_model_map_enabled: form.grok_cross_client_model_map_enabled,
    grok_scheduling_threshold_percent: normalizeNumber(form.grok_scheduling_threshold_percent, 100, 1, 100),
    upstream_billing_auto_probe_enabled: form.upstream_billing_auto_probe_enabled,
    upstream_billing_probe_interval_minutes: normalizeNumber(form.upstream_billing_probe_interval_minutes, 30, 5, 1440),
    upstream_billing_probe_batch_size: normalizeNumber(form.upstream_billing_probe_batch_size, 20, 1, 20),
    upstream_billing_probe_concurrency: normalizeNumber(form.upstream_billing_probe_concurrency, 4, 1, 4),
    openai_fast_policy_settings: buildOpenAIFastPolicySettings(),
    risk_control_enabled: form.risk_control_enabled,
    cyber_session_block_enabled: form.cyber_session_block_enabled,
    cyber_session_block_ttl_seconds: normalizeNumber(form.cyber_session_block_ttl_seconds, 3600, 60, 604800),
    channel_monitor_enabled: form.channel_monitor_enabled,
    channel_monitor_mode: form.channel_monitor_mode,
    channel_monitor_default_interval_seconds: normalizeNumber(form.channel_monitor_default_interval_seconds, 60, 10, 86400),
    channel_monitor_hide_throughput: form.channel_monitor_hide_throughput,
  };

  if (form.turnstile_secret_key.trim()) {
    payload.turnstile_secret_key = form.turnstile_secret_key.trim();
  }

  return payload;
}

async function saveSettings(): Promise<void> {
  if (!openAIFastPolicyForm.priority_detection_by_request_enabled && !openAIFastPolicyForm.priority_detection_by_response_enabled) {
    appStore.showError(localText("根据请求和根据响应至少要开启一个。", "At least one of request or response detection must remain enabled."));
	return;
  }
	const grokThreshold = Number(form.grok_scheduling_threshold_percent);
	if (!Number.isInteger(grokThreshold) || grokThreshold < 1 || grokThreshold > 100) {
		appStore.showError(localText("Grok 调度阈值必须是 1 到 100 的整数。", "Grok scheduling threshold must be an integer from 1 to 100."));
		return;
	}
  saving.value = true;
  try {
    const updated = await adminAPI.settings.updateSettings(buildSettingsPayload());
    applySettings(updated);
    adminSettingsStore.setOpsMonitoringEnabledLocal(updated.ops_monitoring_enabled ?? true);
    adminSettingsStore.setOpsRealtimeMonitoringEnabledLocal(updated.ops_realtime_monitoring_enabled ?? true);
    adminSettingsStore.setOpsQueryModeDefaultLocal(updated.ops_query_mode_default || "auto");
    appStore.showSuccess(localText("设置已保存", "Settings saved"));
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, localText("保存设置失败", "Failed to save settings")));
  } finally {
    saving.value = false;
  }
}

function normalizeNumber(value: unknown, fallback: number, min: number, max: number): number {
  const numeric = Number(value);
  if (!Number.isFinite(numeric)) return fallback;
  return Math.min(max, Math.max(min, Math.floor(numeric)));
}

function normalizeDecimal(value: unknown, fallback: number, min: number, max: number): number {
  const numeric = Number(value);
  if (!Number.isFinite(numeric)) return fallback;
  return Math.min(max, Math.max(min, numeric));
}

function normalizePageSizeOptions(options: unknown): number[] {
  const source = Array.isArray(options) ? options : [10, 20, 50, 100];
  const normalized = source
    .map((value) => normalizeNumber(value, 0, 5, 1000))
    .filter((value) => value > 0);
  return [...new Set(normalized)].sort((a, b) => a - b);
}

function parsePageSizeOptionsInput(value: string): number[] {
  return value
    .split(/[,\s]+/)
    .map((item) => Number(item.trim()))
    .filter((item) => Number.isFinite(item));
}

async function loadAdminApiKeyStatus(): Promise<void> {
  adminApiKeyLoading.value = true;
  adminApiKeyPlaintext.value = "";
  adminApiKeyVisible.value = false;
  try {
    const status = await adminAPI.settings.getAdminApiKey();
    adminApiKeyExists.value = status.exists;
    adminApiKeyMasked.value = status.masked_key || "";
  } catch {
    // 管理员密钥状态读取失败不影响设置页其它功能。
  } finally {
    adminApiKeyLoading.value = false;
  }
}

async function createAdminApiKey(): Promise<void> {
  adminApiKeyOperating.value = true;
  try {
    const result = await adminAPI.settings.regenerateAdminApiKey();
    adminApiKeyPlaintext.value = result.key;
    adminApiKeyVisible.value = true;
    adminApiKeyExists.value = true;
    adminApiKeyMasked.value = `${result.key.substring(0, 10)}...${result.key.slice(-4)}`;
    appStore.showSuccess(localText("管理员密钥已生成", "Admin API key generated"));
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, localText("生成管理员密钥失败", "Failed to create admin API key")));
  } finally {
    adminApiKeyOperating.value = false;
  }
}

async function regenerateAdminApiKey(): Promise<void> {
  if (!window.confirm(localText("确认重新生成管理员 API Key？旧密钥会立即失效。", "Regenerate admin API key? The old key will stop working."))) {
    return;
  }
  await createAdminApiKey();
}

async function deleteAdminApiKey(): Promise<void> {
  if (!window.confirm(localText("确认删除管理员 API Key？", "Delete admin API key?"))) {
    return;
  }
  adminApiKeyOperating.value = true;
  try {
    await adminAPI.settings.deleteAdminApiKey();
    adminApiKeyExists.value = false;
    adminApiKeyMasked.value = "";
    adminApiKeyPlaintext.value = "";
    adminApiKeyVisible.value = false;
    appStore.showSuccess(localText("管理员密钥已删除", "Admin API key deleted"));
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, localText("删除管理员密钥失败", "Failed to delete admin API key")));
  } finally {
    adminApiKeyOperating.value = false;
  }
}

async function readAdminApiKey(): Promise<string> {
  if (adminApiKeyPlaintext.value) return adminApiKeyPlaintext.value;

  adminApiKeyOperating.value = true;
  try {
    const result = await adminAPI.settings.revealAdminApiKey();
    adminApiKeyPlaintext.value = result.key;
    return result.key;
  } finally {
    adminApiKeyOperating.value = false;
  }
}

async function toggleAdminApiKeyVisibility(): Promise<void> {
  if (adminApiKeyVisible.value) {
    // 隐藏时立即丢弃完整值；下一次查看重新向服务端按需读取。
    adminApiKeyVisible.value = false;
    adminApiKeyPlaintext.value = "";
    return;
  }

  try {
    await readAdminApiKey();
    adminApiKeyVisible.value = true;
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, localText("读取管理员密钥失败", "Failed to reveal admin API key")));
  }
}

async function copyAdminApiKey(): Promise<void> {
  try {
    const key = await readAdminApiKey();
    await navigator.clipboard.writeText(key);
    appStore.showSuccess(localText("已复制", "Copied"));
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, localText("复制失败", "Copy failed")));
  }
}

async function loadOverloadCooldownSettings(): Promise<void> {
  overloadCooldownLoading.value = true;
  try {
    Object.assign(overloadCooldownForm, await adminAPI.settings.getOverloadCooldownSettings());
  } finally {
    overloadCooldownLoading.value = false;
  }
}

async function saveOverloadCooldownSettings(): Promise<void> {
  overloadCooldownSaving.value = true;
  try {
    Object.assign(
      overloadCooldownForm,
      await adminAPI.settings.updateOverloadCooldownSettings({
        enabled: overloadCooldownForm.enabled,
        cooldown_minutes: normalizeNumber(overloadCooldownForm.cooldown_minutes, 10, 1, 120),
      }),
    );
    appStore.showSuccess(localText("529 过载冷却已保存", "Overload cooldown saved"));
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, localText("保存 529 过载冷却失败", "Failed to save overload cooldown")));
  } finally {
    overloadCooldownSaving.value = false;
  }
}

async function loadRateLimit429CooldownSettings(): Promise<void> {
  rateLimit429CooldownLoading.value = true;
  try {
    Object.assign(rateLimit429CooldownForm, await adminAPI.settings.getRateLimit429CooldownSettings());
  } finally {
    rateLimit429CooldownLoading.value = false;
  }
}

async function saveRateLimit429CooldownSettings(): Promise<void> {
  rateLimit429CooldownSaving.value = true;
  try {
    Object.assign(
      rateLimit429CooldownForm,
      await adminAPI.settings.updateRateLimit429CooldownSettings({
        enabled: rateLimit429CooldownForm.enabled,
        cooldown_seconds: normalizeNumber(rateLimit429CooldownForm.cooldown_seconds, 5, 1, 3600),
      }),
    );
    appStore.showSuccess(localText("429 限流冷却已保存", "429 cooldown saved"));
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, localText("保存 429 限流冷却失败", "Failed to save 429 cooldown")));
  } finally {
    rateLimit429CooldownSaving.value = false;
  }
}

async function loadFallbackQueueSettings(): Promise<void> {
  fallbackQueueLoading.value = true;
  try {
    Object.assign(fallbackQueueForm, await adminAPI.settings.getFallbackQueueSettings());
  } finally {
    fallbackQueueLoading.value = false;
  }
}

async function saveFallbackQueueSettings(): Promise<void> {
  fallbackQueueSaving.value = true;
  try {
    Object.assign(
      fallbackQueueForm,
      await adminAPI.settings.updateFallbackQueueSettings({
        enabled: fallbackQueueForm.enabled,
        max_waiting: normalizeNumber(fallbackQueueForm.max_waiting, 100, 1, 100000),
        wait_timeout_seconds: normalizeNumber(fallbackQueueForm.wait_timeout_seconds, 120, 1, 3600),
      }),
    );
    appStore.showSuccess(localText("账号兜底排队已保存", "Fallback queue saved"));
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, localText("保存账号兜底排队失败", "Failed to save fallback queue")));
  } finally {
    fallbackQueueSaving.value = false;
  }
}

async function loadStreamTimeoutSettings(): Promise<void> {
  streamTimeoutLoading.value = true;
  try {
    Object.assign(streamTimeoutForm, await adminAPI.settings.getStreamTimeoutSettings());
  } finally {
    streamTimeoutLoading.value = false;
  }
}

async function saveStreamTimeoutSettings(): Promise<void> {
  streamTimeoutSaving.value = true;
  try {
    Object.assign(
      streamTimeoutForm,
      await adminAPI.settings.updateStreamTimeoutSettings({
        enabled: streamTimeoutForm.enabled,
        action: streamTimeoutForm.action,
        temp_unsched_minutes: normalizeNumber(streamTimeoutForm.temp_unsched_minutes, 5, 1, 1440),
        threshold_count: normalizeNumber(streamTimeoutForm.threshold_count, 3, 1, 100),
        threshold_window_minutes: normalizeNumber(streamTimeoutForm.threshold_window_minutes, 10, 1, 1440),
      }),
    );
    appStore.showSuccess(localText("流超时策略已保存", "Stream timeout saved"));
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, localText("保存流超时策略失败", "Failed to save stream timeout")));
  } finally {
    streamTimeoutSaving.value = false;
  }
}

async function loadRectifierSettings(): Promise<void> {
  rectifierLoading.value = true;
  try {
    Object.assign(rectifierForm, await adminAPI.settings.getRectifierSettings());
    if (!Array.isArray(rectifierForm.apikey_signature_patterns)) {
      rectifierForm.apikey_signature_patterns = [];
    }
    rectifierPatternsInput.value = rectifierForm.apikey_signature_patterns.join("\n");
  } finally {
    rectifierLoading.value = false;
  }
}

async function saveRectifierSettings(): Promise<void> {
  rectifierSaving.value = true;
  try {
    const patterns = rectifierPatternsInput.value
      .split(/\r?\n/)
      .map((item) => item.trim())
      .filter(Boolean);
    Object.assign(
      rectifierForm,
      await adminAPI.settings.updateRectifierSettings({
        enabled: rectifierForm.enabled,
        thinking_signature_enabled: rectifierForm.thinking_signature_enabled,
        thinking_budget_enabled: rectifierForm.thinking_budget_enabled,
        apikey_signature_enabled: rectifierForm.apikey_signature_enabled,
        apikey_signature_patterns: patterns,
      }),
    );
    rectifierPatternsInput.value = rectifierForm.apikey_signature_patterns.join("\n");
    appStore.showSuccess(localText("修正器设置已保存", "Rectifier settings saved"));
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, localText("保存修正器设置失败", "Failed to save rectifier settings")));
  } finally {
    rectifierSaving.value = false;
  }
}

async function loadBetaPolicySettings(): Promise<void> {
  betaPolicyLoading.value = true;
  try {
    const settings = await adminAPI.settings.getBetaPolicySettings();
    betaPolicyForm.rules = settings.rules || [];
  } finally {
    betaPolicyLoading.value = false;
  }
}

function addBetaPolicyRule(): void {
  betaPolicyForm.rules.push({
    beta_token: "",
    action: "pass",
    scope: "all",
    error_message: "",
    model_whitelist: [],
    fallback_action: "pass",
    fallback_error_message: "",
  });
}

function removeBetaPolicyRule(index: number): void {
  betaPolicyForm.rules.splice(index, 1);
}

function updateBetaWhitelist(rule: BetaPolicyRule, event: Event): void {
  const value = (event.target as HTMLInputElement).value;
  rule.model_whitelist = value
    .split(",")
    .map((item) => item.trim())
    .filter(Boolean);
}

async function saveBetaPolicySettings(): Promise<void> {
  betaPolicySaving.value = true;
  try {
    const cleanedRules = betaPolicyForm.rules
      .filter((rule) => rule.beta_token.trim())
      .map((rule) => {
        const whitelist = rule.model_whitelist?.filter((item) => item.trim()) || [];
        return {
          beta_token: rule.beta_token.trim(),
          action: rule.action,
          scope: rule.scope,
          error_message: rule.error_message?.trim() || undefined,
          model_whitelist: whitelist.length > 0 ? whitelist : undefined,
          fallback_action: whitelist.length > 0 ? rule.fallback_action || "pass" : undefined,
          fallback_error_message: rule.fallback_error_message?.trim() || undefined,
        };
      });
    const updated = await adminAPI.settings.updateBetaPolicySettings({ rules: cleanedRules });
    betaPolicyForm.rules = updated.rules || [];
    appStore.showSuccess(localText("Beta 策略已保存", "Beta policy saved"));
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, localText("保存 Beta 策略失败", "Failed to save beta policy")));
  } finally {
    betaPolicySaving.value = false;
  }
}

watch(
  () => rectifierPatternsInput.value,
  (value) => {
    rectifierForm.apikey_signature_patterns = value
      .split(/\r?\n/)
      .map((item) => item.trim())
      .filter(Boolean);
  },
);

onMounted(() => {
  void Promise.allSettled([
    loadSettings(),
    loadGatewayLiteConfig(),
    loadBillingSettlementSettings(),
    loadCompositeGroupsSettings(),
    loadCodexTailBurstSettings(),
    loadAdminApiKeyStatus(),
    loadOverloadCooldownSettings(),
    loadRateLimit429CooldownSettings(),
    loadFallbackQueueSettings(),
    loadStreamTimeoutSettings(),
    loadRectifierSettings(),
    loadBetaPolicySettings(),
  ]);
});

const SwitchCard = defineComponent({
  name: "SwitchCard",
  props: {
    modelValue: { type: Boolean, required: true },
    title: { type: String, required: true },
  },
  emits: ["update:modelValue"],
  setup(props, { emit }) {
    return () =>
      h("div", { class: "switch-card" }, [
        h("span", { class: "switch-card-title" }, props.title),
        h(Toggle, {
          modelValue: props.modelValue,
          "onUpdate:modelValue": (value: boolean) => emit("update:modelValue", value),
        }),
      ]);
  },
});

const PolicyCard = defineComponent({
  name: "PolicyCard",
  props: {
    title: { type: String, required: true },
    description: { type: String, required: true },
    loading: { type: Boolean, required: true },
    saving: { type: Boolean, required: true },
  },
  emits: ["save"],
  setup(props, { slots, emit }) {
    return () =>
      h("section", { class: "card" }, [
        h("div", { class: "border-b border-gray-100 px-6 py-4 dark:border-dark-700" }, [
          h("h2", { class: "text-lg font-semibold text-gray-900 dark:text-white" }, props.title),
          h("p", { class: "mt-1 text-sm text-gray-500 dark:text-gray-400" }, props.description),
        ]),
        h("div", { class: "space-y-5 p-6" }, [
          props.loading
            ? h("div", { class: "inline-loading" }, [
                h("span", { class: "spinner-sm" }),
                localText("加载中", "Loading"),
              ])
            : slots.default?.(),
          h("div", { class: "flex justify-end" }, [
            h(
              "button",
              {
                type: "button",
                class: "btn btn-secondary btn-sm",
                disabled: props.loading || props.saving,
                onClick: () => emit("save"),
              },
              props.saving ? localText("保存中", "Saving") : localText("保存", "Save"),
            ),
          ]),
        ]),
      ]);
  },
});
</script>

<style scoped>
.field {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}

.field > span {
  font-size: 0.875rem;
  font-weight: 500;
  color: rgb(55 65 81);
}

.dark .field > span {
  color: rgb(209 213 219);
}

.textarea {
  width: 100%;
  border-radius: 0.375rem;
  border: 1px solid rgb(209 213 219);
  background: white;
  padding: 0.625rem 0.75rem;
  font-size: 0.875rem;
  color: rgb(17 24 39);
}

.textarea:focus {
  border-color: rgb(59 130 246);
  outline: none;
  box-shadow: 0 0 0 1px rgb(59 130 246);
}

.dark .textarea {
  border-color: rgb(75 85 99);
  background: rgb(31 41 55);
  color: rgb(243 244 246);
}

.setting-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
}

.setting-row h3 {
  font-size: 0.875rem;
  font-weight: 500;
  color: rgb(17 24 39);
}

.setting-row p {
  margin-top: 0.25rem;
  font-size: 0.875rem;
  color: rgb(107 114 128);
}

.dark .setting-row h3 {
  color: white;
}

.dark .setting-row p {
  color: rgb(156 163 175);
}

.switch-card {
  display: flex;
  min-height: 3.5rem;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  border-radius: 0.5rem;
  border: 1px solid rgb(229 231 235);
  padding: 0.75rem 1rem;
}

.dark .switch-card {
  border-color: rgb(55 65 81);
}

.switch-card-title {
  font-size: 0.875rem;
  font-weight: 500;
  color: rgb(31 41 55);
}

.priority-policy-panel {
  border: 1px solid rgb(229 231 235);
  border-radius: 0.5rem;
  padding: 1rem;
}

.priority-policy-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 1rem;
}

.priority-policy-head h3 {
  font-size: 0.875rem;
  font-weight: 600;
  color: rgb(17 24 39);
}

.priority-policy-head p,
.priority-policy-note {
  margin-top: 0.25rem;
  font-size: 0.8125rem;
  line-height: 1.5;
  color: rgb(107 114 128);
}

.priority-policy-badge {
  flex-shrink: 0;
  border-radius: 9999px;
  background: rgb(219 234 254);
  padding: 0.25rem 0.625rem;
  font-size: 0.75rem;
  font-weight: 600;
  color: rgb(29 78 216);
}

.priority-detection-grid {
  display: grid;
  gap: 0.75rem;
  margin-top: 1rem;
}

.priority-detection-card {
  display: flex;
  min-height: 5.25rem;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  border: 1px solid rgb(229 231 235);
  border-radius: 0.5rem;
  padding: 0.875rem;
}

.priority-detection-title {
  display: flex;
  align-items: center;
  gap: 0.25rem;
}

.priority-detection-title strong,
.priority-rules-head strong,
.priority-rule-head strong {
  font-size: 0.875rem;
  font-weight: 600;
  color: rgb(31 41 55);
}

.priority-detection-card p,
.priority-rules-head p {
  margin-top: 0.25rem;
  font-size: 0.75rem;
  line-height: 1.5;
  color: rgb(107 114 128);
}

.priority-rules-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  margin-top: 1.25rem;
  padding-top: 1rem;
  border-top: 1px solid rgb(229 231 235);
}

.priority-rule-list {
  display: grid;
  gap: 0.875rem;
  margin-top: 1rem;
}

.priority-rule-card {
  border: 1px solid rgb(229 231 235);
  border-radius: 0.5rem;
  padding: 1rem;
  background: rgb(249 250 251);
}

.priority-rule-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  margin-bottom: 1rem;
}

.priority-rule-head > div:first-child {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 0.625rem;
}

.priority-rule-head code {
  overflow: hidden;
  max-width: 18rem;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 0.6875rem;
  color: rgb(107 114 128);
}

.priority-rule-actions {
  display: flex;
  flex-shrink: 0;
  gap: 0.375rem;
}

.priority-rule-actions .icon-button {
  display: inline-flex;
  width: 2rem;
  height: 2rem;
  align-items: center;
  justify-content: center;
  border: 1px solid rgb(229 231 235);
  border-radius: 0.375rem;
  background: white;
  transition: border-color 150ms ease, background-color 150ms ease;
}

.priority-rule-actions .icon-button:hover:not(:disabled) {
  border-color: rgb(147 197 253);
  background: rgb(239 246 255);
}

.priority-rule-actions .icon-button:disabled {
  cursor: not-allowed;
  opacity: 0.35;
}

.dark .priority-policy-panel,
.dark .priority-detection-card,
.dark .priority-rule-card {
  border-color: rgb(55 65 81);
}

.dark .priority-policy-head h3,
.dark .priority-detection-title strong,
.dark .priority-rules-head strong,
.dark .priority-rule-head strong {
  color: rgb(243 244 246);
}

.dark .priority-policy-head p,
.dark .priority-policy-note,
.dark .priority-detection-card p,
.dark .priority-rules-head p,
.dark .priority-rule-head code {
  color: rgb(156 163 175);
}

.dark .priority-policy-badge {
  background: rgba(37, 99, 235, 0.2);
  color: rgb(147 197 253);
}

.dark .priority-rules-head {
  border-color: rgb(55 65 81);
}

.dark .priority-rule-card {
  background: rgba(17, 24, 39, 0.45);
}

.dark .priority-rule-actions .icon-button {
  border-color: rgb(55 65 81);
  background: rgb(31 41 55);
}

.dark .priority-rule-actions .icon-button:hover:not(:disabled) {
  border-color: rgb(59 130 246);
  background: rgba(30, 64, 175, 0.18);
}

@media (min-width: 768px) {
  .priority-detection-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

.dark .switch-card-title {
  color: rgb(229 231 235);
}

.upstream-billing-panel {
  overflow: hidden;
  border: 1px solid rgb(226 232 240);
  border-radius: 0.75rem;
  background: rgb(248 250 252);
  padding: 1rem;
}

.preset-grid {
  display: grid;
  gap: 1rem;
  margin-top: 1rem;
}

.preset-group > span,
.weight-row > span {
  font-size: 0.75rem;
  font-weight: 600;
  color: rgb(71 85 105);
}

.preset-buttons {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
  margin-top: 0.5rem;
}

.preset-button {
  min-height: 2rem;
  border: 1px solid rgb(203 213 225);
  border-radius: 0.5rem;
  background: white;
  padding: 0.375rem 0.75rem;
  font-size: 0.75rem;
  font-weight: 600;
  color: rgb(71 85 105);
  transition: border-color 150ms ease, background-color 150ms ease, color 150ms ease;
}

.preset-button:hover,
.preset-button.active {
  border-color: rgb(16 185 129);
  background: rgb(236 253 245);
  color: rgb(4 120 87);
}

.upstream-scheduler-row {
  margin-top: 1rem;
  border-top: 1px solid rgb(226 232 240);
  padding-top: 1rem;
}

.scheduler-weight-box {
  margin-top: 1rem;
}

.weight-row {
  display: grid;
  grid-template-columns: minmax(7rem, 1fr) minmax(8rem, 2fr) 2.5rem;
  align-items: center;
  gap: 0.75rem;
  margin-top: 0.75rem;
}

.weight-row input[type="range"] {
  accent-color: rgb(16 185 129);
}

.weight-row input[type="range"]:disabled {
  cursor: not-allowed;
  opacity: 0.35;
}

.weight-row strong {
  text-align: right;
  font-size: 0.75rem;
  color: rgb(4 120 87);
}

.dark .upstream-billing-panel {
  border-color: rgb(55 65 81);
  background: rgba(17, 24, 39, 0.45);
}

.dark .preset-group > span,
.dark .weight-row > span {
  color: rgb(203 213 225);
}

.dark .preset-button {
  border-color: rgb(75 85 99);
  background: rgb(31 41 55);
  color: rgb(203 213 225);
}

.dark .preset-button:hover,
.dark .preset-button.active {
  border-color: rgb(52 211 153);
  background: rgba(6, 78, 59, 0.35);
  color: rgb(110 231 183);
}

.dark .upstream-scheduler-row {
  border-color: rgb(55 65 81);
}

@media (min-width: 768px) {
  .preset-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}

.inline-loading {
  display: inline-flex;
  align-items: center;
  gap: 0.5rem;
  font-size: 0.875rem;
  color: rgb(107 114 128);
}

.spinner-sm {
  display: inline-block;
  height: 1rem;
  width: 1rem;
  flex-shrink: 0;
  animation: spin 1s linear infinite;
  border-radius: 9999px;
  border-bottom: 2px solid currentColor;
}

.empty-hint {
  border-radius: 0.5rem;
  border: 1px dashed rgb(209 213 219);
  padding: 1rem;
  font-size: 0.875rem;
  color: rgb(107 114 128);
}

.dark .empty-hint {
  border-color: rgb(75 85 99);
  color: rgb(156 163 175);
}

.settings-tabs-shell {
  position: sticky;
  top: 0;
  z-index: 10;
  margin: -0.25rem -0.25rem 0;
  border-radius: 0.75rem;
  border: 1px solid rgba(229, 231, 235, 0.9);
  background: rgba(255, 255, 255, 0.92);
  padding: 0.25rem;
  backdrop-filter: blur(16px);
}

.settings-tabs-scroll {
  overflow-x: auto;
}

.settings-tabs-scroll::-webkit-scrollbar {
  display: none;
}

.settings-tabs {
  display: inline-flex;
  min-width: 100%;
  gap: 0.25rem;
}

.settings-tab {
  display: inline-flex;
  min-height: 2.75rem;
  min-width: 7.5rem;
  flex: 1 0 auto;
  align-items: center;
  justify-content: center;
  gap: 0.5rem;
  border-radius: 0.5rem;
  padding: 0.625rem 0.875rem;
  font-size: 0.875rem;
  font-weight: 500;
  color: rgb(75 85 99);
  transition: background-color 0.15s ease, color 0.15s ease, box-shadow 0.15s ease;
}

.settings-tab:hover {
  background: rgb(249 250 251);
  color: rgb(17 24 39);
}

.settings-tab-active {
  background: white;
  color: rgb(37 99 235);
  box-shadow: 0 1px 2px rgba(15, 23, 42, 0.08);
}

.settings-tab-icon {
  display: inline-flex;
  flex-shrink: 0;
}

.settings-tab-label {
  white-space: nowrap;
}

.dark .settings-tabs-shell {
  border-color: rgba(55, 65, 81, 0.9);
  background: rgba(17, 24, 39, 0.9);
}

.dark .settings-tab {
  color: rgb(209 213 219);
}

.dark .settings-tab:hover {
  background: rgb(31 41 55);
  color: white;
}

.dark .settings-tab-active {
  background: rgb(31 41 55);
  color: rgb(96 165 250);
}

@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}

@media (max-width: 640px) {
  .setting-row {
    align-items: flex-start;
  }

  .settings-tab {
    min-width: 6.5rem;
  }
}
</style>
