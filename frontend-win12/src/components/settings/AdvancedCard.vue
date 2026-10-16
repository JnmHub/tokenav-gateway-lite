<script setup>
/* ============================================================
 * 杂项「高级」卡（字段对照 dto.SystemSettings + UpdateSettingsRequest）
 * - openai_advanced_scheduler_enabled / openai_upstream_cost_scheduling_enabled（switch）
 * - openai_upstream_cost_weight / openai_quota_headroom_weight / openai_prefer_soonest_reset_weight
 *   （后端校验 0~2 的有限数；权重类用 clampNum 不取整）
 * - default_subscriptions：DefaultSubscriptionSetting[] = { group_id, validity_days }（整体替换）
 * - default_platform_quotas：map[platform]{daily,weekly,monthly}（*float64，null=不限制、0=禁用；
 *   请求 non-nil = 整体覆盖，故按 4 平台全量提交，对照 frontend/src/api/admin/settings.ts 的
 *   normalize/sanitizePlatformQuotasMap 语义）
 * - allow_user_view_error_requests（switch）
 * - web_search_emulation_enabled：主接口不收该字段，走独立子接口
 *   GET/PUT /admin/settings/web-search-emulation（{enabled, providers}；
 *   api_key 留空时后端 mergeExistingAPIKeys 自动保留原值，providers 原样回传）
 * ============================================================ */
import { ref, reactive, computed, watch } from 'vue'
import { request } from '../../api'
import { message } from 'ant-design-vue'
import { PlusOutlined, DeleteOutlined, SearchOutlined } from '@ant-design/icons-vue'
import { clampNum } from './fields'
import { confirmAction } from '../../utils/confirmAction'

const props = defineProps({
  s: { type: Object, required: true },
  save: { type: Function, required: true },
})

const saving = ref(false)

/* ── OpenAI 调度权重（0~2，非法输入回退当前值） ── */
const clampWeight = (key) => {
  props.s[key] = clampNum(Number(props.s[key]), Number(props.s[key]) || 0, 0, 2)
}

/* ── 默认订阅：行编辑 {group_id, validity_days} ── */
const subs = computed(() => {
  if (!Array.isArray(props.s.default_subscriptions)) props.s.default_subscriptions = []
  return props.s.default_subscriptions
})
const addSub = () => subs.value.push({ group_id: undefined, validity_days: 30 })
const removeSub = (i) => subs.value.splice(i, 1)

/* ── 全局平台配额：4 平台 × 3 窗口，空输入 = null（不限制） ── */
const PLATFORMS = ['anthropic', 'openai', 'gemini', 'antigravity']
const WINDOWS = [
  { k: 'daily', label: '日限额' },
  { k: 'weekly', label: '周限额' },
  { k: 'monthly', label: '月限额' },
]
const quotas = reactive({}) // quotas[platform][window] = number | null
const loadQuotas = () => {
  const src = props.s.default_platform_quotas || {}
  for (const p of PLATFORMS) {
    quotas[p] = { daily: null, weekly: null, monthly: null }
    for (const w of WINDOWS) {
      const v = src[p]?.[w.k]
      quotas[p][w.k] = typeof v === 'number' && Number.isFinite(v) ? v : null
    }
  }
}
const buildQuotas = () => {
  // 提交结构对齐 service.DefaultPlatformQuotaSetting{daily,weekly,monthly *float64}
  const out = {}
  for (const p of PLATFORMS) {
    out[p] = { daily: quotas[p]?.daily ?? null, weekly: quotas[p]?.weekly ?? null, monthly: quotas[p]?.monthly ?? null }
  }
  return out
}

// setup 期同步初始化：模板首帧就会绑定 quotas[p][w.k]，晚一帧会整卡渲染崩溃
loadQuotas()
// 快照被 PUT 响应整体替换时重新规范化输入值
watch(() => props.s.default_platform_quotas, () => loadQuotas())

const doSave = async () => {
  // group_id 必填（int64），validity_days 缺省行剔除
  const subRows = subs.value
    .map((r) => ({ group_id: Number(r.group_id), validity_days: Number(r.validity_days) || 0 }))
    .filter((r) => Number.isInteger(r.group_id) && r.group_id > 0 && r.validity_days > 0)
  if (subs.value.length !== subRows.length) {
    message.warning('存在未填分组 ID 或有效天数的订阅行，已忽略')
  }
  saving.value = true
  try {
    await props.save('advanced', {
      openai_advanced_scheduler_enabled: !!props.s.openai_advanced_scheduler_enabled,
      openai_upstream_cost_scheduling_enabled: !!props.s.openai_upstream_cost_scheduling_enabled,
      openai_upstream_cost_weight: clampNum(Number(props.s.openai_upstream_cost_weight), 0, 0, 2),
      openai_quota_headroom_weight: clampNum(Number(props.s.openai_quota_headroom_weight), 0, 0, 2),
      openai_prefer_soonest_reset_weight: clampNum(Number(props.s.openai_prefer_soonest_reset_weight), 0, 0, 2),
      default_subscriptions: subRows,
      default_platform_quotas: buildQuotas(),
      allow_user_view_error_requests: !!props.s.allow_user_view_error_requests,
    })
  } finally {
    saving.value = false
  }
}

