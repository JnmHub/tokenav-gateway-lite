<script setup>
import GroupDisplayIdentityFields from './GroupDisplayIdentityFields.vue'
/* 分组编辑弹窗（一期表单 + 二期 A 深化）
 * 分区（a-collapse）：基础 / 调度与限额 / 平台特性 / 模型映射与路由 / 专属定价
 * 全部状态与保存逻辑来自 useGroupEditor（props.editor），本组件只负责模板与常量。
 * 字段契约来源：旧 GroupsView.vue + components/admin/group/*，详见各子组件头注释。
 */
import { computed } from 'vue'
import W12Dialog from '../design/W12Dialog.vue'
import GroupModelMappingFields from './GroupModelMappingFields.vue'
import GroupModelRoutingFields from './GroupModelRoutingFields.vue'
import GroupPricingFields from './GroupPricingFields.vue'
import GroupMediaPricingFields from './GroupMediaPricingFields.vue'
// v0.2.5：分组利润控制（开关 + 最低毛利率 / 安全缓冲）
import GroupProfitControlFields from './GroupProfitControlFields.vue'
import GroupResponseModelFields from './GroupResponseModelFields.vue'
// 二期 B：Messages 调度 / 推理力度 / 模型白名单
import MessagesDispatchFields from './fields/MessagesDispatchFields.vue'
import ReasoningEffortFields from './fields/ReasoningEffortFields.vue'
import ModelsListFields from './fields/ModelsListFields.vue'

const props = defineProps({
  editor: { type: Object, required: true }, // useGroupEditor() 返回值
})
// composable 返回的 ref 实例稳定，解构保持响应性；模板中自动解包
const {
  editOpen, editSaving, editForm, activeKeys,
  pricingForm, pricingLoading, pricingLoadError, pricingShared, retryPricing,
  routingEnabled, routingRules,
  routeHealth, resetRouteHealth,
  mappingCandidates, candidatesLoading,
  apiCandidates,
  fallbackGroupOptions, invalidFallbackOptions,
  failoverSelectOptions, copyAccountsOptions, compositeEnabled, compositeReady,
  save, closeEditor, beforeClose,
} = props.editor

// ===== 常量选项（与旧 GroupsView/constants 对齐，一期已核实） =====
const platformOptions = computed(() => [
  { value: 'anthropic', label: 'Anthropic' },
  { value: 'openai', label: 'OpenAI' },
  { value: 'gemini', label: 'Gemini' },
  { value: 'antigravity', label: 'Antigravity' },
  { value: 'grok', label: 'Grok' },
  { value: 'kimi', label: 'Kimi' },
  { value: 'zhipu', label: '智谱 GLM' },
  { value: 'deepseek', label: 'DeepSeek' },
  { value: 'composite', label: compositeEnabled.value ? '聚合模型（多平台）' : '聚合模型（多平台）· 总开关未启用', disabled: !compositeReady.value || !compositeEnabled.value },
])
const statusOptions = [
  { value: 'active', label: '正常' },
  { value: 'inactive', label: '停用' },
]
const subTypeOptions = [
  { value: 'standard', label: '标准（余额）' },
  { value: 'subscription', label: '订阅（配额）' },
]
const minimumTierOptions = [
  { value: null, label: '继承节点' },
  { value: 0, label: 'T0' },
  { value: 1, label: 'T1' },
  { value: 2, label: 'T2' },
]
const concurrencyPresets = [0, 1, 2, 3, 5, 10]
const peakTimezones = [
  { value: 'Asia/Shanghai', label: '上海' },
  { value: 'America/Los_Angeles', label: '洛杉矶' },
  { value: 'Asia/Tokyo', label: '东京' },
  { value: 'UTC', label: 'UTC' },
]
const peakMultiplierPresets = [1, 1.25, 1.5, 2]
const billingSourceOptions = [
  { value: 'requested', label: '按用户请求模型计费', hint: '客户请求什么模型，就按这个模型的价格扣费。（推荐）' },
  { value: 'channel_mapped', label: '按映射后模型计费', hint: '按分组和渠道映射完成后的模型价格扣费。' },
  { value: 'upstream', label: '按实际上游模型计费', hint: '按上游实际使用的模型价格扣费。' },
  { value: 'response_model', label: '按响应声明模型计费', hint: '只在声明唯一、有可信价格且不会抬高费用时采用；否则自动回退。' },
]
const scopeOptions = [
  { label: 'Claude', value: 'claude' },
  { label: 'Gemini Text', value: 'gemini_text' },
  { label: 'Gemini Image', value: 'gemini_image' },
]
const failoverStrategyOptions = [
  { value: 'original_model_first', label: '优先保持原模型（推荐）' },
  { value: 'current_group_first', label: '优先留在当前分组' },
]

