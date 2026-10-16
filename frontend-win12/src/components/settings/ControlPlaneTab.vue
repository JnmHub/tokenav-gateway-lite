<script setup>
/* ============================================================
 * 主站 tab：主站连接 + 私有登录入口（对照旧 SettingsView.vue「主站」tab）
 * 接口：GET/PUT /admin/gateway-lite/config
 * - control_plane_token 为密码框：留空表示不修改（payload 仅在有值时携带）
 * - 两个分区都通过同一全量 payload 保存（服务端语义如此），
 *   私有入口保存前做正则校验 /^\/[A-Za-z0-9][A-Za-z0-9_-]{2,63}$/
 * - 保存成功后回显服务端响应（token 清空），并提示是否需要手动同步
 * ============================================================ */
import { ref, onMounted } from 'vue'
import { request as apiRequest, isCancellation } from '../../api'
import { useRequestLifetime } from '../../composables/useRequestLifetime'
import { useDraftBaseline, cloneDraft, retainEdits } from '../../composables/useDraftBaseline'
import { createLatestRequest } from '../../api/latestRequest'
import { assertGatewayConfig, gatewayConnectionFields, gatewayLoginFields, saveGatewayConfig } from '../../api/gatewayConfig'
import { message } from 'ant-design-vue'
import { clampInt } from './fields'
import W12Button from '../design/W12Button.vue'
import W12Input from '../design/W12Input.vue'

const LOGIN_PATH_RE = new RegExp("^/[A-Za-z0-9][A-Za-z0-9_-]{2,63}$")
const loading = ref(true)
const err = ref('')
const saving = ref(false)
const f = ref(null)
const lane = createLatestRequest()
const lifetime = useRequestLifetime(() => { lane.cancel(); f.value = null; err.value = ''; draft.clear() })
const draft = useDraftBaseline(() => f.value, () => saving.value)
const request = (path, options) => apiRequest(path, { ...options, ...lifetime, signal: options?.signal || lifetime.signal })
const apply = (d) => { f.value = { ...assertGatewayConfig(d), control_plane_token: '', admin_sync_key: '' } }
const load = async () => {
  if (saving.value) return
  const turn = lane.begin()
  loading.value = true
  try {
    const data = await request('/admin/gateway-lite/config', { method: 'GET', signal: AbortSignal.any([turn.signal, lifetime.signal]) })
    if (!turn.isCurrent() || !lifetime.isAlive()) return
    const edits = draft.edits()
    apply(data); draft.accept(); Object.assign(f.value, edits)
    err.value = ''
  } catch (e) { if (turn.isCurrent() && !isCancellation(e)) err.value = e.message }
  finally { if (turn.isCurrent() && lifetime.isAlive()) loading.value = false }
}
const buildPayload = (source, submitted) => {
  const keys = source === 'login' ? gatewayLoginFields : gatewayConnectionFields
  const body = Object.fromEntries(keys.map(key => [key, submitted[key]]))
  if (source !== 'login') {
    for (const key of ['region', 'gateway_code', 'redis_prefix', 'control_plane_url']) body[key] = String(body[key] || '').trim()
    body.region ||= 'default'
    for (const [key, fallback, min, max] of [
      ['control_plane_timeout_ms', 1000, 100, 30000], ['runtime_health_interval_seconds', 15, 5, 3600],
      ['runtime_active_window_seconds', 300, 30, 86400], ['config_sync_interval_seconds', 30, 5, 3600],
      ['cache_invalidation_interval_seconds', 5, 1, 3600], ['usage_queue_pending_alert_threshold', 1000, 1, 10000000],
      ['usage_queue_dead_alert_threshold', 1, 1, 10000000],
    ]) body[key] = clampInt(body[key], fallback, min, max)
    body.legacy_snapshot_scheduler_authority = Boolean(submitted.legacy_snapshot_scheduler_authority)
    // 后台仅接收非空秘密；配置状态只能展示，不能作为密钥重新写回。
    for (const key of ['control_plane_token', 'admin_sync_key']) {
      if (submitted[key].trim()) body[key] = submitted[key].trim()
    }
  } else for (const key of keys) body[key] = String(body[key] || '').trim()
  return body
}
const persist = async (source) => {
  if (saving.value || !f.value || !lifetime.isAlive()) return
  if (source === 'login' && !LOGIN_PATH_RE.test(String(f.value.admin_login_path || '').trim())) {
    message.error('私有入口必须是以 / 开头的单段路径，只能包含字母、数字、下划线和连字符'); return
  }
  const submitted = cloneDraft(f.value), body = buildPayload(source, submitted)
  lane.cancel(); loading.value = false; saving.value = true
  try {
    const updated = await saveGatewayConfig(body, lifetime)
    const edits = draft.edits()
    // 只清除本次提交且没有续改的字段；另一张卡片草稿继续保留。
    for (const key of Object.keys(body)) if (JSON.stringify(f.value[key]) === JSON.stringify(submitted[key])) delete edits[key]
    Object.assign(edits, retainEdits(f.value, submitted))
    apply(updated); draft.accept(); Object.assign(f.value, edits)
    message.success(source === 'login' ? '私有登录入口已保存并立即生效' : '配置已保存并即时生效')
  } catch (e) { if (!isCancellation(e) && lifetime.isAlive()) message.error('保存失败：' + e.message) }
  finally { saving.value = false }
}

