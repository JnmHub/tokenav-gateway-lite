<script setup>
/* 概览指标卡（补入口）
 * 接口：GET /admin/dashboard/stats（该接口不接收日期参数，恒返回「全局累计 + 今日」快照）
 * 实测字段（curl 确认）：
 *   active_users / hourly_active_users / total_users / today_new_users
 *   active_api_keys / total_api_keys
 *   total_accounts / normal_accounts / error_accounts / ratelimit_accounts / overload_accounts
 *   average_duration_ms / rpm / tpm / uptime(秒)
 *   today_requests / today_tokens / today_input_tokens / today_output_tokens /
 *   today_cache_creation_tokens / today_cache_read_tokens / today_cost / today_actual_cost
 *   total_requests / total_tokens / total_input_tokens / total_output_tokens /
 *   total_cache_creation_tokens / total_cache_read_tokens / total_cost / total_actual_cost
 *   stats_updated_at（RFC3339）/ stats_stale
 * 与 snapshot-v2 的差异：这里补充 Token 分项构成、运行时长、预聚合新鲜度（快照不返回）。
 * 备注：本机 lite 实测 uptime/hourly_active_users/rpm/tpm 可能为 0，属后端真实采样值，不做伪造。 */
import { ref, onMounted } from 'vue'
import { ReloadOutlined } from '@ant-design/icons-vue'
import { message } from 'ant-design-vue'
import { request } from '../../api'

const loading = ref(false)
const err = ref('')
const s = ref(null)

const load = async () => {
  loading.value = true
  try {
    s.value = (await request('/admin/dashboard/stats', { method: 'GET' })) || null
    err.value = ''
  } catch (e) {
    err.value = e.message
    message.error(`概览指标加载失败：${e.message}`)
  } finally {
    loading.value = false
  }
}
onMounted(load)

const fmt = (n) => (n === null || n === undefined || n === '' ? '-' : Number(n).toLocaleString('zh-CN'))
const fmtTokens = (n) => {
  const v = Number(n)
  if (n === null || n === undefined || !Number.isFinite(v)) return '-'
  if (Math.abs(v) >= 1e9) return (v / 1e9).toFixed(2) + 'B'
  if (Math.abs(v) >= 1e6) return (v / 1e6).toFixed(2) + 'M'
  if (Math.abs(v) >= 1e3) return (v / 1e3).toFixed(1) + 'K'
  return String(Math.round(v))
}
const fmtDuration = (ms) => {
  const n = Number(ms)
  if (ms === null || ms === undefined || !Number.isFinite(n)) return '-'
  return n >= 1000 ? `${(n / 1000).toFixed(2)}s` : `${Math.round(n)}ms`
}
// uptime 为秒；>1 天显示「N 天 h 小时」，否则「h 小时 m 分」
const fmtUptime = (sec) => {
  const n = Number(sec)
  if (sec === null || sec === undefined || !Number.isFinite(n)) return '-'
  const d = Math.floor(n / 86400)
  const h = Math.floor((n % 86400) / 3600)
  const m = Math.floor((n % 3600) / 60)
  return d > 0 ? `${d} 天 ${h} 小时` : h > 0 ? `${h} 小时 ${m} 分` : `${m} 分 ${Math.floor(n % 60)} 秒`
}
const fmtTime = (t) => (t ? String(t).replace('T', ' ').replace('Z', '').slice(0, 19) : '-')
</script>