// ===== 平台显隐（一期已核实） =====
const schedulingOptions = computed(() => {
  const def = { value: 'default', label: '默认（负载感知）', hint: '优先使用更空闲、优先级更高的账号，并尽量保持普通会话稳定。' }
  const strict = { value: 'strict_priority', label: '严格优先级', hint: '先用最高优先级中仍有并发余量的账号；该层全部满载后，才进入下一层。' }
  const rnd = { value: 'random', label: '完全随机', hint: '每次请求都在所有合格账号中等概率随机。' }
  return ['openai', 'grok'].includes(editForm.value.platform) ? [def, strict, rnd] : [def, rnd]
})
const showAccountFilter = computed(() => ['openai', 'antigravity', 'anthropic', 'gemini'].includes(editForm.value.platform))
const supportsLive = computed(() => ['openai', 'composite'].includes(editForm.value.platform))
// 推理力度策略仅 openai / composite（与后端 reasoningEffortValuesForPlatform 一致）
const supportsReasoningEffort = computed(() => ['openai', 'composite'].includes(editForm.value.platform))
const peakSameTime = computed(() => editForm.value.peak_rate_enabled && editForm.value.peak_start === editForm.value.peak_end)
const peakCrossMidnight = computed(() =>
  editForm.value.peak_rate_enabled && editForm.value.peak_start && editForm.value.peak_end && editForm.value.peak_start > editForm.value.peak_end)
// 媒体计价区显示（组件内部再分平台小节）
const showMediaPricing = computed(() =>
  ['antigravity', 'gemini', 'openai', 'grok', 'composite'].includes(editForm.value.platform))
// Claude Code 降级分组 / 无效请求兜底显示条件（旧 1880-1899、1550-1562）
const showFallbackGroup = computed(() =>
  editForm.value.platform === 'anthropic' && editForm.value.claude_code_only)
const showInvalidFallback = computed(() =>
  ['anthropic', 'antigravity'].includes(editForm.value.platform) && editForm.value.subscription_type !== 'subscription')
// 平台切换副作用：把不适用平台的开关复位（一期 watch 迁移）
const onPlatformChange = (p) => {
  // [修复] 受控下拉必须先把选中平台写回 editForm，否则选择不生效（下方副作用与 watcher 都依赖此值）
  editForm.value.platform = p
  if (editForm.value.scheduling_mode === 'strict_priority' && !['openai', 'grok'].includes(p)) {
    editForm.value.scheduling_mode = 'default'
  }
  if (!['openai', 'antigravity', 'anthropic', 'gemini'].includes(p)) {
    editForm.value.require_oauth_only = false
    editForm.value.require_privacy_set = false
  }
  if (p !== 'openai') editForm.value.allow_openai_long_context_billing = false
  if (!['openai', 'composite'].includes(p)) editForm.value.allow_live = false
}
const patchForm = (values) => Object.assign(editForm.value, values)
// 处理函数在脚本里写 ref.value，避免模板自动解包后误写到定价对象的 value 字段。
const updatePricing = (state) => { pricingForm.value = state }
</script>

