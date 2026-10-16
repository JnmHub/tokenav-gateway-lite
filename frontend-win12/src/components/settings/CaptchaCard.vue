<script setup>
/* ============================================================
 * 人机验证服务卡（官方 v0.2.5：腾讯天御 + 阿里云验证码 2.0）
 * 键名对照 internal/service/domain_constants.go:264-277：
 * - 腾讯：tencent_captcha_enabled / app_id / app_secret_key /
 *   cloud_secret_id / cloud_secret_key / region（cn|intl）
 * - 阿里：aliyun_captcha_enabled / access_key_id / access_key_secret /
 *   scene_id / prefix / region（cn|sgp）
 * secret 类字段照 Turnstile 卡模式：GET 响应无该键则留空不提交；
 * 若响应带 *_configured 后缀键则显示已配置状态。三家互斥启用。
 * ============================================================ */
import { computed } from 'vue'
const props = defineProps({
  s: { type: Object, required: true },
  save: { type: Function, required: true },
})

/* 三家互斥（腾讯天御 / 阿里云验证码 / Cloudflare Turnstile）：任一家启用时
 * 其余两家禁开。Turnstile 开关在「Cloudflare Turnstile」卡，但三家状态同在
 * 共享配置对象 s 上，这里按 s.turnstile_enabled 联动，避免同时保存两家生效 */
const turnstileOn = computed(() => !!props.s.turnstile_enabled)
const tencentOn = computed(() => !!props.s.tencent_captcha_enabled)
const aliyunOn = computed(() => !!props.s.aliyun_captcha_enabled)
const tencentDisabled = computed(() => aliyunOn.value || turnstileOn.value)
const aliyunDisabled = computed(() => tencentOn.value || turnstileOn.value)

/* secret 输入为空时不进 patch（undefined 键不提交），后端保留已存值 */
const doSave = () => props.save('captcha', {
  tencent_captcha_enabled: !!props.s.tencent_captcha_enabled,
  tencent_captcha_app_id: String(props.s.tencent_captcha_app_id || '').trim(),
  tencent_captcha_app_secret_key: String(props.s.tencent_captcha_app_secret_key || '').trim() || undefined,
  tencent_captcha_cloud_secret_id: String(props.s.tencent_captcha_cloud_secret_id || '').trim() || undefined,
  tencent_captcha_cloud_secret_key: String(props.s.tencent_captcha_cloud_secret_key || '').trim() || undefined,
  tencent_captcha_region: props.s.tencent_captcha_region === 'intl' ? 'intl' : 'cn',
  aliyun_captcha_enabled: !!props.s.aliyun_captcha_enabled,
  aliyun_captcha_access_key_id: String(props.s.aliyun_captcha_access_key_id || '').trim(),
  aliyun_captcha_access_key_secret: String(props.s.aliyun_captcha_access_key_secret || '').trim() || undefined,
  aliyun_captcha_scene_id: String(props.s.aliyun_captcha_scene_id || '').trim(),
  aliyun_captcha_prefix: String(props.s.aliyun_captcha_prefix || '').trim(),
  aliyun_captcha_region: props.s.aliyun_captcha_region === 'sgp' ? 'sgp' : 'cn',
})

const tencentRegionOpts = [{ value: 'cn', label: '中国大陆（cn）' }, { value: 'intl', label: '国际站（intl）' }]
const aliyunRegionOpts = [{ value: 'cn', label: '中国大陆（cn）' }, { value: 'sgp', label: '新加坡（sgp）' }]
</script>

<template>
  <div class="card">
    <div class="card-title">人机验证服务（腾讯天御 / 阿里云验证码）</div>
    <div class="card-sub">与 Cloudflare Turnstile 三家互斥，同一时间仅可启用一家。</div>
    <div class="form">
      <div class="frow">
        <label>启用腾讯天御</label>
        <div class="fmain">
          <a-switch v-model:checked="s.tencent_captcha_enabled" :disabled="tencentDisabled" />
          <span v-if="tencentDisabled" class="static">同一时间仅可启用一家</span>
        </div>
      </div>
      <div class="frow">
        <label>CaptchaAppID</label>
        <div class="fmain"><a-input v-model:value="s.tencent_captcha_app_id" class="fld" placeholder="腾讯云验证码 AppID" /></div>
      </div>
      <div class="frow">
        <label>AppSecretKey</label>
        <div class="fmain"><a-input-password v-model:value="s.tencent_captcha_app_secret_key" class="fld" placeholder="留空 = 保留已存值" /></div>
      </div>
      <div class="frow">
        <label>Cloud SecretId</label>
        <div class="fmain"><a-input v-model:value="s.tencent_captcha_cloud_secret_id" class="fld" placeholder="云 API 密钥 SecretId" /></div>
      </div>
      <div class="frow">
        <label>Cloud SecretKey</label>
        <div class="fmain"><a-input-password v-model:value="s.tencent_captcha_cloud_secret_key" class="fld" placeholder="留空 = 保留已存值" /></div>
      </div>
      <div class="frow">
        <label>站点地域</label>
        <div class="fmain">
          <a-select v-model:value="s.tencent_captcha_region" class="fld-n" :options="tencentRegionOpts" />
        </div>
      </div>
    </div>

    <div class="divider" />

    <div class="form">
      <div class="frow">
        <label>启用阿里云验证码</label>
        <div class="fmain">
          <a-switch v-model:checked="s.aliyun_captcha_enabled" :disabled="aliyunDisabled" />
          <span v-if="aliyunDisabled" class="static">同一时间仅可启用一家</span>
        </div>
      </div>
      <div class="frow">
        <label>AccessKey ID</label>
        <div class="fmain"><a-input v-model:value="s.aliyun_captcha_access_key_id" class="fld" /></div>
      </div>
      <div class="frow">
        <label>AccessKey Secret</label>
        <div class="fmain"><a-input-password v-model:value="s.aliyun_captcha_access_key_secret" class="fld" placeholder="留空 = 保留已存值" /></div>
      </div>
      <div class="frow">
        <label>场景 ID（SceneId）</label>
        <div class="fmain"><a-input v-model:value="s.aliyun_captcha_scene_id" class="fld" placeholder="所有认证流程共用" /></div>
      </div>
      <div class="frow">
        <label>身份标（Prefix）</label>
        <div class="fmain"><a-input v-model:value="s.aliyun_captcha_prefix" class="fld" placeholder="前端 SDK 初始化用" /></div>
      </div>
      <div class="frow">
        <label>地域</label>
        <div class="fmain">
          <a-select v-model:value="s.aliyun_captcha_region" class="fld-n" :options="aliyunRegionOpts" />
        </div>
      </div>
    </div>

    <div class="savebar">
      <a-button type="primary" class="save" @click="doSave">保存人机验证配置</a-button>
    </div>
  </div>
</template>

<style scoped>
@import './settings.css';
.divider { border-top: 1px solid var(--hr); margin: 12px -16px; opacity: 0.6; }
</style>
