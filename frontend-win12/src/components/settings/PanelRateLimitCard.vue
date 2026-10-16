<script setup>
/* ============================================================
 * 面板 API 限流卡（独立子接口，非总表）
 * - GET/PUT /admin/ops/runtime/panel-rate-limit
 *   → { enabled, preset, user_rpm, heavy_rpm, window_seconds,
 *       exempt_admin, public_ip_rpm }
 * - POST /admin/ops/runtime/panel-rate-limit/reset 恢复默认
 * - preset 档位对照 internal/service/setting_panel_rate_limit.go：
 *   default(240/60/60) / relaxed(600/180/60) / strict(120/30/60) / custom
 *   非 custom 档位后端忽略自定义数字；手动改数字时前端自动切到 custom
 * ============================================================ */
import { ref, onMounted } from 'vue'
import { message } from 'ant-design-vue'
import { SafetyCertificateOutlined, UndoOutlined } from '@ant-design/icons-vue'
import { request } from '../../api'

const PRESETS = {
  default: { user_rpm: 240, heavy_rpm: 60, window_seconds: 60 },
  relaxed: { user_rpm: 600, heavy_rpm: 180, window_seconds: 60 },
  strict: { user_rpm: 120, heavy_rpm: 30, window_seconds: 60 },
}

const loading = ref(true)
const saving = ref(false)
const p = ref(null)

const load = async () => {
  loading.value = true
  try {
    /* 响应包装：{ settings, runtime_stats }，配置取 settings 字段 */
    const res = await request('/admin/ops/runtime/panel-rate-limit', { method: 'GET' })
    p.value = (res && res.settings) || res
  } catch (e) {
    message.error(e.message)
  } finally {
    loading.value = false
  }
}

/* 切换预设档位：自动填入对应数值（后端非 custom 档位忽略数字，这里仅做回显） */
const applyPreset = (preset) => {
  p.value.preset = preset
  if (PRESETS[preset]) Object.assign(p.value, PRESETS[preset])
}

/* 手动改数字：档位视为自定义（custom），否则后端会忽略这些数字 */
const onNumberChange = () => {
  if (p.value.preset !== 'custom') {
    const ref = p.value.preset && PRESETS[p.value.preset]
    const match = ref && ref.user_rpm === p.value.user_rpm && ref.heavy_rpm === p.value.heavy_rpm
      && ref.window_seconds === p.value.window_seconds
    if (!match) p.value.preset = 'custom'
  }
}

const doSave = async () => {
  /* 前端校验：重接口 RPM 不得大于普通接口 RPM（与表单提示一致），不通过则不发请求 */
  const userRpm = Number(p.value.user_rpm) || 1
  const heavyRpm = Number(p.value.heavy_rpm) || 1
  if (heavyRpm > userRpm) {
    message.error('保存失败：重接口 RPM 不能大于普通接口 RPM')
    return
  }
  saving.value = true
  try {
    const res = await request('/admin/ops/runtime/panel-rate-limit', {
      method: 'PUT',
      body: {
        enabled: !!p.value.enabled,
        preset: p.value.preset || 'custom',
        user_rpm: userRpm,
        heavy_rpm: heavyRpm,
        window_seconds: Number(p.value.window_seconds) || 60,
        exempt_admin: !!p.value.exempt_admin,
        public_ip_rpm: Number(p.value.public_ip_rpm) || 0,
      },
    })
    const next = (res && res.settings) || res
    if (next && typeof next === 'object') p.value = next
    message.success('已保存')
  } catch (e) {
    message.error(`保存失败：${e.message}`)
  } finally {
    saving.value = false
  }
}

const doReset = async () => {
  saving.value = true
  try {
    const res = await request('/admin/ops/runtime/panel-rate-limit/reset', { method: 'POST' })
    const next = (res && res.settings) || res
    if (next && typeof next === 'object') p.value = next
    message.success('已恢复默认（240/60，60 秒窗口）')
  } catch (e) {
    message.error(e.message)
  } finally {
    saving.value = false
  }
}

const presetBtns = [
  { v: 'default', label: '默认' },
  { v: 'relaxed', label: '宽松' },
  { v: 'strict', label: '严格' },
  { v: 'custom', label: '自定义' },
]

onMounted(load)
</script>

<template>
  <div class="card">
    <a-spin :spinning="loading">
      <div class="card-title"><SafetyCertificateOutlined /> 面板 API 限流</div>
      <div v-if="p" class="form">
        <div class="frow">
          <label>启用限流</label>
          <div class="fmain"><a-switch v-model:checked="p.enabled" /><span class="static">限制后台管理接口请求频率</span></div>
        </div>
        <div class="frow">
          <label>限流档位</label>
          <div class="fmain preset-group">
            <button v-for="b in presetBtns" :key="b.v" class="preset-btn" :class="{ on: p.preset === b.v }"
              @click="b.v !== 'custom' ? applyPreset(b.v) : (p.preset = 'custom')">{{ b.label }}</button>
          </div>
        </div>
        <div class="frow">
          <label>普通接口 RPM</label>
          <div class="fmain"><a-input-number v-model:value="p.user_rpm" :min="1" :max="100000" class="fld-n" @change="onNumberChange" /></div>
        </div>
        <div class="frow">
          <label>重接口 RPM</label>
          <div class="fmain"><a-input-number v-model:value="p.heavy_rpm" :min="1" :max="100000" class="fld-n" @change="onNumberChange" /><span class="static">不能大于普通接口</span></div>
        </div>
        <div class="frow">
          <label>窗口（秒）</label>
          <div class="fmain"><a-input-number v-model:value="p.window_seconds" :min="10" :max="3600" class="fld-n" @change="onNumberChange" /></div>
        </div>
        <div class="frow">
          <label>管理员豁免</label>
          <div class="fmain"><a-switch v-model:checked="p.exempt_admin" /><span class="static">管理员请求不计入限流</span></div>
        </div>
        <div class="frow">
          <label>公共 IP RPM</label>
          <div class="fmain"><a-input-number v-model:value="p.public_ip_rpm" :min="0" :max="100000" class="fld-n" /><span class="static">0 = 不单独限制公共出口 IP</span></div>
        </div>
      </div>
      <div class="savebar">
        <a-button :loading="saving" @click="doReset"><UndoOutlined /> 恢复默认</a-button>
        <a-button type="primary" class="save" :loading="saving" @click="doSave">保存限流配置</a-button>
      </div>
    </a-spin>
  </div>
</template>

<style scoped>
@import './settings.css';
.card-title .anticon { margin-right: 6px; }
</style>