<template>
  <W12Dialog
    :open="editOpen" :close-key="editForm.id" :title="editForm.id ? '编辑分组' : '新增分组'" :confirm-loading="editSaving" :busy="editSaving"
    ok-text="保存" cancel-text="取消" width="860px" wrap-class-name="group-edit-modal"
    :before-close="beforeClose" @ok="save" @cancel="closeEditor"
  >
    <a-collapse v-model:active-key="activeKeys" ghost expand-icon-position="end" class="g-collapse">
      <!-- ============ 基础 ============ -->
      <a-collapse-panel key="basics" header="基础">
        <a-form layout="vertical" size="small" class="gform">
          <div class="grid2">
            <a-form-item label="名称" required>
              <a-input v-model:value="editForm.name" placeholder="分组名称" />
            </a-form-item>
            <a-form-item label="平台">
              <a-select :value="editForm.platform" :options="platformOptions" :disabled="!!editForm.id" @change="onPlatformChange" />
              <div v-if="editForm.id" class="hint">创建后不可更改平台</div>
            </a-form-item>
          </div>
          <div class="grid2">
            <a-form-item label="状态">
              <a-select v-model:value="editForm.status" :options="statusOptions" />
            </a-form-item>
            <a-form-item label="订阅类型">
              <a-select v-model:value="editForm.subscription_type" :options="subTypeOptions" :disabled="!!editForm.id" />
              <div v-if="editForm.id" class="hint">分组创建后无法修改计费类型</div>
            </a-form-item>
          </div>
          <div v-if="editForm.subscription_type === 'subscription'" class="grid3">
            <a-form-item label="每日限额（USD）">
              <a-input-number v-model:value="editForm.daily_limit_usd" :min="0" :step="0.01" style="width: 100%" placeholder="无限制" />
            </a-form-item>
            <a-form-item label="每周限额（USD）">
              <a-input-number v-model:value="editForm.weekly_limit_usd" :min="0" :step="0.01" style="width: 100%" placeholder="无限制" />
            </a-form-item>
            <a-form-item label="每月限额（USD）">
              <a-input-number v-model:value="editForm.monthly_limit_usd" :min="0" :step="0.01" style="width: 100%" placeholder="无限制" />
            </a-form-item>
          </div>
          <a-form-item v-if="copyAccountsOptions.length" label="从已有分组复制账号">
            <a-select v-model:value="editForm.copy_accounts_from_group_ids" mode="multiple" :options="copyAccountsOptions" placeholder="点击选择来源分组" />
            <div class="hint">{{ editForm.id ? '保存后将替换本分组的账号绑定；留空保留当前绑定。' : '创建后绑定来源分组账号，保留账号已有的其他分组绑定。' }}</div>
          </a-form-item>
          <a-form-item label="描述">
            <a-textarea v-model:value="editForm.description" :rows="2" />
          </a-form-item>
          <a-form-item label="对外品牌与图标（用户侧展示）">
            <GroupDisplayIdentityFields
              :platform="editForm.platform"
              v-model:brand="editForm.display_brand"
              v-model:icon="editForm.display_icon" />
          </a-form-item>
          <div class="grid2">
            <a-form-item label="计费倍率">
              <a-input-number v-model:value="editForm.rate_multiplier" :min="0.001" :step="0.1" style="width: 100%" />
            </a-form-item>
          </div>
          <div class="frow">
            <div class="frow-main">
              <div class="frow-t">本地分组</div>
              <div class="frow-d">不上报主站，仅本地 API Key 可用</div>
            </div>
            <a-switch v-model:checked="editForm.local_only" size="small" />
          </div>
          <div v-if="editForm.subscription_type !== 'subscription'" class="frow">
            <div class="frow-main">
              <div class="frow-t">专属分组（独占）</div>
              <div class="frow-d">开启后仅专属用户可选用此分组</div>
            </div>
            <a-switch v-model:checked="editForm.is_exclusive" size="small" />
          </div>
          <a-form-item label="分组最低等级">
            <div class="seg">
              <button
                v-for="o in minimumTierOptions" :key="o.label" type="button" class="seg-btn"
                :class="{ on: editForm.minimum_tier === o.value }" @click="editForm.minimum_tier = o.value"
              >{{ o.label }}</button>
            </div>
            <div class="hint">主站会与节点等级取较高值，并在用户选择分组时显示最终等级。</div>
          </a-form-item>
        </a-form>
      </a-collapse-panel>

      <!-- ============ 调度与限额 ============ -->
      <a-collapse-panel key="scheduling" header="调度与限额">
        <a-form layout="vertical" size="small" class="gform">
          <a-form-item label="账号调度规则">
            <div class="cards">
              <button
                v-for="o in schedulingOptions" :key="o.value" type="button" class="card"
                :class="{ on: editForm.scheduling_mode === o.value }" @click="editForm.scheduling_mode = o.value"
              >
                <span class="card-t">{{ o.label }}</span>
                <span class="card-d">{{ o.hint }}</span>
              </button>
            </div>
          </a-form-item>
          <a-form-item label="每用户并发上限">
            <div class="seg">
              <button
                v-for="v in concurrencyPresets" :key="v" type="button" class="seg-btn"
                :class="{ on: Number(editForm.per_user_concurrency) === v }" @click="editForm.per_user_concurrency = v"
              >{{ v === 0 ? '不限' : v }}</button>
            </div>
            <a-input-number v-model:value="editForm.per_user_concurrency" :min="0" :max="100000" style="width: 180px; margin-top: 6px" />
            <div class="hint">同一主站用户的所有 API Key 共享此上限；0 表示不限。</div>
          </a-form-item>
          <a-form-item label="分组 RPM 限制">
            <a-input-number v-model:value="editForm.rpm_limit" :min="0" :step="1" style="width: 180px" />
            <div class="hint">每用户在本分组每分钟最大请求数，0 = 不限制；一旦设置即接管该用户的限流（覆盖用户级 rpm_limit）。</div>
          </a-form-item>
          <div class="frow">
            <div class="frow-main">
              <div class="frow-t">高峰时段倍率</div>
              <div class="frow-d">默认关闭。开启后只在设定时段内叠加 Token 计费倍率，图片和视频按次计费不受影响。</div>
            </div>
            <a-switch v-model:checked="editForm.peak_rate_enabled" size="small" />
          </div>
          <template v-if="editForm.peak_rate_enabled">
            <div class="grid2">
              <a-form-item label="开始时间">
                <input v-model="editForm.peak_start" type="time" class="tin" step="60" />
              </a-form-item>
              <a-form-item label="结束时间">
                <input v-model="editForm.peak_end" type="time" class="tin" step="60" />
              </a-form-item>
            </div>
            <p v-if="peakSameTime" class="err-hint">开始和结束时间不能相同。</p>
            <p v-else-if="peakCrossMidnight" class="hint">这是跨夜时段：从当天开始时间持续到次日结束时间。</p>
            <a-form-item label="计费时区">
              <div class="seg wrap">
                <button
                  v-for="tz in peakTimezones" :key="tz.value" type="button" class="seg-btn"
                  :class="{ on: editForm.peak_timezone === tz.value }" :title="tz.value" @click="editForm.peak_timezone = tz.value"
                >{{ tz.label }}</button>
              </div>
              <div class="hint">所有节点都按这个时区判断，避免美国、日本节点产生不同扣费结果。</div>
            </a-form-item>
            <a-form-item label="高峰叠加倍率">
              <div class="seg">
                <button
                  v-for="m in peakMultiplierPresets" :key="m" type="button" class="seg-btn"
                  :class="{ on: Number(editForm.peak_rate_multiplier) === m }" @click="editForm.peak_rate_multiplier = m"
                >{{ m }}x</button>
              </div>
              <a-input-number
                v-model:value="editForm.peak_rate_multiplier" :min="0.001" :max="100" :step="0.05"
                style="width: 180px; margin-top: 6px"
              />
              <div class="hint">高峰时最终 Token 倍率 = 分组或用户专属倍率 × 这个倍率。</div>
            </a-form-item>
          </template>
          <a-form-item label="计费模型来源">
            <div class="cards">
              <button
                v-for="o in billingSourceOptions" :key="o.value" type="button" class="card"
                :class="{ on: editForm.billing_model_source === o.value }" @click="editForm.billing_model_source = o.value"
              >
                <span class="card-t">{{ o.label }}</span>
                <span class="card-d">{{ o.hint }}</span>
              </button>
            </div>
            <div class="hint">选择价格表按哪个模型名称计算；这里的选择优先于渠道管理中的旧设置。</div>
          </a-form-item>
        </a-form>
      </a-collapse-panel>

      <!-- ============ 平台特性 ============ -->
      <a-collapse-panel key="platform" header="平台特性">
        <a-form layout="vertical" size="small" class="gform">
          <div class="frow">
            <div class="frow-main">
              <div class="frow-t">允许命中关键词的请求</div>
              <div class="frow-d">开启后，本分组可处理命中全部强拦截关键词的请求；语种、等级、余额、模型和账号规则仍照常检查。</div>
            </div>
            <a-switch v-model:checked="editForm.allow_blocked_keyword_requests" size="small" />
          </div>
          <template v-if="showAccountFilter">
            <div class="frow">
              <div class="frow-main">
                <div class="frow-t">仅允许 OAuth 账号</div>
                <div class="frow-d">{{ editForm.require_oauth_only ? '已启用 — 排除 API Key 类型账号' : '未启用' }}</div>
              </div>
              <a-switch v-model:checked="editForm.require_oauth_only" size="small" />
            </div>
            <div class="frow">
              <div class="frow-main">
                <div class="frow-t">仅允许隐私保护已设置的账号</div>
                <div class="frow-d">{{ editForm.require_privacy_set ? '已启用 — Privacy 未设置的账号将被排除' : '未启用' }}</div>
              </div>
              <a-switch v-model:checked="editForm.require_privacy_set" size="small" />
            </div>
          </template>
          <!-- OpenAI 专属 -->
          <template v-if="editForm.platform === 'openai'">
            <div class="frow">
              <div class="frow-main">
                <div class="frow-t">启用长上下文计费</div>
                <div class="frow-d">控制超过模型长上下文阈值的请求使用哪类上游账号和价格档位。</div>
              </div>
              <a-switch v-model:checked="editForm.allow_openai_long_context_billing" size="small" />
            </div>
            <div v-if="supportsLive" class="frow">
              <div class="frow-main">
                <div class="frow-t">允许当前分组使用 Live（实时会话）</div>
                <div class="frow-d">实验能力，仍受设置页的节点总开关约束，当前不会按 Token 收费。</div>
              </div>
              <a-switch v-model:checked="editForm.allow_live" size="small" />
            </div>
            <!-- OpenAI Messages 调度三件套（二期 B）：开关 / 默认映射模型 / 调度模型配置 -->
            <MessagesDispatchFields :form="editForm" @patch="patchForm" />
            <a-form-item label="OpenAI 分组兜底模型">
              <a-input v-model:value="editForm.fallback_model" :maxlength="100" placeholder="例如：gpt-5.6-luna（留空关闭）" />
              <div class="hint">仅 OpenAI 文本请求在原模型无可用账号时尝试一次，不改正常计费和响应声明。</div>
            </a-form-item>
            <div class="grid2">
              <a-form-item label="可用性兜底分组">
                <a-select v-model:value="editForm.failover_group_id" :options="failoverSelectOptions" />
              </a-form-item>
              <a-form-item label="兜底顺序">
                <a-select v-model:value="editForm.failover_strategy" :options="failoverStrategyOptions" :disabled="!editForm.failover_group_id" />
              </a-form-item>
            </div>
            <div class="hint pad">仅在账号耗尽、超时、429 或 5xx 等可恢复故障时切换一次；不会绕过安全和余额检查。</div>
          </template>
          <!-- 推理力度策略（二期 B）：max_reasoning_effort + reasoning_effort_mappings，
               仅 openai / composite（后端枚举校验同平台范围） -->
          <ReasoningEffortFields
            v-if="supportsReasoningEffort"
            :platform="editForm.platform"
            :max-effort="editForm.max_reasoning_effort"
            :mappings="editForm.reasoning_effort_rows"
            @update:max-effort="(v) => patchForm({ max_reasoning_effort: v })"
            @update:mappings="(v) => patchForm({ reasoning_effort_rows: v })"
          />
          <!-- Anthropic 专属：Claude Code 限制 + 降级分组（二期 A） -->
          <template v-if="editForm.platform === 'anthropic'">
            <div class="frow">
              <div class="frow-main">
                <div class="frow-t">Claude Code 客户端限制</div>
                <div class="frow-d">{{ editForm.claude_code_only ? '仅限 Claude Code' : '允许所有客户端' }}；启用后非 Claude Code 请求将被拒绝或降级。</div>
              </div>
              <a-switch v-model:checked="editForm.claude_code_only" size="small" />
            </div>
            <a-form-item v-if="showFallbackGroup" label="降级分组">
              <a-select v-model:value="editForm.fallback_group_id" :options="fallbackGroupOptions" placeholder="不降级（直接拒绝）" />
              <div class="hint">非 Claude Code 请求将使用此分组，留空则直接拒绝。</div>
            </a-form-item>
          </template>
          <!-- 无效请求兜底（anthropic/antigravity 且非订阅，二期 A） -->
          <a-form-item v-if="showInvalidFallback" label="无效请求兜底分组">
            <a-select v-model:value="editForm.fallback_group_id_on_invalid_request" :options="invalidFallbackOptions" placeholder="不兜底" />
            <div class="hint">仅当上游明确返回 prompt too long 时才会触发，留空表示不兜底。</div>
          </a-form-item>
          <!-- Antigravity 专属 -->
          <template v-if="editForm.platform === 'antigravity'">
            <a-form-item label="支持的模型系列">
              <a-checkbox-group v-model:value="editForm.supported_model_scopes" :options="scopeOptions" />
              <div class="hint">选择此分组支持的模型系列，未勾选的系列将不会被路由到此分组，至少选择一个。</div>
            </a-form-item>
            <div class="frow">
              <div class="frow-main">
                <div class="frow-t">MCP XML 协议注入</div>
                <div class="frow-d">请求包含 MCP 工具时在 system prompt 注入 XML 调用协议提示词；关闭可避免干扰部分客户端。</div>
              </div>
              <a-switch v-model:checked="editForm.mcp_xml_inject" size="small" />
            </div>
          </template>
          <!-- 媒体与按次计价（二期 A：image/video/voice/search 小字段 + 允许生图开关） -->
          <GroupMediaPricingFields v-if="showMediaPricing" :form="editForm" @patch="patchForm" />
        </a-form>
      </a-collapse-panel>

      <!-- ============ 模型映射与路由 ============ -->
      <a-collapse-panel key="mapping" header="模型映射与路由">
        <div class="panel-body">
          <GroupModelMappingFields
            :rows="editForm.model_mapping_rows"
            :candidates="mappingCandidates"
            :loading="candidatesLoading"
            :platform="editForm.platform"
            :group-id="editForm.id || null"
            :health="routeHealth"
            @update:rows="(rows) => (editForm.model_mapping_rows = rows)"
            @reset-health="resetRouteHealth"
          />
          <GroupModelRoutingFields v-if="editForm.platform === 'anthropic'" :group-id="editForm.id || null" v-model:enabled="routingEnabled" v-model:rules="routingRules" />
          <!-- /v1/models 白名单（models_list_config.enabled/models，二期 B） -->
          <ModelsListFields
            :enabled="editForm.models_list_enabled" :models="editForm.models_list_models"
            :candidates="apiCandidates" :loading="candidatesLoading"
            @update:enabled="(v) => patchForm({ models_list_enabled: v })"
            @update:models="(v) => patchForm({ models_list_models: v })"
          />
          <GroupResponseModelFields
            :mode="editForm.response_model_mode" :custom="editForm.response_model_custom"
            @update:mode="(v) => patchForm({ response_model_mode: v })"
            @update:custom="(v) => patchForm({ response_model_custom: v })"
          />
        </div>
      </a-collapse-panel>

      <!-- ============ 专属定价 ============ -->
      <a-collapse-panel key="pricing" header="专属定价">
        <div class="panel-body">
          <!-- v0.2.5：利润控制（仅五个支持平台显示） -->
          <GroupProfitControlFields :form="editForm" @patch="(v) => patchForm(v)" />
          <GroupPricingFields
            :form-state="pricingForm" :platform="editForm.platform" :group-id="editForm.id || null"
            :loading="pricingLoading" :shared="pricingShared" :load-error="pricingLoadError"
            @update:form-state="updatePricing" @retry="retryPricing"
          />
        </div>
      </a-collapse-panel>
    </a-collapse>
  </W12Dialog>