<template>
  <div class="sec">
    <div class="phead">
      <h3>系统概览指标</h3>
      <span class="hint">GET /admin/dashboard/stats · 全局累计 + 今日快照</span>
      <span v-if="s?.stats_stale" class="tag warn">预聚合数据已过期</span>
      <div class="actions">
        <a-button size="small" :loading="loading" @click="load">
          <template #icon><ReloadOutlined /></template>
          刷新
        </a-button>
      </div>
    </div>

    <div v-if="err && !s" class="empty">{{ err }}</div>
    <a-spin v-else :spinning="loading && !s">
      <div class="mgrid">
        <div class="mcard">
          <div class="mk">用户</div>
          <div class="mv">{{ fmt(s?.active_users) }}</div>
          <div class="mrow"><span>当前小时活跃</span><b>{{ fmt(s?.hourly_active_users) }}</b></div>
          <div class="mrow"><span>用户总数</span><b>{{ fmt(s?.total_users) }}</b></div>
          <div class="mrow"><span>今日新增</span><b class="ok">+{{ fmt(s?.today_new_users) }}</b></div>
        </div>

        <div class="mcard">
          <div class="mk">API Key</div>
          <div class="mv">{{ fmt(s?.active_api_keys) }}</div>
          <div class="mrow"><span>活跃 Key</span><b>{{ fmt(s?.active_api_keys) }}</b></div>
          <div class="mrow"><span>Key 总数</span><b>{{ fmt(s?.total_api_keys) }}</b></div>
        </div>

        <div class="mcard">
          <div class="mk">上游账号</div>
          <div class="mv">{{ fmt(s?.total_accounts) }}</div>
          <div class="mrow"><span>正常</span><b class="ok">{{ fmt(s?.normal_accounts) }}</b></div>
          <div class="mrow"><span>错误</span><b class="bad">{{ fmt(s?.error_accounts) }}</b></div>
          <div class="mrow"><span>限流 / 过载</span><b>{{ fmt(s?.ratelimit_accounts) }} / {{ fmt(s?.overload_accounts) }}</b></div>
        </div>

        <div class="mcard">
          <div class="mk">性能</div>
          <div class="mv">{{ fmtDuration(s?.average_duration_ms) }}</div>
          <div class="mrow"><span>平均响应时长</span><b>{{ fmtDuration(s?.average_duration_ms) }}</b></div>
          <div class="mrow"><span>RPM</span><b>{{ fmtTokens(s?.rpm) }}</b></div>
          <div class="mrow"><span>TPM</span><b>{{ fmtTokens(s?.tpm) }}</b></div>
        </div>

        <div class="mcard">
          <div class="mk">今日 Token 构成</div>
          <div class="mv">{{ fmtTokens(s?.today_tokens) }}</div>
          <div class="mrow"><span>Input</span><b>{{ fmtTokens(s?.today_input_tokens) }}</b></div>
          <div class="mrow"><span>Output</span><b>{{ fmtTokens(s?.today_output_tokens) }}</b></div>
          <div class="mrow"><span>Cache 创建 / 读取</span><b>{{ fmtTokens(s?.today_cache_creation_tokens) }} / {{ fmtTokens(s?.today_cache_read_tokens) }}</b></div>
        </div>

        <div class="mcard">
          <div class="mk">累计 Token 构成</div>
          <div class="mv">{{ fmtTokens(s?.total_tokens) }}</div>
          <div class="mrow"><span>Input</span><b>{{ fmtTokens(s?.total_input_tokens) }}</b></div>
          <div class="mrow"><span>Output</span><b>{{ fmtTokens(s?.total_output_tokens) }}</b></div>
          <div class="mrow"><span>Cache 创建 / 读取</span><b>{{ fmtTokens(s?.total_cache_creation_tokens) }} / {{ fmtTokens(s?.total_cache_read_tokens) }}</b></div>
        </div>

        <div class="mcard">
          <div class="mk">运行状态</div>
          <div class="mv">{{ fmtUptime(s?.uptime) }}</div>
          <div class="mrow"><span>今日请求</span><b>{{ fmt(s?.today_requests) }} / 累计 {{ fmt(s?.total_requests) }}</b></div>
          <div class="mrow"><span>今日实际成本</span><b class="ok">${{ Number(s?.today_actual_cost ?? 0).toFixed(4) }}</b></div>
          <div class="mrow"><span>统计更新于</span><b>{{ fmtTime(s?.stats_updated_at) }}</b></div>
        </div>
      </div>
    </a-spin>
  </div>
</template>

<style scoped>
.sec { display: flex; flex-direction: column; }
.phead { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 10px; }
.phead h3 { margin: 0; font-size: 13px; font-weight: 600; color: var(--text); }
.hint { font-size: 12px; color: var(--text2); }
.actions { margin-left: auto; display: flex; align-items: center; gap: 8px; }
.tag.warn { font-size: 11px; padding: 1px 6px; border-radius: 4px; color: #d18a2d; border: 1px solid color-mix(in srgb, #d18a2d 40%, transparent); }
.mgrid { display: grid; grid-template-columns: repeat(auto-fit, minmax(210px, 1fr)); gap: 12px; }
.mcard { padding: 12px 14px; border-radius: 10px; background: var(--card); border: 1px solid var(--hr); }
.mk { font-size: 12px; color: var(--text2); }
.mv { margin-top: 4px; font-size: 22px; font-weight: 600; line-height: 1.2; color: var(--text); }
.mrow { display: flex; justify-content: space-between; gap: 10px; margin-top: 5px; font-size: 12px; color: var(--text2); }
.mrow b { font-weight: 500; color: var(--text); }
.mrow b.ok { color: #2f9e63; }
.mrow b.bad { color: #e05252; }
.empty { font-size: 12px; color: var(--text2); }
/* 移动端：卡片单列 */
@media (max-width: 768px) {
  .mgrid { grid-template-columns: 1fr; }
}
</style>
