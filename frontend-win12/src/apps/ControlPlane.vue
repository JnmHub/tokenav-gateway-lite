<script setup>
/* 控制面 + Codex 客户端准入（对照旧 settings.ts 592-660 行）
 * GET/PUT /admin/gateway-lite/config（区域网关身份与主站对接）
 * GET/PUT /admin/settings/codex-client-admission（Codex 客户端准入策略 + 实时统计）
 * Win12 表单规范：标签列固定 112px 右对齐、输入框统一 240px 左对齐成一条竖线、
 * 行高统一 40px、行 hover 浅灰；卡片极淡半透毛玻璃、块间 1px 分割线；
 * 保存按钮 36px 高位于区块右下、与表单留 16px。
 * 待迭代：admin_sync_key/control_plane_token 轮换（后端 reveal 接口） */
import { ref, onMounted } from 'vue'
import { request } from '../api'
import { message } from 'ant-design-vue'
import { openApp } from '../core/apps'

const loading = ref(true)
const savingCp = ref(false)
const savingCx = ref(false)
const cp = ref(null)   // control plane config
const cx = ref(null)   // codex admission { settings, stats }
const err = ref('')

const MODES = [
  { value: 'audit_only', label: 'audit_only（只审计）' },
  { value: 'enforce', label: 'enforce（强制拦截）' },
  { value: 'off', label: 'off（关闭）' },
]

const load = async () => {
  loading.value = true
  try {
    const [a, b] = await Promise.all([
      request('/admin/gateway-lite/config', { method: 'GET' }),
      request('/admin/settings/codex-client-admission', { method: 'GET' }),
    ])
    cp.value = a
    cx.value = { ...b, allowedClientsText: (b.settings.allowed_clients || []).join(','), deniedClientsText: (b.settings.denied_clients || []).join(',') }
    err.value = ''
  } catch (e) {
    err.value = e.message
  } finally {
    loading.value = false
  }
}

const saveCp = async () => {
  savingCp.value = true
  try {
    await request('/admin/gateway-lite/config', { method: 'PUT', body: cp.value })
    message.success('已保存')
  } catch (e) {
    message.error(`保存失败：${e.message}`)
  } finally {
    savingCp.value = false
  }
}