</template>

<style scoped>
/* ===== 一期表单样式（原样迁移） ===== */
.gform :deep(.ant-form-item) { margin-bottom: 12px; }
.grid2 { display: grid; grid-template-columns: 1fr 1fr; gap: 0 16px; }
.grid3 { display: grid; grid-template-columns: 1fr 1fr 1fr; gap: 0 16px; }
.hint { font-size: 12px; color: var(--text2); line-height: 1.6; margin-top: 4px; }
.hint.pad { margin: -6px 0 10px; }
.err-hint { font-size: 12px; color: #d13438; margin: 2px 0 0; }
.frow {
  display: flex; align-items: center; justify-content: space-between; gap: 16px;
  padding: 8px 10px; border-radius: 8px; min-height: 40px; margin-bottom: 8px;
  background: var(--card);
}
.frow:hover { filter: brightness(1.03); }
.frow-main { min-width: 0; }
.frow-t { font-size: 13px; }
.frow-d { font-size: 12px; color: var(--text2); margin-top: 1px; }
.seg {
  display: inline-grid; grid-auto-flow: column; gap: 4px; padding: 3px;
  background: var(--card); border-radius: 8px;
}
.seg.wrap { grid-auto-flow: row; grid-template-columns: repeat(4, 1fr); width: 100%; }
.seg-btn {
  border: none; background: transparent; border-radius: 6px; padding: 5px 12px;
  font-size: 12px; cursor: pointer; color: var(--text2); white-space: nowrap;
}
.seg-btn:hover { color: inherit; }
.seg-btn.on { background: var(--bg70); color: var(--text); font-weight: 500; }
.cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 8px; }
.card {
  text-align: left; border: 1px solid var(--hr); background: var(--card);
  border-radius: 8px; padding: 8px 10px; cursor: pointer; display: grid; gap: 3px;
}
.card:hover { border-color: var(--text2); }
/* 选中态改用主题强调色令牌：此前硬编码 #4f6bed，在翠绿/靛紫/曜黑等
 * 主题下与所选强调色不一致（切到非默认主题即可复现）。 */
.card.on { border-color: var(--accent); background: color-mix(in srgb, var(--accent) 12%, transparent); }
.card-t { font-size: 12.5px; font-weight: 500; }
.card-d { font-size: 11.5px; color: var(--text2); line-height: 1.5; }
.tin {
  height: 32px; width: 100%; padding: 0 10px; border-radius: var(--r-ctl);
  border: 1px solid var(--stroke-ctl); background: var(--surface-card); color: inherit; font-size: 13px;
}
.tin:hover { border-color: var(--accent); }
.tin:focus { outline: none; border-color: var(--accent); }
/* ===== 二期：collapse 分区与面板内边距 ===== */
.panel-body { display: grid; gap: 14px; padding: 2px 2px 8px; }
</style>

<style src="./groupSample.css"></style>