/* ── Web Search 模拟开关（独立子接口） ── */
const ws = reactive({ loading: false, saving: false, enabled: false, loaded: false, hasKey: false })
const loadWebSearch = async () => {
  ws.loading = true
  try {
    const d = await request('/admin/settings/web-search-emulation', { method: 'GET' })
    ws.enabled = !!d?.enabled
    // 记录是否有已配置 key（enabled 保存时后端要求至少一个 provider 有 key）
    ws.hasKey = (d?.providers || []).some((p) => p.api_key_configured || p.api_key)
    ws.loaded = true
  } catch (e) {
    message.error(`Web 搜索配置读取失败：${e.message}`)
  } finally {
    ws.loading = false
  }
}
const saveWebSearch = async () => {
  ws.saving = true
  try {
    // providers 原样回传（GET 返回含原 key，api_key 留空语义由后端 merge 保护）
    const cfg = await request('/admin/settings/web-search-emulation', { method: 'GET' })
    await request('/admin/settings/web-search-emulation', {
      method: 'PUT',
      body: { enabled: ws.enabled, providers: cfg?.providers || [] },
    })
    message.success('Web 搜索模拟设置已保存')
  } catch (e) {
    message.error(`保存失败：${e.message}`)
  } finally {
    ws.saving = false
  }
}

/* ── Web 搜索：连通性测试 + 重置用量（独立子接口，请求/响应字段对照 setting_handler.go） ── */
const wsTest = reactive({ running: false, query: '', result: null })
const testWebSearch = async () => {
  wsTest.running = true
  wsTest.result = null
  try {
    // POST /admin/settings/web-search-emulation/test，body 仅 { query }（后端默认「搜索今年世界大事件」）
    const r = await request('/admin/settings/web-search-emulation/test', {
      method: 'POST',
      body: { query: String(wsTest.query || '').trim() },
    })
    wsTest.result = { provider: r?.provider || '-', query: r?.query || '', results: Array.isArray(r?.results) ? r.results : [] }
    message.success(`搜索成功：${r?.provider || '-'} 返回 ${wsTest.result.results.length} 条结果`)
  } catch (e) {
    message.error(`连通性测试失败：${e.message}`)
  } finally {
    wsTest.running = false
  }
}

const wsReset = reactive({ running: false, provider: '' })
const resetWebSearchUsage = async () => {
  if (!wsReset.provider) { message.warning('请先选择要重置用量的 Provider'); return }
  if (!(await confirmAction({
    title: '重置搜索配额用量',
    content: `将清空 ${wsReset.provider} 的当月配额用量统计（Redis 计数归零），不可撤销。确认重置？`,
    okText: '重置',
    danger: true,
  }))) return
  wsReset.running = true
  try {
    await request('/admin/settings/web-search-emulation/reset-usage', {
      method: 'POST',
      body: { provider_type: wsReset.provider },
    })
    message.success('配额用量已重置')
    await loadWebSearch() // 重置后刷新配置，配额用量随响应一并回读
  } catch (e) {
    message.error(`重置失败：${e.message}`)
  } finally {
    wsReset.running = false
  }
}

loadWebSearch() // 开关读取独立子接口，setup 期发起，先于首帧完成
</script>

