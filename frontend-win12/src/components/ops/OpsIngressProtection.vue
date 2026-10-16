<script setup>
/* 入口防护卡（二期）：后台接口限流 + 入口拒绝快照
 * 契约来源：旧 OpsIngressProtectionCard.vue + frontend/src/api/admin/ops.ts
 * - GET/PUT /admin/ops/runtime/panel-rate-limit → { settings: { enabled, preset,
 *   user_rpm, heavy_rpm, window_seconds }, runtime_stats: { checked_total,
 *   rejected_total, disabled_bypass_total, active_buckets } }
 * - POST /admin/ops/runtime/panel-rate-limit/reset（恢复默认）
 * - GET /admin/ops/ingress-rejections?minutes=60 → 快照（3 卡 + 每分钟趋势 + Top 原因）
 * 预设 default 240/60 · relaxed 600/180 · strict 120/30 · custom 自定义；窗口 10s/30s/60s/5min */
import { ref, reactive, computed, onMounted } from 'vue'
import { message } from 'ant-design-vue'
import { ReloadOutlined } from '@ant-design/icons-vue'
import { request } from '../../api'

const loadingConfig = ref(false)
const saving = ref(false)
const config = ref(null)
const runtimeStats = ref(null)
const snapshot = ref(null)
const rejectionsLoading = ref(false)
const customDraft = reactive({ user_rpm: 240, heavy_rpm: 60, window_seconds: 60 })

const PRESETS = [
  { value: 'default', label: '默认', hint: '240 / 60 RPM' },
  { value: 'relaxed', label: '宽松', hint: '600 / 180 RPM' },
  { value: 'strict', label: '严格', hint: '120 / 30 RPM' },
  { value: 'custom', label: '自定义', hint: '按实际后台规模设置' },
]
const WINDOW_OPTS = [
  { value: 10, label: '10s' }, { value: 30, label: '30s' },
  { value: 60, label: '60s' }, { value: 300, label: '5min' },
]
const REASON_LABEL = {
  header_too_large: '请求头过大', body_too_large: '正文过大', invalid_path: '非法路径',
  malformed_request: '畸形请求', auth_rate_limited: '鉴权限流', admin_rate_limited: '后台限流',
  codex_client_denied: 'Codex 客户端准入拒绝',
}

const applyRuntime = (result) => {
  config.value = result.settings
  runtimeStats.value = result.runtime_stats
  Object.assign(customDraft, {
    user_rpm: result.settings.user_rpm,
    heavy_rpm: result.settings.heavy_rpm,
    window_seconds: result.settings.window_seconds,
  })
}

const loadConfig = async () => {
  loadingConfig.value = true
  try { applyRuntime(await request('/admin/ops/runtime/panel-rate-limit', { method: 'GET' })) }
  catch (e) { message.error(`加载后台限流配置失败：${e.message}`) }
  finally { loadingConfig.value = false }
}
const loadRejections = async () => {
  rejectionsLoading.value = true
  try { snapshot.value = await request('/admin/ops/ingress-rejections', { method: 'GET', query: { minutes: 60 } }) }
  catch (e) { message.error(`加载入口拒绝统计失败：${e.message}`) }
  finally { rejectionsLoading.value = false }
}
const refreshAll = () => { loadConfig(); loadRejections() }

const saveConfig = async (next, tip) => {
  if (saving.value) return
  saving.value = true
  try { applyRuntime(await request('/admin/ops/runtime/panel-rate-limit', { method: 'PUT', body: next })); message.success(tip) }
  catch (e) { message.error(`保存后台限流配置失败：${e.message}`) }
  finally { saving.value = false }
}
const setEnabled = (enabled) => { if (config.value) saveConfig({ ...config.value, enabled }, '限流开关已更新') }
const selectPreset = (preset) => {
  if (config.value && config.value.preset !== preset) saveConfig({ ...config.value, preset }, '限流档位已更新')
}
const saveCustom = () => {
  if (!config.value) return
  saveConfig({
    ...config.value, preset: 'custom',
    user_rpm: Number(customDraft.user_rpm), heavy_rpm: Number(customDraft.heavy_rpm),
    window_seconds: Number(customDraft.window_seconds),
  }, '自定义限流已保存')
}
/* 恢复默认（POST /reset，返回体与 GET 同构） */
const doReset = async () => {
  saving.value = true
  try { applyRuntime(await request('/admin/ops/runtime/panel-rate-limit/reset', { method: 'POST' })); message.success('已恢复默认限流配置') }
  catch (e) { message.error(`恢复默认配置失败：${e.message}`) }
  finally { saving.value = false }
}

