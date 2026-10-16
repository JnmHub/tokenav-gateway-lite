<script setup>
import { computed } from 'vue'
import W12Select from '../design/W12Select.vue'
import W12Input from '../design/W12Input.vue'
import W12FormRow from '../design/W12FormRow.vue'
import W12Status from '../design/W12Status.vue'
import { riskModeLabel } from '../../api/riskControl.js'
const props = defineProps({ cfg: { type: Object, required: true }, saved: Object, groups: { type: Array, default: () => [] }, saving: Boolean })
const aiOnly = computed(() => props.cfg.keyword_blocking_mode === 'ai_only')
const engines = [{ value: 'keyword_only', label: '本地关键词（不出网）' }, { value: 'ai_only', label: '纯 AI（外部模型审查）' }]
const modes = computed(() => aiOnly.value
  ? [{ value: 'off', label: '关闭' }, { value: 'shadow', label: '观察（有效判定不拦截，失败拒绝）' }, { value: 'pre_block', label: '同步拦截' }]
  : [{ value: 'off', label: '关闭' }, { value: 'observe', label: '观察（只记录）' }, { value: 'pre_block', label: '前置拦截' }])
function changeEngine(engine) {
  props.cfg.keyword_blocking_mode = engine
  // 切 AI 的安全起点为观察和关闭处罚，不能继承旧封号/扣费草稿并意外开始处罚。
  props.cfg.mode = engine === 'ai_only' ? 'shadow' : 'pre_block'
  if (engine === 'ai_only') {
    for (const key of ['auto_ban_enabled', 'permanent_ban_enabled', 'deduct_balance_on_block_enabled', 'email_on_hit']) props.cfg[key] = false
    if (props.cfg.ai) props.cfg.ai.penalties_enabled = false
  }
}
const groupOptions = computed(() => props.groups.map((g) => ({ value: g.id, label: `${g.name}（${g.platform}）` })))
const languages = [['zh', '中文'], ['en', '英语'], ['ja', '日语'], ['ko', '韩语'], ['es', '西班牙语'], ['fr', '法语'], ['de', '德语'], ['pt', '葡萄牙语'], ['it', '意大利语'], ['ru', '俄语'], ['ar', '阿拉伯语'], ['hi', '印地语'], ['th', '泰语'], ['tr', '土耳其语'], ['vi', '越南语'], ['id', '印尼语'], ['he', '希伯来语'], ['el', '希腊语']].map(([value, label]) => ({ value, label }))
const scans = [['scan_latest_user_input', '扫描最新用户输入'], ['scan_top_level_instructions', '扫描顶层系统指令'], ['scan_new_skill_content', '扫描新增技能内容'], ['scan_new_tool_output', '扫描新增工具输出']]
</script>
<template>
  <div class="sw w12-design">
    <div class="card">
      <div class="card-title">内容审查</div>
      <p class="card-sub">已保存：{{ saved?.keyword_blocking_mode === 'ai_only' ? '纯 AI' : '本地关键词' }} · {{ riskModeLabel(saved?.mode) }} · {{ saved?.enabled ? '已开启' : '已关闭' }}。当前编辑尚未保存时不会改变运行配置。</p>
      <W12FormRow label="内容审查总开关"><a-switch v-model:checked="cfg.enabled" :disabled="saving" /></W12FormRow>
      <W12FormRow label="审查引擎"><W12Select :value="cfg.keyword_blocking_mode" :options="engines" class="fld" :disabled="saving" @change="changeEngine" /></W12FormRow>
      <W12FormRow label="运行模式"><W12Select v-model:value="cfg.mode" :options="modes" class="fld" :disabled="saving" /></W12FormRow>
      <a-alert v-if="aiOnly" show-icon type="info" message="纯 AI 跳过关键词、规则包、语种拦截及历史指纹捷径，固定全量覆盖。观察时有效判定不拦截、不处罚；观察和同步模式审查失败都拒绝请求，错误不计违规。" />
      <W12FormRow label="作用于全部分组"><a-switch v-model:checked="cfg.all_groups" /></W12FormRow>
      <W12FormRow v-if="!cfg.all_groups" label="指定分组"><W12Select v-model:value="cfg.group_ids" class="fld-full" mode="multiple" :options="groupOptions" placeholder="点击选择分组" /></W12FormRow>
      <W12FormRow label="模型范围"><W12Select v-model:value="cfg.model_filter.type" class="fld" :options="[{ value: 'all', label: '全部模型' }, { value: 'include', label: '仅指定模型' }, { value: 'exclude', label: '排除指定模型' }]" /></W12FormRow>
      <W12FormRow v-if="cfg.model_filter.type !== 'all'" label="模型清单" hint="指定或排除时至少填写一个模型，后台验证实际范围。"><W12Select v-model:value="cfg.model_filter.models" mode="tags" class="fld-full" :token-separators="[',', ' ']" /></W12FormRow>
      <template v-if="!aiOnly">
        <W12FormRow v-for="[key, label] in scans" :key="key" :label="label"><a-switch v-model:checked="cfg[key]" /></W12FormRow>
        <W12FormRow label="允许语种" hint="空列表为不按语种限制。纯 AI 使用完整内容审查，忽略这些本地扫描开关。"><W12Select v-model:value="cfg.allowed_languages" mode="multiple" :options="languages" class="fld-full" /></W12FormRow>
      </template>
    </div>
    <div class="card">
      <div class="card-title">违规处置</div>
      <p v-if="aiOnly" class="card-sub">仅在 AI 同步拦截、真实违规并开启 AI 处罚时生效。观察与无法判定不处罚。临时封禁不属于当前 AI 处罚能力，请采用后台支持的永久封禁策略。</p>
      <W12FormRow label="自动封禁"><a-switch v-model:checked="cfg.auto_ban_enabled" /></W12FormRow>
      <W12FormRow label="封禁阈值（次数）"><a-input-number v-model:value="cfg.ban_threshold" :min="1" :precision="0" class="fld-n" /></W12FormRow>
      <W12FormRow label="计数窗口（小时）"><a-input-number v-model:value="cfg.violation_window_hours" :min="1" :precision="0" class="fld-n" /></W12FormRow>
      <W12FormRow label="永久封禁"><a-switch v-model:checked="cfg.permanent_ban_enabled" /></W12FormRow>
      <W12FormRow label="永久封禁阈值（次数）"><a-input-number v-model:value="cfg.permanent_ban_threshold" :min="1" :precision="0" class="fld-n" /></W12FormRow>
      <W12FormRow label="命中邮件通知"><a-switch v-model:checked="cfg.email_on_hit" /></W12FormRow>
      <W12FormRow label="拦截时扣费"><a-switch v-model:checked="cfg.deduct_balance_on_block_enabled" /></W12FormRow>
      <W12FormRow label="拦截扣费金额"><a-input-number v-model:value="cfg.deduct_balance_on_block_amount" :min="0" :step="0.01" class="fld-n" /></W12FormRow>
      <W12FormRow label="上游安全策略排除封号"><a-switch v-model:checked="cfg.cyber_policy_exclude_from_ban_count" /></W12FormRow>
    </div>
    <div class="card">
      <div class="card-title">日志保留</div>
      <W12FormRow label="记录非命中"><a-switch v-model:checked="cfg.record_non_hits" /></W12FormRow>
      <W12FormRow label="命中日志保留（天）"><a-input-number v-model:value="cfg.hit_retention_days" :min="1" :precision="0" class="fld-n" /></W12FormRow>
      <W12FormRow label="非命中保留（天）"><a-input-number v-model:value="cfg.non_hit_retention_days" :min="1" :precision="0" class="fld-n" /></W12FormRow>
      <W12FormRow label="历史指纹捷径"><W12Status tone="warn">后台固定关闭</W12Status><span class="static">指纹仅用于记录和管理，不能切换为前置秒拦。</span></W12FormRow>
    </div>
    <a-collapse v-if="!aiOnly" ghost>
      <a-collapse-panel key="legacy" header="旧审核服务配置（保留管理）">
        <div class="card">
          <p class="card-sub">原有接口参数和密钥测试保留。当前本地关键词或纯 AI 引擎采用各自执行链；这些旧参数不代表专用 AI 配置。</p>
          <W12FormRow label="旧审核 API 地址"><W12Input v-model:value="cfg.base_url" class="fld" /></W12FormRow>
          <W12FormRow label="旧审核模型"><W12Input v-model:value="cfg.model" class="fld" /></W12FormRow>
          <W12FormRow label="旧密钥"><span class="static">{{ cfg.api_key_configured ? `已配置 ${cfg.api_key_count} 把 · ${cfg.api_key_masked || '已隐藏'}` : '未配置' }}</span></W12FormRow>
          <W12FormRow label="采样率 %"><a-input-number v-model:value="cfg.sample_rate" :disabled="aiOnly" :min="0" :max="100" class="fld-n" /><span v-if="aiOnly" class="static">纯 AI 后台固定 100%</span></W12FormRow>
          <W12FormRow label="旧审核超时（毫秒）"><a-input-number v-model:value="cfg.timeout_ms" :min="100" class="fld-n" /></W12FormRow>
          <W12FormRow label="工作线程"><a-input-number v-model:value="cfg.worker_count" :min="1" class="fld-n" /></W12FormRow>
          <W12FormRow label="旧审核队列"><a-input-number v-model:value="cfg.queue_size" :min="1" class="fld-n" /></W12FormRow>
          <W12FormRow label="旧审核重试"><a-input-number v-model:value="cfg.retry_count" :min="0" class="fld-n" /></W12FormRow>
          <W12FormRow v-for="(_, category) in cfg.thresholds" :key="category" :label="`阈值 ${category}`"><a-input-number v-model:value="cfg.thresholds[category]" :min="0" :max="1" :step="0.01" class="fld-n" /></W12FormRow>
        </div>
      </a-collapse-panel>
    </a-collapse>
  </div>
</template>
<style scoped>
@import '../settings/settings.css';
</style>