<template>
  <div class="card">
    <div class="card-title">高级</div>
    <div class="form">
      <div class="frow">
        <label>允许用户看失败请求</label>
        <div class="fmain">
          <a-switch v-model:checked="s.allow_user_view_error_requests" />
          <span class="static">终端用户可在用量页查看自己的失败请求明细</span>
        </div>
      </div>

      <div class="frow">
        <label>Web 搜索模拟</label>
        <div class="fmain">
          <a-switch v-model:checked="ws.enabled" :loading="ws.loading" />
          <a-button size="small" :loading="ws.saving" :disabled="!ws.loaded" @click="saveWebSearch">保存</a-button>
          <span class="static">{{ ws.loaded ? (ws.hasKey ? '已配置搜索 Provider' : '尚未配置 Provider Key') : '读取中…' }}</span>
        </div>
      </div>
      <div class="frow">
        <label>连通性测试</label>
        <div class="fmain">
          <a-input v-model:value="wsTest.query" class="fld" placeholder="留空使用默认测试查询" @press-enter="testWebSearch" />
          <a-button size="small" :loading="wsTest.running" :disabled="!ws.loaded" @click="testWebSearch"><SearchOutlined /> 测试搜索连接</a-button>
        </div>
      </div>
      <div v-if="wsTest.result" class="frow frow-col">
        <label>测试结果</label>
        <div class="fmain fm-col">
          <span class="static">Provider：{{ wsTest.result.provider }} · 查询：{{ wsTest.result.query || '-' }} · 共 {{ wsTest.result.results.length }} 条</span>
          <div v-for="(r, i) in wsTest.result.results" :key="i" class="wsres">
            <a v-if="r.url" :href="r.url" target="_blank" rel="noopener">{{ r.title || r.url }}</a>
            <span v-else>{{ r.title || '-' }}</span>
          </div>
        </div>
      </div>
      <div class="frow">
        <label>重置配额用量</label>
        <div class="fmain">
          <a-select v-model:value="wsReset.provider" class="fld-n" placeholder="选择 Provider" :options="[{ value: 'brave', label: 'Brave' }, { value: 'tavily', label: 'Tavily' }]" allow-clear style="min-width: 140px" />
          <a-button size="small" danger :loading="wsReset.running" :disabled="!wsReset.provider" @click="resetWebSearchUsage">重置用量</a-button>
          <span class="static">破坏性操作：Redis 配额计数归零，不可撤销</span>
        </div>
      </div>
    </div>

    <div class="divider" />

    <div class="card-title">OpenAI 账号调度</div>
    <div class="form">
      <div class="frow">
        <label>高级调度器</label>
        <div class="fmain"><a-switch v-model:checked="s.openai_advanced_scheduler_enabled" /></div>
      </div>
      <div class="frow">
        <label>上游成本调度</label>
        <div class="fmain"><a-switch v-model:checked="s.openai_upstream_cost_scheduling_enabled" /></div>
      </div>
      <div class="frow">
        <label>成本权重</label>
        <div class="fmain">
          <a-input-number v-model:value="s.openai_upstream_cost_weight" :min="0" :max="2" :step="0.05" class="fld-n"
            @blur="clampWeight('openai_upstream_cost_weight')" />
        </div>
      </div>
      <div class="frow">
        <label>余量权重</label>
        <div class="fmain">
          <a-input-number v-model:value="s.openai_quota_headroom_weight" :min="0" :max="2" :step="0.05" class="fld-n"
            @blur="clampWeight('openai_quota_headroom_weight')" />
        </div>
      </div>
      <div class="frow">
        <label>优先快重置权重</label>
        <div class="fmain">
          <a-input-number v-model:value="s.openai_prefer_soonest_reset_weight" :min="0" :max="2" :step="0.05" class="fld-n"
            @blur="clampWeight('openai_prefer_soonest_reset_weight')" />
          <span class="static">三项权重均为 0~2</span>
        </div>
      </div>
    </div>

    <div class="divider" />

    <div class="card-title">新用户默认订阅</div>
    <div v-for="(r, i) in subs" :key="i" class="frow">
      <label>{{ i === 0 ? '订阅行' : ' ' }}</label>
      <div class="fmain">
        <a-input-number v-model:value="r.group_id" class="fld-n" :min="1" placeholder="分组 ID" />
        <span class="static">分组 ID</span>
        <a-input-number v-model:value="r.validity_days" class="fld-n" :min="1" placeholder="天数" />
        <span class="static">有效天数</span>
        <a-button size="small" type="text" danger @click="removeSub(i)"><DeleteOutlined /></a-button>
      </div>
    </div>
    <div class="frow">
      <label>默认订阅</label>
      <div class="fmain">
        <a-button size="small" @click="addSub"><PlusOutlined /> 添加订阅</a-button>
        <span class="static">新注册用户按分组自动获得对应期限订阅</span>
      </div>
    </div>

    <div class="divider" />

    <div class="card-title">系统全局平台配额（空 = 不限制，0 = 禁用，USD）</div>
    <div class="form">
      <div v-for="p in PLATFORMS" :key="p" class="frow">
        <label>{{ p }}</label>
        <div class="fmain qrow">
          <template v-for="w in WINDOWS" :key="w.k">
            <a-input-number v-model:value="quotas[p][w.k]" :min="0" :step="0.5" class="fld-n" placeholder="不限" />
            <span class="static">{{ w.label }}</span>
          </template>
        </div>
      </div>
    </div>

    <div class="savebar">
      <a-button type="primary" class="save" :loading="saving" @click="doSave">保存高级设置</a-button>
    </div>
  </div>
</template>

<style scoped>
@import './settings.css';
.qrow { gap: 4px; flex-wrap: wrap; }
.qrow .static { margin-left: 0; margin-right: 8px; }
.divider { border-top: 1px solid var(--hr); margin: 12px -16px; opacity: 0.6; }
.wsres { font-size: 12px; line-height: 1.6; word-break: break-all; }
.wsres a { color: var(--accent, #2563eb); text-decoration: none; }
.wsres a:hover { text-decoration: underline; }
</style>