/* ---------- 拒绝快照展示 ---------- */
const visibleTrend = computed(() => (snapshot.value?.trend ?? []).slice(-30))
const trendMax = computed(() => Math.max(1, ...visibleTrend.value.map((p) => p.total)))
const trendHeight = (total) => (total <= 0 ? '4%' : `${Math.max(10, Math.round((total / trendMax.value) * 100))}%`)
const trendTime = (v) => { try { return String(v).slice(11, 16) } catch { return '--:--' } }
const fmtPercent = (v) => (typeof v === 'number' && Number.isFinite(v) ? `${v.toFixed(v >= 10 ? 1 : 2)}%` : '0.00%')
const fmtNum = (v) => (typeof v === 'number' && Number.isFinite(v) ? v.toLocaleString('zh-CN') : '-')

onMounted(refreshAll)
defineExpose({ refresh: refreshAll })
</script>

<template>
  <div class="card">
    <div class="card-h">
      <div>
        <div class="sec-t">入口防护</div>
        <div class="sub-t">聚合展示入口拒绝，并用常用档位保护后台接口</div>
      </div>
      <a-button size="small" :loading="loadingConfig || rejectionsLoading" @click="refreshAll">
        <template #icon><ReloadOutlined /></template>刷新
      </a-button>
    </div>

    <div class="ip-grid">
      <!-- 左：后台接口限流 -->
      <div class="pane">
        <div class="pane-h">
          <div>
            <div class="pane-t">后台接口限流</div>
            <div class="sub-t">只保护后台管理接口，不影响登录与普通模型请求</div>
          </div>
          <a-switch v-if="config" :checked="config.enabled" :loading="saving" @change="setEnabled" />
        </div>

        <div class="preset-grid">
          <button
            v-for="p in PRESETS" :key="p.value" type="button" class="preset-btn"
            :class="{ active: config?.preset === p.value }"
            :disabled="saving || loadingConfig || !config || !config.enabled"
            @click="selectPreset(p.value)"
          >
            <span class="pb-l">{{ p.label }}</span>
            <span class="pb-h">{{ p.hint }}</span>
          </button>
        </div>

        <div v-if="config?.preset === 'custom'" class="custom-box">
          <div class="c-grid">
            <label>每管理员
              <a-input-number v-model:value="customDraft.user_rpm" :min="1" :max="100000" :step="10" size="small" style="width: 100%" />
            </label>
            <label>重查询
              <a-input-number v-model:value="customDraft.heavy_rpm" :min="1" :max="100000" :step="10" size="small" style="width: 100%" />
            </label>
            <label>窗口
              <a-select v-model:value="customDraft.window_seconds" :options="WINDOW_OPTS" size="small" style="width: 100%" />
            </label>
          </div>
          <a-button size="small" type="primary" block :loading="saving" style="margin-top: 8px" @click="saveCustom">保存自定义限流</a-button>
        </div>

        <div v-if="config" class="cur-grid">
          <div class="cur-cell"><div class="dt-k">每管理员</div><b>{{ fmtNum(config.user_rpm) }}</b></div>
          <div class="cur-cell"><div class="dt-k">重查询</div><b>{{ fmtNum(config.heavy_rpm) }}</b></div>
          <div class="cur-cell"><div class="dt-k">窗口</div><b>{{ config.window_seconds }}s</b></div>
        </div>

        <div v-if="runtimeStats" class="rt-line">
          <span>本进程已检查 <b>{{ fmtNum(runtimeStats.checked_total) }}</b></span>
          <span>限流拒绝 <b>{{ fmtNum(runtimeStats.rejected_total) }}</b></span>
          <span>活跃管理员 <b>{{ fmtNum(runtimeStats.active_buckets) }}</b></span>
        </div>

        <div class="reset-line">
          <span class="dim">{{ config?.enabled ? '保护已开启' : '保护已关闭' }}</span>
          <a-button size="small" :disabled="saving || loadingConfig" @click="doReset">恢复默认</a-button>
        </div>
      </div>

      <!-- 右：入口拒绝摘要 -->
      <div class="pane">
        <div class="pane-h">
          <div>
            <div class="pane-t">入口拒绝摘要</div>
            <div class="sub-t">节点：{{ snapshot?.node || '-' }}；只记录分类和计数</div>
          </div>
          <span class="win-tag">最近 60 分钟</span>
        </div>

        <a-spin :spinning="rejectionsLoading">
          <div class="snap-grid">
            <div class="snap-cell"><div class="dt-k">入口请求</div><div class="snap-v">{{ fmtNum(snapshot?.request_count) }}</div></div>
            <div class="snap-cell warn"><div class="dt-k">已拒绝</div><div class="snap-v">{{ fmtNum(snapshot?.rejection_count) }}</div></div>
            <div class="snap-cell danger"><div class="dt-k">拒绝率</div><div class="snap-v">{{ fmtPercent(snapshot?.rejection_rate_percent) }}</div></div>
          </div>

          <div class="sub-h">拒绝趋势 <span class="dim">每分钟</span></div>
          <div v-if="visibleTrend.length" class="trend">
            <div v-for="p in visibleTrend" :key="p.bucket_start" class="tbar" :title="`${trendTime(p.bucket_start)} · ${p.total} 次`">
              <span :style="{ height: trendHeight(p.total) }" />
            </div>
          </div>
          <div v-else class="empty">当前窗口没有入口拒绝</div>

          <div class="sub-h">主要类型</div>
          <div v-if="snapshot?.top_reasons?.length" class="reasons">
            <span v-for="it in snapshot.top_reasons" :key="it.reason" class="reason-chip">
              {{ REASON_LABEL[it.reason] || it.reason }} <b>{{ fmtNum(it.count) }}</b>
            </span>
          </div>
          <div v-else class="empty">当前窗口没有入口拒绝</div>
        </a-spin>
      </div>
    </div>
  </div>