onMounted(load)
</script>

<template>
  <a-spin :spinning="loading">
    <div class="sw w12-design">
      <div v-if="err" class="card"><W12Button variant="danger" @click="load">加载失败：{{ err }}（点击重试）</W12Button></div>
      <template v-if="f">
        <!-- 主站连接 -->
        <div class="card">
          <div class="card-title">主站连接</div>
          <div class="card-sub">保存后会写入 config.yaml，并立即应用到当前运行中的 tokenav gateway 进程。</div>
          <div class="form">
            <div class="frow"><label>区域标识</label><W12Input v-model:value="f.region" class="fld" placeholder="default" /></div>
            <div class="frow"><label>主站 API 地址</label><W12Input v-model:value="f.control_plane_url" class="fld" placeholder="http://127.0.0.1:8088" /></div>
            <div class="frow">
              <label>主站内部令牌</label>
              <a-input-password v-model:value="f.control_plane_token" class="fld" autocomplete="new-password"
                :placeholder="f.control_plane_token_configured ? '已配置，留空表示不修改' : '未配置'" />
            </div>
            <div class="frow">
              <label>管理同步密钥</label>
              <a-input-password v-model:value="f.admin_sync_key" class="fld" autocomplete="new-password"
                :placeholder="f.admin_sync_key_configured ? '已配置，留空表示不修改' : '未配置'" />
            </div>
            <div class="frow"><label>旧快照调度权威</label><div class="fmain"><a-switch v-model:checked="f.legacy_snapshot_scheduler_authority" /><span class="static">允许旧主站快照写入调度缓存；默认关闭，账号与分组以本地后台为准</span></div></div>
            <div class="frow"><label>网关编号</label><W12Input v-model:value="f.gateway_code" class="fld" placeholder="openai-hk-t1" /></div>
            <div class="frow"><label>Redis 前缀</label><W12Input v-model:value="f.redis_prefix" class="fld" placeholder="jnm:gateway-lite" /></div>
            <div class="frow"><label>主站请求超时（毫秒；后台调用不低于 10s、鉴权/租约上限 3s）</label><a-input-number v-model:value="f.control_plane_timeout_ms" :min="100" :max="30000" class="fld-n" /></div>
            <div class="frow"><label>运行心跳间隔（秒）</label><a-input-number v-model:value="f.runtime_health_interval_seconds" :min="5" :max="3600" class="fld-n" /></div>
            <div class="frow"><label>活跃窗口（秒）</label><a-input-number v-model:value="f.runtime_active_window_seconds" :min="30" :max="86400" class="fld-n" /></div>
            <div class="frow"><label>主站配置同步间隔（秒）</label><a-input-number v-model:value="f.config_sync_interval_seconds" :min="5" :max="3600" class="fld-n" /></div>
            <div class="frow"><label>缓存失效同步间隔（秒）</label><a-input-number v-model:value="f.cache_invalidation_interval_seconds" :min="1" :max="3600" class="fld-n" /></div>
            <div class="frow"><label>用量队列积压告警阈值</label><a-input-number v-model:value="f.usage_queue_pending_alert_threshold" :min="1" :max="10000000" class="fld-n" /></div>
            <div class="frow"><label>死信队列告警阈值</label><a-input-number v-model:value="f.usage_queue_dead_alert_threshold" :min="1" :max="10000000" class="fld-n" /></div>
          </div>
          <a-alert style="margin-top: 12px" type="info" show-icon :message="`配置文件：${f.config_path || 'config.yaml'}。${f.restart_required ? '配置已保存后当前版本会尽量热更新；如遇旧队列或旧缓存残留，可手动同步一次。' : '所有主站连接配置已即时生效，并已触发一次配置同步。'}`" />
          <div class="savebar"><W12Button variant="primary" class="save" :loading="saving" @click="persist('control-plane')">保存主站配置</W12Button></div>
        </div>
        <!-- 私有登录入口 -->
        <div class="card">
          <div class="card-title">私有登录入口</div>
          <div class="card-sub">只有正确入口会显示管理员登录表单，其他地址统一显示 404。</div>
          <div class="form">
            <div class="frow"><label>私有入口路径</label><W12Input v-model:value="f.admin_login_path" class="fld mono" placeholder="/gateway-admin" autocomplete="off" /></div>
            <div class="frow"><label>登录标题</label><W12Input v-model:value="f.login_title" class="fld" :maxlength="100" /></div>
            <div class="frow"><label>登录说明</label><W12Input v-model:value="f.login_subtitle" class="fld-full" :maxlength="180" /></div>
          </div>
          <a-alert style="margin-top: 12px" type="warning" show-icon message="修改入口后旧地址立即失效。私有路径只用于防止误入，不能替代强密码、人机验证和双因素认证。" />
          <div class="savebar"><W12Button variant="primary" class="save" :loading="saving" @click="persist('login')">保存登录入口</W12Button></div>
        </div>
      </template>
    </div>
  </a-spin>
</template>

<style scoped>
@import './settings.css';
</style>

