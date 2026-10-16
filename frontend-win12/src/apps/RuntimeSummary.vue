<script setup>
/* 运行摘要 v2（对照旧 RuntimeSummaryView.vue + settings/ 三个热更新卡）：
 * - 只读快照 7 分区（DB/缓存/计费池/用量上报/超时/WebSocket/高级调度）+ 来源标签
 *   数据源：GET /admin/ops/runtime/summary（字段名与旧版逐一核对）
 * - 计费缓存热更新：GET/PUT /admin/ops/runtime/billing-cache（expected_revision CAS 防并发）
 * - 假首字配置：GET/PUT /admin/ops/runtime/fake-first-token（0.01-30s，min<=max 后端校验）
 * - OpenAI 调度器策略：GET/PUT /admin/ops/runtime/openai-scheduler + POST .../reset
 *   （PUT 只发白名单字段 + expected_revision；previous_response_mode/sales_billing_isolated 为只读硬约束）
 * 待迭代：告警高亮细化、i18n */
import { ref, computed, onMounted } from 'vue'
import { message } from 'ant-design-vue'
import { request } from '../api'
import { confirmAction } from '../utils/confirmAction'

const loading = ref(true)
const err = ref('')
const s = ref(null)

const fmt = (n) => (n === null || n === undefined ? '-' : Number(n).toLocaleString('zh-CN'))
const yn = (v) => (v ? '开' : '关')
const uptime = computed(() => {
  const sec = s.value?.service?.uptime_seconds
  if (!sec) return '-'
  const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60)
  return `${d}天 ${h}小时 ${m}分`
})
// 分区来源标签（与旧 sourceLabel 语义一致）
const srcLabel = (src) => ({ runtime_memory: '运行内存', startup_config: '启动配置', database_setting_and_startup_config: '数据库 + 启动', runtime_memory_and_startup_config: '内存 + 启动' }[src] || '系统内置')

const load = async () => {
  loading.value = true
  try {
    s.value = await request('/admin/ops/runtime/summary', { method: 'GET' })
    err.value = ''
  } catch (e) {
    err.value = e.message
  } finally {
    loading.value = false
  }
}
onMounted(load)

/* ===== 计费缓存热更新（revision CAS） ===== */
const bc = ref(null)
const bcForm = ref({})
const bcSaving = ref(false)
const PRESET_NAMES = { standard: '标准', conservative: '保守', high_throughput: '高吞吐', performance: '性能' }

const loadBc = async () => {
  try {
    bc.value = await request('/admin/ops/runtime/billing-cache', { method: 'GET' })
    bcForm.value = {
      preset: bc.value.preset,
      critical_worker_count: bc.value.critical_worker_count,
      critical_buffer_size: bc.value.critical_buffer_size,
      best_effort_worker_count: bc.value.best_effort_worker_count,
      best_effort_buffer_size: bc.value.best_effort_buffer_size,
      timeout_milliseconds: bc.value.timeout_milliseconds,
    }
  } catch (e) { message.warning(`计费缓存配置读取失败：${e.message}`) }
}

const saveBc = async () => {
  bcSaving.value = true
  try {
    // CAS：带上当前 revision，后端冲突即拒绝（防多管理员并发改）
    await request('/admin/ops/runtime/billing-cache', {
      method: 'PUT',
      body: { expected_revision: Number(bc.value.revision), ...bcForm.value },
    })
    message.success('计费缓存配置已热生效')
    await loadBc()
  } catch (e) {
    message.error(`保存失败：${e.message}`)
  } finally {
    bcSaving.value = false
  }
}

/* ===== 假首字配置 ===== */
const ft = ref(null)
const ftForm = ref({})
const ftSaving = ref(false)

const loadFt = async () => {
  try {
    ft.value = await request('/admin/ops/runtime/fake-first-token', { method: 'GET' })
    ftForm.value = { enabled: !!ft.value.enabled, min_seconds: ft.value.min_seconds, max_seconds: ft.value.max_seconds }
  } catch (e) { message.warning(`假首字配置读取失败：${e.message}`) }
}