</template>

<style scoped>
.card { background: var(--card); border: 1px solid var(--hr); border-radius: 10px; padding: 12px 14px; min-width: 0; }
.card-h { display: flex; align-items: flex-start; justify-content: space-between; gap: 10px; margin-bottom: 10px; flex-wrap: wrap; }
.sec-t, .pane-t { font-size: 13px; font-weight: 500; }
.sub-t { font-size: 11px; color: var(--text2); margin-top: 2px; }
.ip-grid { display: grid; grid-template-columns: 2fr 3fr; gap: 12px; }
.pane { border: 1px solid var(--hr); border-radius: 8px; padding: 12px; min-width: 0; }
.pane-h { display: flex; align-items: flex-start; justify-content: space-between; gap: 10px; margin-bottom: 10px; }
/* 档位 2×2：左 pane 较窄，4 列时「按实际后台规模设置」等长文案
 * 会 nowrap 溢出按钮、叠到右侧摘要面板上（用户反馈重叠） */
.preset-grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 6px; }
.preset-btn { border: 1px solid var(--hr); background: transparent; border-radius: 8px; padding: 8px 10px; text-align: left; cursor: pointer; transition: all 0.15s ease; min-width: 0; overflow: hidden; }
.preset-btn:hover:not(:disabled) { border-color: #3b82f6; }
.preset-btn.active { border-color: #3b82f6; background: rgba(59, 130, 246, 0.1); }
.preset-btn:disabled { opacity: 0.5; cursor: not-allowed; }
.pb-l { display: block; font-size: 12px; font-weight: 500; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.pb-h { display: block; font-size: 10px; color: var(--text2); margin-top: 2px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.custom-box { margin-top: 8px; border: 1px solid rgba(59, 130, 246, 0.4); background: rgba(59, 130, 246, 0.06); border-radius: 8px; padding: 10px; }
.c-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; }
.c-grid label { display: flex; flex-direction: column; gap: 4px; font-size: 11px; color: var(--text2); }
.cur-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 6px; margin-top: 10px; }
.cur-cell { background: rgba(127, 127, 127, 0.06); border-radius: 8px; padding: 6px 10px; font-size: 13px; }
.dt-k { font-size: 10px; color: var(--text2); }
.rt-line { display: flex; flex-wrap: wrap; gap: 4px 14px; margin-top: 10px; font-size: 11px; color: var(--text2); }
.reset-line { display: flex; align-items: center; justify-content: space-between; gap: 10px; margin-top: 12px; padding-top: 10px; border-top: 1px solid var(--hr); }
.dim { font-size: 11px; color: var(--text2); }
.win-tag { font-size: 11px; color: var(--text2); background: rgba(127, 127, 127, 0.1); border-radius: 10px; padding: 2px 10px; }
.snap-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; }
.snap-cell { background: rgba(127, 127, 127, 0.06); border-radius: 8px; padding: 8px 12px; }
.snap-cell.warn { background: rgba(234, 179, 8, 0.1); }
.snap-cell.danger { background: rgba(225, 29, 72, 0.08); }
.snap-v { font-size: 18px; font-weight: 600; margin-top: 2px; }
.sub-h { font-size: 12px; font-weight: 500; margin: 12px 0 6px; }
.trend { display: flex; align-items: flex-end; gap: 2px; height: 84px; background: rgba(127, 127, 127, 0.05); border-radius: 8px; padding: 8px 10px; }
.tbar { flex: 1; min-width: 0; height: 100%; display: flex; align-items: flex-end; cursor: default; }
.tbar span { display: block; width: 100%; background: #f59e0b; border-radius: 2px 2px 0 0; min-height: 3px; transition: height 0.2s ease; }
.tbar:hover span { background: #d97706; }
.reasons { display: flex; flex-wrap: wrap; gap: 6px; }
.reason-chip { font-size: 11px; color: var(--text2); border: 1px solid var(--hr); border-radius: 12px; padding: 2px 10px; }
.empty { padding: 16px 0; text-align: center; font-size: 12px; color: var(--text2); }
@media (max-width: 1000px) { .ip-grid { grid-template-columns: 1fr; } }
</style>