const saveCx = async () => {
  savingCx.value = true
  try {
    const body = {
      ...cx.value.settings,
      allowed_clients: cx.value.allowedClientsText.split(',').map((s) => s.trim()).filter(Boolean),
      denied_clients: cx.value.deniedClientsText.split(',').map((s) => s.trim()).filter(Boolean),
    }
    await request('/admin/settings/codex-client-admission', { method: 'PUT', body })
    message.success('已保存')
    await load()
  } catch (e) {
    message.error(`保存失败：${e.message}`)
  } finally {
    savingCx.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="cpcx">
    <a-spin :spinning="loading">
      <!-- 区块一：控制面（主站）对接 -->
      <div v-if="cp" class="card">
        <div class="card-title">控制面（主站）对接</div>
        <p class="static">主站身份由持久绑定管理。解绑、改绑和实际连接状态请打开主站绑定。</p>
        <a-button @click="openApp('gatewayBinding')">管理主站绑定</a-button>
        <div class="form">
          <div class="frow"><label>区域</label><a-input v-model:value="cp.region" disabled class="fld" /></div>
          <div class="frow"><label>网关代码</label><a-input v-model:value="cp.gateway_code" disabled class="fld" /></div>
          <div class="frow"><label>Redis 前缀</label><a-input v-model:value="cp.redis_prefix" disabled class="fld" /></div>
          <div class="frow"><label>后台登录路径</label><a-input v-model:value="cp.admin_login_path" class="fld" /></div>
          <div class="frow"><label>登录标题</label><a-input v-model:value="cp.login_title" class="fld" /></div>
          <div class="frow"><label>登录副标题</label><a-input v-model:value="cp.login_subtitle" class="fld" /></div>
          <div class="frow"><label>主站地址</label><a-input v-model:value="cp.control_plane_url" disabled class="fld" /></div>
          <div class="frow"><label>同步 Token</label><span class="static">{{ cp.control_plane_token_configured ? '已配置' : '未配置' }}</span></div>
          <div class="frow"><label>同步密钥</label><span class="static">{{ cp.admin_sync_key_configured ? '已配置' : '未配置' }}</span></div>
          <div class="frow"><label>主站超时（毫秒；后台≥10s、鉴权≤3s）</label><a-input-number v-model:value="cp.control_plane_timeout_ms" :min="100" class="fld" /></div>
          <div class="frow"><label>健康上报间隔（秒）</label><a-input-number v-model:value="cp.runtime_health_interval_seconds" :min="1" class="fld" /></div>
          <div class="frow"><label>活跃窗口（秒）</label><a-input-number v-model:value="cp.runtime_active_window_seconds" :min="10" class="fld" /></div>
        </div>
        <div class="savebar">
          <a-button type="primary" class="save" :loading="savingCp" @click="saveCp">保存控制面配置</a-button>
        </div>
      </div>

      <!-- 区块分割线 -->
      <div class="sep" />

      <!-- 区块二：Codex 客户端准入 -->
      <div v-if="cx" class="card">
        <div class="card-title">Codex 客户端准入</div>
        <div class="form">
          <div class="frow"><label>模式</label><a-select v-model:value="cx.settings.mode" :options="MODES" class="fld" /></div>
          <div class="frow"><label>app_server 放行</label><a-switch v-model:checked="cx.settings.app_server_enabled" /></div>
          <div class="frow"><label>最低版本</label><a-input v-model:value="cx.settings.min_version" placeholder="留空不限" class="fld" /></div>
          <div class="frow"><label>最高版本</label><a-input v-model:value="cx.settings.max_version" placeholder="留空不限" class="fld" /></div>
          <div class="frow"><label>允许客户端</label><a-input v-model:value="cx.allowedClientsText" placeholder="逗号分隔" class="fld" /></div>
          <div class="frow"><label>拒绝客户端</label><a-input v-model:value="cx.deniedClientsText" placeholder="逗号分隔" class="fld" /></div>
        </div>
        <div class="stats" v-if="cx.stats">
          <div class="scard"><div class="k">已检查</div><div class="v">{{ cx.stats.inspected ?? 0 }}</div></div>
          <div class="scard"><div class="k">已拦截</div><div class="v">{{ cx.stats.denied ?? 0 }}</div></div>
          <div class="scard"><div class="k">审计违规</div><div class="v">{{ cx.stats.audit_violations ?? 0 }}</div></div>
          <div class="scard"><div class="k">检出客户端</div><div class="v">{{ (cx.stats.clients || []).length }}</div></div>
        </div>
        <div class="savebar">
          <a-button type="primary" class="save" :loading="savingCx" @click="saveCx">保存准入策略</a-button>
        </div>
      </div>
    </a-spin>
  </div>
</template>

<style scoped>
.cpcx { height: 100%; overflow-y: auto; }

/* 区块：极淡半透毛玻璃（不要实色浅白块） */
.card {
  padding: 16px 20px;
  border-radius: 10px;
  background: color-mix(in srgb, var(--surface) 55%, transparent);
  backdrop-filter: blur(12px) saturate(1.4);
  -webkit-backdrop-filter: blur(12px) saturate(1.4);
  border: 1px solid var(--hr);
}

/* 区块标题：字重 500，与表单留 16px */
.card-title { font-weight: 500; font-size: 15px; margin-bottom: 16px; }

/* 两大区块之间 1px 浅灰水平分割线 */
.sep { height: 1px; background: var(--hr); margin: 20px 4px; }

/* 表单：标签列固定 112px 右对齐，控件统一 240px 左对齐成一条竖线；行高统一 40px */
.form { display: flex; flex-direction: column; }
.frow {
  display: flex;
  align-items: center;
  min-height: 40px;
  padding: 2px 8px;
  margin: 0 -8px;
  border-radius: 8px;
  transition: background 100ms;
}
.frow:hover { background: rgba(0, 0, 0, 0.03); }
:root.dark .frow:hover { background: rgba(255, 255, 255, 0.045); }
.frow > label {
  flex: none;
  width: 112px;
  text-align: right;
  font-weight: 400;
  font-size: 13px;
  color: var(--text2);
  margin-right: 12px;
}
.fld { width: 240px !important; flex: none; }

/* 状态文字：次要灰，与控件留 12px */
.static { font-size: 13px; color: var(--text2); margin-left: 12px; }

/* 保存行：按钮 36px 高，右下角，与表单区留 16px */
.savebar { display: flex; justify-content: flex-end; margin-top: 16px; }
.save { height: 36px; padding: 0 16px; }

/* 实时统计卡 */
.stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(120px, 1fr)); gap: 8px; margin-top: 14px; }
.scard { padding: 8px 12px; border-radius: 8px; background: color-mix(in srgb, var(--surface) 60%, transparent); border: 1px solid var(--hr); }
.scard .k { font-size: 12px; color: var(--text2); }
.scard .v { font-size: 16px; font-weight: 500; }

@media (max-width: 900px) {
  .fld { width: 100% !important; flex: 1; }
  .frow { flex-wrap: wrap; }
  .frow > label { width: 100%; text-align: left; margin-bottom: 4px; }
}
</style>
