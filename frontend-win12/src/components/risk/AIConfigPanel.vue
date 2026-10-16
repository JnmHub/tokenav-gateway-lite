<script setup>
import { computed, ref } from 'vue'
import { applyDeepSeekPreset, aiEndpoint, AI_SERVICE_PRESETS, AI_MODEL_OPTIONS } from '../../api/riskControl.js'
import { AI_PROTOCOL_LABELS } from './aiFields.js'
import W12Input from '../design/W12Input.vue'
import W12Select from '../design/W12Select.vue'
import W12FormRow from '../design/W12FormRow.vue'
import W12Status from '../design/W12Status.vue'
const props = defineProps({ cfg: { type: Object, required: true }, saved: Object, apiKey: String, clearAPIKey: Boolean, saving: Boolean })
const emit = defineEmits(['update:apiKey', 'update:clearAPIKey'])
const customService = ref(false), customModel = ref(false)
const adapters = [{ value: 'deepseek_chat', label: 'Chat Completions（对话接口）' }, { value: 'deepseek_responses', label: 'Responses（响应接口）' }]
const service = computed(() => customService.value || props.cfg.ai.base_url !== 'https://api.deepseek.com' ? 'custom' : props.cfg.ai.adapter)
const modelChoice = computed(() => customModel.value || !AI_MODEL_OPTIONS.some((item) => item.value !== 'custom' && item.value === props.cfg.ai.model) ? 'custom' : props.cfg.ai.model)
const endpoint = computed(() => aiEndpoint(props.cfg.ai))
function selectService(value) {
  customService.value = value === 'custom'
  if (!customService.value) applyDeepSeekPreset(props.cfg.ai, value)
}
function selectModel(value) {
  customModel.value = value === 'custom'
  if (!customModel.value) props.cfg.ai.model = value
}
</script>

<template>
  <div class="sw w12-design">
    <div class="card">
      <div class="card-title">专用 AI 审查服务</div>
      <p class="card-sub">选择服务与模型，填写专用密钥后保存即可测试。每次审查仅调用一次模型，沿用原业务请求的取消与超时生命周期。</p>
      <W12FormRow label="审查服务"><W12Select :value="service" class="fld" :options="AI_SERVICE_PRESETS" :disabled="saving" @change="selectService" /></W12FormRow>
      <template v-if="service === 'custom'">
        <W12FormRow label="适配协议"><W12Select v-model:value="cfg.ai.adapter" class="fld" :options="adapters" :disabled="saving" /></W12FormRow>
        <W12FormRow label="服务地址" hint="支持 HTTP（普通连接）或 HTTPS（加密连接），允许内网 IP、域名及端口。地址可含基础路径。"><W12Input v-model:value="cfg.ai.base_url" class="fld" placeholder="http://192.168.1.10:8000/v1" :disabled="saving" /></W12FormRow>
      </template>
      <W12FormRow label="请求模型"><W12Select :value="modelChoice" class="fld" :options="AI_MODEL_OPTIONS" :disabled="saving" @change="selectModel" /></W12FormRow>
      <W12FormRow v-if="modelChoice === 'custom'" label="自定义请求模型名" hint="原样发送，不自动转换产品名称。"><W12Input v-model:value="cfg.ai.model" class="fld" :disabled="saving" /></W12FormRow>
      <W12FormRow label="专用请求路径"><span class="static mono">{{ endpoint || '请填写有效服务地址' }}</span></W12FormRow>
      <W12FormRow label="已保存密钥"><span class="static">{{ saved?.ai?.api_key_configured ? `已配置 · ${saved.ai.api_key_masked || '已隐藏'}（有效性尚需测试）` : '未配置' }}</span></W12FormRow>
      <W12FormRow label="专用密钥" hint="留空保留已保存密钥；旧审核密钥不会自动复用。">
        <a-input-password :value="apiKey" class="fld" :disabled="clearAPIKey || saving" autocomplete="new-password" placeholder="仅输入新密钥" @update:value="emit('update:apiKey', $event)" />
      </W12FormRow>
      <W12FormRow label="图片识别" hint="开启后内联图片与文本整体送审，需模型支持视觉；关闭则记录图片未覆盖。外链图片不下载，也不标记为已审查。"><a-switch v-model:checked="cfg.ai.image_review_enabled" :disabled="saving" /></W12FormRow>
      <W12FormRow label="外部数据授权" hint="生产审查会向上述服务发送提取的完整文本及已启用的内联图片。请确认用途后开启；合成测试只发送内置案例。"><a-switch v-model:checked="cfg.ai.external_data_consent" :disabled="saving" /></W12FormRow>
    </div>
    <a-collapse ghost>
      <a-collapse-panel key="policy" header="审查策略与违规处置">
        <div class="sw">
          <div class="card">
            <W12FormRow label="模型修订" hint="仅配置备注，修改文字不会成为测试证据。"><W12Input v-model:value="cfg.ai.model_revision" class="fld" :disabled="saving" /></W12FormRow>
            <W12FormRow label="审查策略版本"><W12Input v-model:value="cfg.ai.policy_version" class="fld" :disabled="saving" /></W12FormRow>
            <W12FormRow label="审查策略"><a-textarea v-model:value="cfg.ai.policy" :rows="4" :maxlength="16000" class="fld-full" :disabled="saving" /></W12FormRow>
            <W12FormRow label="AI 违规处罚" hint="仅同步拦截的真实违规参与已启用处置。观察有效判定不拦截、不处罚，审查失败仍拒绝本次请求且不计违规。"><a-switch v-model:checked="cfg.ai.penalties_enabled" :disabled="saving" /></W12FormRow>
            <W12FormRow label="删除已保存密钥"><a-checkbox :checked="clearAPIKey" :disabled="saving" @update:checked="emit('update:clearAPIKey', $event)">保存时删除专用密钥</a-checkbox></W12FormRow>
          </div>
          <div class="card">
            <div class="card-title">网关内容提取范围</div>
            <p class="card-sub">以下是网关已实现的提取范围，供应商能力需另行测试。不支持的媒体和未知扩展字段只记录未覆盖；其余已知内容继续审查。没有可审查输入时跳过模型调用并放行，不标记为已审查。</p>
            <W12FormRow v-for="cap in saved?.ai?.capabilities || []" :key="cap.protocol" :label="AI_PROTOCOL_LABELS[cap.protocol] || cap.protocol">
              <W12Status :tone="cap.supported ? '' : 'warn'">{{ cap.supported ? '实现文本支持' : '不支持' }}</W12Status><span class="static">{{ cap.scope }}{{ cap.reason ? ` · ${cap.reason}` : '' }}</span>
            </W12FormRow>
          </div>
        </div>
      </a-collapse-panel>
    </a-collapse>
  </div>
</template>
<style scoped>
@import '../settings/settings.css';
</style>
