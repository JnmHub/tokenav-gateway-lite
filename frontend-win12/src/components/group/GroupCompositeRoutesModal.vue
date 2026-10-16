<script setup>
import W12Dialog from '../design/W12Dialog.vue'
import W12Table from '../design/W12Table.vue'
/* 聚合路由弹窗（二期 B，composite 平台分组专用）
 * 契约来源（唯一权威）：旧 frontend/src/components/admin/group/CompositeRoutesModal.vue
 * 接口：
 * - GET/POST /admin/groups/:id/composite-routes
 * - PUT/DELETE /admin/groups/:id/composite-routes/:routeId
 * - PUT  /admin/groups/:id/composite-routes/batch-status { route_ids, enabled }
 * - POST /admin/groups/:id/composite-routes/preview { model, endpoint, candidate, editing_route_id }
 * - GET  /admin/groups/:id/models-list-candidates?platform=composite（常用模型候选）
 * 行为（与旧实现一致）：
 * - 保存前必须服务端预演：candidate_wins=false 时报错并阻止保存
 * - prefix 匹配预演时模型名追加 "preview" 后缀（旧实现如此）
 * - 平台推断正则与旧 detectPlatform 完全一致（claude→anthropic、gemini→gemini、
 *   grok→grok、kimi/moonshot→kimi、glm/zhipu→zhipu、deepseek→deepseek、gpt系→openai）
 */
import { reactive, ref, computed, watch } from 'vue'
import { message } from 'ant-design-vue'
import { useDiscardGuard } from '../../core/windowContext'
import { useGroupDialogRequests } from './useGroupDialogRequests.js'
import { isCancellation } from '../../api/session.js'
import { ReloadOutlined } from '@ant-design/icons-vue'
import {
  listCompositeRoutes, createCompositeRoute, updateCompositeRoute, deleteCompositeRoute,
  batchSetCompositeRoutesEnabled, previewCompositeRoute, getModelsListCandidates,
} from './groupApi'

const props = defineProps({
  open: { type: Boolean, default: false },
  group: { type: Object, default: null },
})
const emit = defineEmits(['update:open', 'changed'])
const requests = useGroupDialogRequests(props)

const routes = ref([])
const candidates = ref([])
const selectedRouteIds = ref([])
const loading = ref(false)
const busy = ref(false)
const editingId = ref(null)
const deleteConfirmId = ref(null)
const preview = ref(null)

// 表单（字段与后端 CompositeRouteRequest 完全对应）
const blankForm = () => ({ public_model: '', match_type: 'exact', target_platform: 'openai', upstream_model: '', endpoint: 'any', priority: 100, enabled: true, notes: '' })
const form = reactive(blankForm()), baseline = ref(JSON.stringify(blankForm()))
const beforeClose = useDiscardGuard(() => props.open && JSON.stringify(form) !== baseline.value, () => busy.value)
const visibleCandidates = computed(() => candidates.value.slice(0, 18))
const allSelected = computed({
  get: () => routes.value.length > 0 && selectedRouteIds.value.length === routes.value.length,
  set: (checked) => { selectedRouteIds.value = checked ? routes.value.map((r) => r.id) : [] },
})

// 选项与旧实现一致
const platformOptions = [
  { value: 'anthropic', label: 'Anthropic' }, { value: 'openai', label: 'OpenAI' },
  { value: 'gemini', label: 'Gemini' }, { value: 'antigravity', label: 'Antigravity' },
  { value: 'grok', label: 'Grok' }, { value: 'kimi', label: 'Kimi' },
  { value: 'zhipu', label: '智谱 GLM' }, { value: 'deepseek', label: 'DeepSeek' },
]
const matchOptions = [{ value: 'exact', label: '精确模型名' }, { value: 'prefix', label: '模型名前缀' }]
const endpointOptions = [
  { value: 'any', label: '全部接口' }, { value: 'messages', label: 'Messages' },
  { value: 'count_tokens', label: 'Count Tokens' }, { value: 'responses', label: 'Responses / Compact' },
  { value: 'chat_completions', label: 'Chat Completions' }, { value: 'embeddings', label: 'Embeddings' },
  { value: 'images', label: '图片生成' }, { value: 'gemini', label: 'Gemini 原生' },
]
const priorityPresets = [10, 50, 100, 200]