const saveFt = async () => {
  if (Number(ftForm.value.min_seconds) > Number(ftForm.value.max_seconds)) {
    message.warning('最小延迟不能大于最大延迟')
    return
  }
  ftSaving.value = true
  try {
    ft.value = await request('/admin/ops/runtime/fake-first-token', { method: 'PUT', body: ftForm.value })
    message.success('假首字配置已热生效')
  } catch (e) {
    message.error(`保存失败：${e.message}`)
  } finally {
    ftSaving.value = false
  }
}

/* ===== OpenAI 调度器策略 ===== */
const WEIGHTS = [
  ['priority_weight', '优先级权重'],
  ['load_weight', '负载权重'],
  ['queue_weight', '队列权重'],
  ['error_rate_weight', '错误率权重'],
  ['ttft_weight', '首字延迟权重'],
]
const sp = ref(null)
const spForm = ref({})
const spSaving = ref(false)
const spResetting = ref(false)
const SP_PRESETS = { safe_default: '安全默认', balanced: '均衡', performance: '性能', custom: '自定义' }

const loadSp = async () => {
  try {
    sp.value = await request('/admin/ops/runtime/openai-scheduler', { method: 'GET' })
    spForm.value = {
      preset: sp.value.preset,
      advanced_enabled: !!sp.value.advanced_enabled,
      sticky_soft_scoring_enabled: !!sp.value.sticky_soft_scoring_enabled,
      prefer_subscription_pool: !!sp.value.prefer_subscription_pool,
      upstream_cost_enabled: !!sp.value.upstream_cost_enabled,
      top_k: sp.value.top_k,
      priority_weight: sp.value.priority_weight,
      load_weight: sp.value.load_weight,
      queue_weight: sp.value.queue_weight,
      error_rate_weight: sp.value.error_rate_weight,
      ttft_weight: sp.value.ttft_weight,
    }
  } catch (e) { message.warning(`调度器策略读取失败：${e.message}`) }
}

const saveSp = async () => {
  spSaving.value = true
  try {
    // 只发后端白名单字段 + expected_revision（previous_response_mode 等硬约束不接收输入）
    sp.value = await request('/admin/ops/runtime/openai-scheduler', {
      method: 'PUT',
      body: { expected_revision: Number(sp.value.revision), ...spForm.value },
    })
    message.success('调度器策略已热生效')
    await loadSp()
  } catch (e) {
    message.error(`保存失败：${e.message}`)
  } finally {
    spSaving.value = false
  }
}

const resetSp = async () => {
  spResetting.value = true
  try {
    await request('/admin/ops/runtime/openai-scheduler/reset', { method: 'POST' })
    message.success('已恢复默认策略')
    await loadSp()
  } catch (e) {
    message.error(`重置失败：${e.message}`)
  } finally {
    spResetting.value = false
  }
}

/* ===== Server-Timing 临时采样（排障用，到期自动关闭） ===== */
const st = ref(null)
const stPreset = ref('15m')
const stBusy = ref(false)

const loadSt = async () => {
  try {
    st.value = await request('/admin/ops/runtime/server-timing', { method: 'GET' })
  } catch (e) { message.warning(`Server-Timing 状态读取失败：${e.message}`) }
}

const enableSt = async () => {
  if (!(await confirmAction({ title: '临时开启 Server-Timing 采样', content: `开启后 ${stPreset.value} 内所有请求都会带 Server-Timing 响应头（含内部耗时），仅用于排障，到期自动关闭。确认开启？`, okText: '开启' }))) return
  stBusy.value = true
  try {
    st.value = await request('/admin/ops/runtime/server-timing/enable', { method: 'POST', body: { preset: stPreset.value } })
    message.success('Server-Timing 临时采样已开启')
  } catch (e) {
    message.error(`开启失败：${e.message}`)
  } finally {
    stBusy.value = false
  }
}

const disableSt = async () => {
  stBusy.value = true
  try {
    st.value = await request('/admin/ops/runtime/server-timing/disable', { method: 'POST' })
    message.success('Server-Timing 已关闭')
  } catch (e) {
    message.error(`关闭失败：${e.message}`)
  } finally {
    stBusy.value = false
  }
}

onMounted(() => { loadBc(); loadFt(); loadSp(); loadSt() })

</script>

<template>
  <div class="rs">
    <div class="toolbar">
      <span class="total">运行摘要 · 网关运行时快照与热更新</span>
      <a-button size="small" @click="load">刷新快照</a-button>
      <a-button v-if="err" size="small" danger @click="load">加载失败，点击重试</a-button>
    </div>

    <!-- 只读快照分区（来源标签 = 值的生效来源） -->
    <a-spin :spinning="loading">
      <div v-if="s" class="grid">
        <div class="card">
          <div class="card-title">服务</div>
          <div class="row"><span>名称 / 版本</span><span>{{ s.service?.name }} {{ s.service?.version }}</span></div>
          <div class="row"><span>运行时</span><span>{{ s.service?.runtime }}</span></div>
          <div class="row"><span>已运行</span><span>{{ uptime }}</span></div>
        </div>
        <div class="card">
          <div class="card-title">PostgreSQL 连接池 <a-tag class="src">{{ srcLabel(s.database?.source) }}</a-tag></div>
          <div class="row"><span>最大连接 / 最大空闲</span><span>{{ fmt(s.database?.max_open) }} / {{ fmt(s.database?.max_idle) }}</span></div>
          <div class="row"><span>连接寿命 / 空闲寿命</span><span>{{ fmt(s.database?.max_lifetime_minutes) }}分 / {{ fmt(s.database?.max_idle_time_minutes) }}分</span></div>
          <div class="row"><span>配额批量落库</span><span>{{ yn(s.database?.quota_flusher_enabled) }}（单批 {{ fmt(s.database?.quota_flush_batch_size) }} / {{ fmt(s.database?.quota_flush_interval_ms) }}ms）</span></div>
        </div>
        <div class="card">
          <div class="card-title">Redis 连接池 <a-tag class="src">{{ srcLabel(s.cache?.source) }}</a-tag></div>
          <div class="row"><span>连接池 / 最小空闲</span><span>{{ fmt(s.cache?.pool_size) }} / {{ fmt(s.cache?.min_idle) }}</span></div>
          <div class="row"><span>拨号 / 读 / 写超时</span><span>{{ fmt(s.cache?.dial_timeout_seconds) }} / {{ fmt(s.cache?.read_timeout_seconds) }} / {{ fmt(s.cache?.write_timeout_seconds) }}s</span></div>
          <div class="row"><span>TLS</span><span>{{ yn(s.cache?.tls_enabled) }}</span></div>
        </div>
        <div class="card">
          <div class="card-title">计费缓存队列 <a-tag class="src">{{ srcLabel(s.billing?.source) }}</a-tag></div>
          <div class="row"><span>当前档位 / 修订</span><span>{{ s.billing?.preset || '-' }} / rev {{ fmt(s.billing?.revision) }}</span></div>
          <div class="row"><span>关键线程（有效）</span><span>{{ fmt(s.billing?.critical_workers) }}（{{ fmt(s.billing?.critical_active_workers) }}）</span></div>
          <div class="row"><span>关键排队 / 容量</span><span>{{ fmt(s.billing?.critical_pending) }} / {{ fmt(s.billing?.critical_capacity) }}</span></div>
          <div class="row"><span>同步兜底 / 失败</span><span>{{ fmt(s.billing?.critical_fallback_total) }} / {{ fmt(s.billing?.critical_fallback_fail_total) }}</span></div>
          <div class="row"><span>非关键线程（有效）</span><span>{{ fmt(s.billing?.best_effort_workers) }}（{{ fmt(s.billing?.best_effort_active_workers) }}）</span></div>
          <div class="row"><span>非关键排队 / 延后区</span><span>{{ fmt(s.billing?.best_effort_pending) }} / {{ fmt(s.billing?.best_effort_deferred) }}</span></div>
          <div class="row"><span>非关键丢弃 / 同键保序等待</span><span>{{ fmt(s.billing?.best_effort_dropped_total) }} / {{ fmt(s.billing?.ordering_wait_total) }}</span></div>
        </div>
        <div class="card">
          <div class="card-title">用量队列与上报 <a-tag class="src">{{ srcLabel(s.usage?.source) }}</a-tag></div>
          <div class="row"><span>本地线程（运行中）</span><span>{{ fmt(s.usage?.local_workers) }}（{{ fmt(s.usage?.local_running) }}）</span></div>
          <div class="row"><span>本地排队 / 容量</span><span>{{ fmt(s.usage?.local_pending) }} / {{ fmt(s.usage?.local_capacity) }}</span></div>
          <div class="row"><span>失败 / 丢弃</span><span>{{ fmt(s.usage?.local_failed) }} / {{ fmt(s.usage?.local_dropped) }}</span></div>
          <div class="row"><span>上报线程 / 单批</span><span>{{ fmt(s.usage?.report_workers) }} / {{ fmt(s.usage?.report_batch_size) }}</span></div>
        </div>
        <div class="card">
          <div class="card-title">请求与流式超时 <a-tag class="src">{{ srcLabel(s.timeouts?.source) }}</a-tag></div>
          <div class="row"><span>主站请求</span><span>{{ fmt(s.timeouts?.control_plane_ms) }}ms</span></div>
          <div class="row"><span>通用响应头</span><span>{{ fmt(s.timeouts?.response_header_seconds) }}s</span></div>
          <div class="row"><span>OpenAI 响应头 / 首字</span><span>{{ fmt(s.timeouts?.openai_response_header_seconds) }} / {{ fmt(s.timeouts?.openai_first_output_seconds) }}s</span></div>
          <div class="row"><span>高推理首字 / 流间隔</span><span>{{ fmt(s.timeouts?.openai_high_reasoning_output_seconds) }} / {{ fmt(s.timeouts?.stream_data_interval_seconds) }}s</span></div>
        </div>
        <div class="card">
          <div class="card-title">WebSocket <a-tag class="src">{{ srcLabel(s.websocket?.source) }}</a-tag></div>
          <div class="row"><span>最终启用 / 强制 HTTP</span><span>{{ yn(s.websocket?.effective_enabled) }} / {{ yn(s.websocket?.force_http) }}</span></div>
          <div class="row"><span>新版路由 / 入口模式</span><span>{{ yn(s.websocket?.mode_router_v2) }} / {{ s.websocket?.ingress_mode || '-' }}</span></div>
          <div class="row"><span>首包超时 / 单账号连接</span><span>{{ fmt(s.websocket?.client_first_message_seconds) }}s / {{ fmt(s.websocket?.max_connections_per_account) }}</span></div>
        </div>
        <div class="card">
          <div class="card-title">高级调度 <a-tag class="src">{{ srcLabel(s.scheduling?.source) }}</a-tag></div>
          <div class="row"><span>高级调度开关</span><span>{{ s.scheduling?.advanced_setting_available ? yn(s.scheduling?.advanced_enabled) : '读取失败' }}</span></div>
          <div class="row"><span>粘性逃逸（TTFT / 错误率阈值）</span><span>{{ yn(s.scheduling?.sticky_escape_enabled) }}（{{ fmt(s.scheduling?.sticky_escape_ttft_ms) }}ms / {{ ((s.scheduling?.sticky_escape_error_rate || 0) * 100).toFixed(1) }}%）</span></div>
          <div class="row"><span>兜底队列（等待 / 容量）</span><span>{{ yn(s.scheduling?.fallback_queue_enabled) }}（{{ fmt(s.scheduling?.fallback_wait_ms) }}ms / {{ fmt(s.scheduling?.fallback_max_waiting) }}）</span></div>
          <div class="row"><span>批量负载读取</span><span>{{ yn(s.scheduling?.load_batch_enabled) }}</span></div>
        </div>
      </div>
    </a-spin>

    <!-- 热更新区：保存即生效，无需重启 -->
    <div class="section-title">运行时热更新</div>
    <div class="grid">
      <div class="card">
        <div class="card-title">计费缓存写入池 <a-tag v-if="bc" class="src">rev {{ bc.revision }}</a-tag></div>
        <template v-if="bc">
          <div class="row"><span>预设档位</span>
            <a-radio-group v-model:value="bcForm.preset" size="small">
              <a-radio-button v-for="p in bc.available_presets || []" :key="p" :value="p">{{ PRESET_NAMES[p] || p }}</a-radio-button>
            </a-radio-group>
          </div>
          <div class="row"><span>关键队列 线程 / 缓冲</span><span class="ctl"><a-input-number v-model:value="bcForm.critical_worker_count" :min="1" size="small" style="width:80px" /> / <a-input-number v-model:value="bcForm.critical_buffer_size" :min="1" size="small" style="width:90px" /></span></div>
          <div class="row"><span>非关键队列 线程 / 缓冲</span><span class="ctl"><a-input-number v-model:value="bcForm.best_effort_worker_count" :min="1" size="small" style="width:80px" /> / <a-input-number v-model:value="bcForm.best_effort_buffer_size" :min="1" size="small" style="width:90px" /></span></div>
          <div class="row"><span>写入超时</span><a-input-number v-model:value="bcForm.timeout_milliseconds" :min="100" :step="100" size="small" style="width:110px" /><span class="unit">ms</span></div>
          <div class="row"><span>当前排队</span><span>关键 {{ fmt(bc.critical_queue_length) }}/{{ fmt(bc.critical_queue_capacity) }} · 非关键 {{ fmt(bc.best_effort_queue_length) }}/{{ fmt(bc.best_effort_queue_capacity) }}</span></div>
          <div class="savebar"><a-button type="primary" size="small" :loading="bcSaving" @click="saveBc">保存并热生效</a-button></div>
        </template>
      </div>
      <div class="card">
        <div class="card-title">伪造流式首字（反检测）</div>
        <template v-if="ft">
          <div class="row"><span>总开关</span><a-switch v-model:checked="ftForm.enabled" size="small" /></div>
          <div class="row"><span>首字延迟范围（秒）</span>
            <span class="ctl">
              <a-input-number v-model:value="ftForm.min_seconds" :min="0.01" :max="30" :step="0.1" size="small" style="width:90px" />
              <span class="unit">~</span>
              <a-input-number v-model:value="ftForm.max_seconds" :min="0.01" :max="30" :step="0.1" size="small" style="width:90px" />
            </span>
          </div>
          <div class="hint">等待上游首字节期间按随机延迟先发 SSE 注释行，让客户端提前收到字节；对 SDK 无害。</div>
          <div class="savebar"><a-button type="primary" size="small" :loading="ftSaving" @click="saveFt">保存并热生效</a-button></div>
        </template>
      </div>
      <div class="card">
        <div class="card-title">OpenAI 调度器策略 <a-tag v-if="sp" class="src">rev {{ sp.revision }}</a-tag></div>
        <template v-if="sp">
          <div class="row"><span>预设档位</span>
            <a-radio-group v-model:value="spForm.preset" size="small">
              <a-radio-button v-for="(label, key) in SP_PRESETS" :key="key" :value="key">{{ label }}</a-radio-button>
            </a-radio-group>
          </div>
          <div class="row"><span>高级调度</span><a-switch v-model:checked="spForm.advanced_enabled" size="small" /></div>
          <div class="row"><span>粘性软评分</span><a-switch v-model:checked="spForm.sticky_soft_scoring_enabled" size="small" /></div>
          <div class="row"><span>优先订阅池</span><a-switch v-model:checked="spForm.prefer_subscription_pool" size="small" /></div>
          <div class="row"><span>上游成本感知</span><a-switch v-model:checked="spForm.upstream_cost_enabled" size="small" /></div>
          <div class="row"><span>候选数 top_k</span>
            <span class="ctl">
              <a-button size="small" @click="spForm.top_k = Math.max(1, spForm.top_k - 1)">-</a-button>
              <b class="topk">{{ spForm.top_k }}</b>
              <a-button size="small" @click="spForm.top_k = Math.min(1000, spForm.top_k + 1)">+</a-button>
              <a-button v-for="v in [3, 7, 12, 20]" :key="v" size="small" :type="spForm.top_k === v ? 'primary' : 'default'" @click="spForm.top_k = v">{{ v }}</a-button>
            </span>
          </div>
          <div v-for="[key, label] in WEIGHTS" :key="key" class="row">
            <span>{{ label }}</span>
            <span class="ctl"><a-slider v-model:value="spForm[key]" :min="0" :max="3" :step="0.1" style="width: 140px" /><b class="unit">{{ spForm[key] }}</b></span>
          </div>
          <div class="row"><span>previous_response_id / 销售计费隔离</span><span>{{ sp.previous_response_mode }}（硬约束，不可改）</span></div>
          <div class="savebar">
            <a-button size="small" :loading="spResetting" @click="resetSp">恢复默认</a-button>
            <a-button type="primary" size="small" :loading="spSaving" @click="saveSp">保存并热生效</a-button>
          </div>
        </template>
      </div>
      <div class="card">
        <div class="card-title">Server-Timing 临时采样
          <a-tag v-if="st" class="src" :color="st.temporary_enabled ? 'green' : undefined">{{ st.temporary_enabled ? '采样中' : '已关闭' }}</a-tag>
        </div>
        <template v-if="st">
          <div class="row"><span>临时采样</span><span>{{ st.temporary_enabled ? '已开启' : '已关闭' }}</span></div>
          <div class="row"><span>采样比例</span><span>{{ st.sample_percent }}%（{{ fmt(st.sample_basis_points) }} / 10000 bp）</span></div>
          <div class="row"><span>已采集请求</span><span>{{ fmt(st.collected_total) }}</span></div>
          <div class="row"><span>到期时间</span><span>{{ st.temporary_until || '-' }}</span></div>
          <div class="row"><span>时长预设</span>
            <a-radio-group v-model:value="stPreset" size="small">
              <a-radio-button v-for="p in ['5m', '15m', '30m', '60m']" :key="p" :value="p">{{ p }}</a-radio-button>
            </a-radio-group>
          </div>
          <div class="hint">开启后响应会带 Server-Timing 头（含内部耗时），仅用于排障，到期自动关闭；状态接口不暴露请求路径与账号信息。</div>
          <div class="savebar">
            <a-button size="small" :loading="stBusy" :disabled="!st.temporary_enabled" @click="disableSt">立即关闭</a-button>
            <a-button type="primary" size="small" :loading="stBusy" @click="enableSt">临时开启</a-button>
          </div>
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped>
.rs { height: 100%; overflow-y: auto; }
.toolbar { display: flex; align-items: center; gap: 12px; margin-bottom: 10px; }
.total { font-size: 12px; color: var(--text2); }
.section-title { font-weight: 600; font-size: 14px; margin: 16px 0 10px; }
.grid { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
.card { padding: 14px 16px; border-radius: 10px; background: var(--card); border: 1px solid var(--hr); display: flex; flex-direction: column; gap: 10px; }
.card-title { font-weight: 600; font-size: 14px; }
.row { display: flex; align-items: center; justify-content: space-between; gap: 8px; font-size: 13px; min-height: 28px; }
.row > span:first-child { color: var(--text2); flex: none; }
.src { font-size: 11px; font-weight: 400; color: var(--text2); background: rgba(127, 127, 127, 0.12); border: none; }
.ctl { display: inline-flex; align-items: center; gap: 6px; }
.unit { color: var(--text2); font-size: 12px; }
.topk { min-width: 28px; text-align: center; }
.hint { font-size: 12px; color: var(--text2); line-height: 1.5; }
.savebar { display: flex; justify-content: flex-end; gap: 8px; padding-top: 4px; border-top: 1px solid var(--hr); }
@media (max-width: 900px) { .grid { grid-template-columns: 1fr; } }
</style>