const cols = [
  { title: '用户模型', key: 'public_model', ellipsis: true },
  { title: '真实去向', key: 'target', width: 170, ellipsis: true },
  { title: '范围', key: 'endpoint', width: 110 },
  { title: '', key: 'act', width: 130 },
]

const platformLabel = (p) => platformOptions.find((i) => i.value === p)?.label || p || '未知'
const endpointLabel = (e) => endpointOptions.find((i) => i.value === e)?.label || e
const priceSourceLabel = (s) => ({ channel: '渠道覆盖', litellm: '模型目录', fallback: '内置价格' }[s || ''] || s || '未知')

// 平台推断（正则与旧 detectPlatform 一致）
const detectPlatform = (model) => {
  const value = String(model || '').trim().toLowerCase().replace(/^models\//, '')
  if (/^(claude-|anthropic\/|claude\/)/.test(value)) return 'anthropic'
  if (/^(gemini-|learnlm-|google\/|google-ai-studio\/|gemini\/)/.test(value)) return 'gemini'
  if (/^(grok($|-)|xai\/|x-ai\/)/.test(value)) return 'grok'
  if (/^(kimi-|moonshot-|kimi\/|moonshot\/)/.test(value)) return 'kimi'
  if (/^(glm-|zhipu\/|bigmodel\/)/.test(value)) return 'zhipu'
  if (/^(deepseek-|deepseek\/)/.test(value)) return 'deepseek'
  if (/^(gpt-|chatgpt-|codex-|text-embedding-|dall-e-|gpt-image-|o[1345]($|-)|openai\/)/.test(value)) return 'openai'
  return null
}
const inferFormPlatform = () => { const d = detectPlatform(form.public_model); if (d) form.target_platform = d }
const pickModel = (m) => { if (busy.value) return; form.public_model = m; form.upstream_model = m; inferFormPlatform(); preview.value = null }
const resetForm = () => { Object.assign(form, blankForm()); baseline.value = JSON.stringify(form); editingId.value = null; preview.value = null; deleteConfirmId.value = null }
const editRoute = (r) => {
  if (busy.value) return
  editingId.value = r.id
  Object.assign(form, { public_model: r.public_model, match_type: r.match_type, target_platform: r.target_platform, upstream_model: r.upstream_model, endpoint: r.endpoint, priority: r.priority, enabled: r.enabled, notes: r.notes || '' })
  baseline.value = JSON.stringify(form)
  preview.value = null
}

const load = async () => {
  if (!props.open || !props.group) return
  const turn = requests.begin('load')
  loading.value = true
  try {
    const [list, cands] = await Promise.all([
      listCompositeRoutes(turn.groupId, turn.signal),
      getModelsListCandidates(turn.groupId, 'composite', turn.signal).catch(() => ({ models: [] })),
    ])
    if (!turn.isCurrent()) return
    routes.value = list || []
    candidates.value = cands?.models || []
    // 只保留仍存在的勾选
    selectedRouteIds.value = selectedRouteIds.value.filter((id) => routes.value.some((r) => r.id === id))
  } catch (e) {
    if (turn.isCurrent() && !isCancellation(e)) message.error(`读取聚合路由失败：${e.message}`)
  } finally { if (turn.isCurrent()) loading.value = false }
}

// 预演并保存：candidate_wins=false 阻止保存（旧 previewAndSave）
const previewAndSave = async () => {
  if (!props.open || !props.group || busy.value) return
  if (!Number.isInteger(form.priority) || form.priority < -100000 || form.priority > 100000) {
    message.error('优先级必须是 -100000 到 100000 之间的整数'); return
  }
  const turn = requests.begin('save'), routeId = editingId.value
  busy.value = true
  preview.value = null
  const payload = { ...form, upstream_model: form.upstream_model || form.public_model }
  try {
    const result = await previewCompositeRoute(turn.groupId, {
      model: form.match_type === 'prefix' ? `${form.public_model}preview` : form.public_model,
      endpoint: form.endpoint,
      candidate: payload,
      editing_route_id: routeId || undefined,
    }, turn.signal)
    if (!turn.isCurrent()) return
    preview.value = result
    if (!result.candidate_wins) {
      message.error('该规则会被已有规则覆盖，请调整接口范围、匹配方式或优先级')
      return
    }
    if (routeId) await updateCompositeRoute(turn.groupId, routeId, payload, turn.signal)
    else await createCompositeRoute(turn.groupId, payload, turn.signal)
    if (!turn.isCurrent()) return
    message.success(routeId ? '聚合路由已更新' : '聚合路由已创建')
    resetForm()
    await load()
    emit('changed')
  } catch (e) {
    if (turn.isCurrent() && !isCancellation(e)) message.error(`保存聚合路由失败：${e.message}`)
  } finally { if (turn.isCurrent()) busy.value = false }
}

const removeRoute = async (routeId) => {
  if (!props.open || !props.group || busy.value) return
  const turn = requests.begin('delete')
  busy.value = true
  try {
    await deleteCompositeRoute(turn.groupId, routeId, turn.signal)
    if (!turn.isCurrent()) return
    resetForm(); await load(); emit('changed')
    message.success('聚合路由已删除')
  } catch (e) {
    if (turn.isCurrent() && !isCancellation(e)) message.error(`删除聚合路由失败：${e.message}`)
  } finally { if (turn.isCurrent()) busy.value = false }
}

const setSelectedEnabled = async (enabled) => {
  if (!props.open || !props.group || !selectedRouteIds.value.length || busy.value) return
  const turn = requests.begin('batch')
  busy.value = true
  try {
    await batchSetCompositeRoutesEnabled(turn.groupId, [...selectedRouteIds.value], enabled, turn.signal)
    if (!turn.isCurrent()) return
    await load(); emit('changed')
    message.success(enabled ? '所选路由已启用' : '所选路由已停用')
  } catch (e) {
    if (turn.isCurrent() && !isCancellation(e)) message.error(`批量修改失败：${e.message}`)
  } finally { if (turn.isCurrent()) busy.value = false }
}

watch(() => [props.open, props.group?.id], ([val]) => { resetForm(); selectedRouteIds.value = []; routes.value = []; candidates.value = []; loading.value = false; busy.value = false; if (val) load() }, { immediate: true, flush: 'sync' })
</script>

<template>
  <W12Dialog
    :open="open" :close-key="group?.id" :title="`聚合路由 · ${group?.name || ''}`" width="1020px" wrap-class-name="group-comp-routes-modal"
    :before-close="beforeClose" :busy="busy" :footer="null" @cancel="emit('update:open', false)"
  >
    <fieldset class="layout" :disabled="busy">
      <!-- 左：已保存路由 -->
      <section class="left">
        <div class="left-head">
          <div>
            <div class="sec-t">已保存路由</div>
            <div class="hint">勾选后可一键启停；停用规则不会进入请求快照。</div>
          </div>
          <div class="left-btns">
            <a-button size="small" :disabled="!selectedRouteIds.length || busy" @click="setSelectedEnabled(true)">启用</a-button>
            <a-button size="small" :disabled="!selectedRouteIds.length || busy" @click="setSelectedEnabled(false)">停用</a-button>
            <a-button size="small" :loading="loading" @click="load"><ReloadOutlined /></a-button>
          </div>
        </div>
        <W12Table
          :columns="cols" :data-source="routes" :loading="loading" row-key="id" size="small"
          :pagination="false" :locale="{ emptyText: '还没有显式规则，已知模型会使用内置平台识别' }"
          :scroll="{ y: 380 }" :row-class-name="(r) => (!r.enabled ? 'row-off' : '')"
        >
          <template #headerCell="{ column }">
            <template v-if="column.key === 'public_model'">
              <a-checkbox
                :checked="allSelected"
                :indeterminate="selectedRouteIds.length > 0 && selectedRouteIds.length < routes.length"
                @change="(e) => (allSelected = e.target.checked)"
              />
            </template>
          </template>
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'public_model'">
              <div class="cell-row">
                <a-checkbox
                  :checked="selectedRouteIds.includes(record.id)"
                  @change="(e) => { const s = new Set(selectedRouteIds); e.target.checked ? s.add(record.id) : s.delete(record.id); selectedRouteIds = [...s] }"
                />
                <div class="cell-main">
                  <div class="pm">{{ record.public_model }}</div>
                  <span class="pill">{{ record.match_type === 'exact' ? '精确' : '前缀' }} · P{{ record.priority }}</span>
                </div>
              </div>
            </template>
            <template v-else-if="column.key === 'target'">
              <div class="tp">{{ platformLabel(record.target_platform) }}</div>
              <div class="um">{{ record.upstream_model || record.public_model }}</div>
            </template>
            <template v-else-if="column.key === 'endpoint'">
              <span class="hint">{{ endpointLabel(record.endpoint) }}</span>
            </template>
            <template v-else-if="column.key === 'act'">
              <div class="acts">
                <a-button size="small" type="link" @click="editRoute(record)">编辑</a-button>
                <a-button size="small" type="link" danger @click="deleteConfirmId === record.id ? removeRoute(record.id) : (deleteConfirmId = record.id)">
                  {{ deleteConfirmId === record.id ? '确认删除' : '删除' }}
                </a-button>
              </div>
            </template>
          </template>
        </W12Table>
      </section>

      <!-- 右：新增/修改规则 -->
      <section class="right">
        <div class="right-head">
          <div class="sec-t">{{ editingId ? '修改规则' : '新增规则' }}</div>
          <a-button v-if="editingId" size="small" type="link" @click="resetForm">取消修改</a-button>
        </div>

        <div class="fitem">
          <div class="flabel">常用模型（点击填入）</div>
          <div class="chips">
            <button v-for="m in visibleCandidates" :key="m" type="button" class="chip" @click="pickModel(m)">{{ m }}</button>
            <span v-if="!visibleCandidates.length" class="hint">暂无候选</span>
          </div>
        </div>

        <div class="fitem">
          <div class="flabel">用户请求的模型</div>
          <a-input v-model:value="form.public_model" placeholder="点选上方模型，或输入自定义别名" @change="inferFormPlatform" />
        </div>

        <div class="f2">
          <div class="fitem">
            <div class="flabel">匹配方式</div>
            <a-select v-model:value="form.match_type" :options="matchOptions" />
          </div>
          <div class="fitem">
            <div class="flabel">接口范围</div>
            <a-select v-model:value="form.endpoint" :options="endpointOptions" />
          </div>
        </div>
        <div class="f2">
          <div class="fitem">
            <div class="flabel">目标平台</div>
            <a-select v-model:value="form.target_platform" :options="platformOptions" />
          </div>
          <div class="fitem">
            <div class="flabel">优先级（越小越优先）</div>
            <div class="prios">
              <button
                v-for="v in priorityPresets" :key="v" type="button" class="prio"
                :class="{ on: form.priority === v }" @click="form.priority = v"
              >{{ v }}</button>
            </div>
            <a-input-number v-model:value="form.priority" :min="-100000" :max="100000" :step="1" :precision="0" aria-label="自定义路由优先级" style="width: 100%" />
          </div>
        </div>

        <div class="fitem">
          <div class="flabel">上游真实模型</div>
          <a-input v-model:value="form.upstream_model" placeholder="默认与用户模型相同" />
        </div>

        <div class="fitem">
          <div class="flabel">备注</div>
          <a-textarea v-model:value="form.notes" :rows="2" placeholder="可选，记录此规则的用途" />
        </div>

        <div class="frow">
          <span>保存后立即启用</span>
          <a-switch v-model:checked="form.enabled" size="small" />
        </div>

        <!-- 预演结果 -->
        <div v-if="preview" class="preview" :class="preview.candidate_wins ? 'pass' : 'warn'">
          <div class="pv-t">{{ preview.candidate_wins ? '预览通过' : '规则会被其他规则覆盖' }}</div>
          <div class="pv-d">{{ preview.public_model }} → {{ platformLabel(preview.target_platform) }} → {{ preview.upstream_model }}</div>
          <div class="pv-s">价格来源：{{ preview.price_available ? priceSourceLabel(preview.price_source) : '未找到价格（启用后请求会失败关闭）' }}</div>
          <div v-if="!preview.candidate_wins && preview.route" class="pv-s">实际胜出：规则 #{{ preview.route.id }}（{{ preview.route.public_model }}）</div>
        </div>

        <a-button type="primary" block :loading="busy" :disabled="!form.public_model || !form.target_platform" @click="previewAndSave">
          {{ busy ? '正在验证…' : editingId ? '预览并保存修改' : '预览并创建' }}
        </a-button>
      </section>
    </fieldset>
  </W12Dialog>
</template>

<style scoped>
.layout { border: 0; margin: 0; padding: 0; min-width: 0; display: grid; grid-template-columns: minmax(0, 1.15fr) minmax(300px, 0.85fr); gap: 16px; }
@container w12-dialog (max-width: 850px) { .layout { grid-template-columns: minmax(0,1fr); } }
@container w12-dialog (max-width: 460px) { .f2 { grid-template-columns: minmax(0,1fr); } }
.sec-t { font-size: 13px; font-weight: 500; }
.hint { font-size: 12px; color: var(--text2); }
.left-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 8px; margin-bottom: 8px; }
.left-btns { display: flex; gap: 6px; }
.cell-row { display: flex; align-items: center; gap: 8px; }
.cell-main { min-width: 0; }
.pm { font-size: 12.5px; word-break: break-all; }
.pill { display: inline-block; margin-top: 2px; font-size: 11px; line-height: 18px; padding: 0 6px; border-radius: 4px; background: var(--card); color: var(--text2); }
.tp { font-size: 12.5px; }
.um { font-size: 11.5px; color: var(--text2); word-break: break-all; }
.acts { display: flex; justify-content: flex-end; }
:deep(.row-off) { opacity: 0.55; }
.right { display: flex; flex-direction: column; gap: 10px; padding: 10px; border: 1px solid var(--hr); border-radius: 8px; background: var(--card); }
.right-head { display: flex; align-items: center; justify-content: space-between; }
.fitem { display: grid; gap: 4px; }
.flabel { font-size: 12px; color: var(--text2); }
.f2 { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
.chips { display: flex; flex-wrap: wrap; gap: 4px; max-height: 72px; overflow-y: auto; }
.chip {
  border: 1px solid var(--hr); background: transparent; border-radius: 999px;
  padding: 2px 9px; font-size: 11.5px; cursor: pointer; color: var(--text2);
}
.chip:hover { border-color: var(--accent); color: var(--accent); }
.prios { display: grid; grid-template-columns: repeat(4, 1fr); gap: 4px; }
.prio { border: 1px solid var(--hr); background: transparent; border-radius: 6px; padding: 5px 0; font-size: 12px; cursor: pointer; color: var(--text2); }
.prio.on { border-color: var(--accent); color: var(--accent); background: color-mix(in srgb, var(--accent) 12%, transparent); }
.frow { display: flex; align-items: center; justify-content: space-between; font-size: 12.5px; padding: 4px 0; }
.preview { border-radius: 8px; padding: 8px 10px; font-size: 12px; }
.preview.pass { background: color-mix(in srgb, #2da44e 12%, transparent); color: #1a7f37; }
.preview.warn { background: color-mix(in srgb, #b58a00 14%, transparent); color: #8a6d00; }
.pv-t { font-weight: 500; margin-bottom: 2px; }
.pv-d { word-break: break-all; }
.pv-s { font-size: 11.5px; margin-top: 2px; }
</style>
