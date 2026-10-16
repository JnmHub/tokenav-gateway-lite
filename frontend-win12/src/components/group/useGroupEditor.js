/* ============================================================
 * 分组编辑器 composable（二期 A）
 * 弹窗全部状态与保存时序集中在这里，Groups.vue 只负责列表与组装。
 * 契约来源（旧 frontend）：
 *  - 回填/提交：views/admin/GroupsView.vue handleEdit(5738-5841)/handleUpdateGroup(5930-6067)
 *  - 保存时序：编辑 = 先 PUT /admin/groups/:id/pricing 再 PUT /admin/groups/:id
 *              （定价已落库而主体被拒时提示并保留弹窗）；创建 = 自定义定价开启时
 *              先建停用草稿，pricing 落库后再激活（草稿保存时序，5668-5689）
 *  - 竞态：sessionSerial + pricingLoadSerial 双序号（5843-5877）
 *  - 路由健康：openai 编辑态异步加载，DELETE 重置单个目标（5704-5717）
 *  - 模型路由：model_routing API↔UI 互转（4982-5026），账号名逐个 GET accounts/:id
 * ============================================================ */
import { computed, ref, watch, onScopeDispose } from 'vue'
import { message } from 'ant-design-vue'
import { request as apiRequest } from '../../api/index.js'
import { isCancellation } from '../../api/session.js'
import { useDiscardGuard } from '../../core/windowContext.js'
import {
  mappingToRows,
  validateMappingRows, MAPPING_ERROR_MESSAGES,
} from './modelMapping.js'
import {
  createPricingFormState, pricingConfigToForm,
  validatePricingForm, PRICING_ERROR_MESSAGES,
  normalizeGrokVideoMatrix,
} from './pricing.js'
// 二期 B：Messages 调度 / 推理力度（字段契约见各模块头注释）
import {
  messagesDispatchConfigToFormState,
  createDefaultMessagesDispatchConfigState,
} from './messagesDispatch.js'
import {
  supportsReasoningEffort, normalizeReasoningEffort,
  reasoningEffortToRows, validateReasoningEffortRows,
} from './reasoningEffort.js'
// v0.2.5：分组利润控制（换算/校验助手见 profitControl.js）
import {
  isProfitControlPlatform, validateProfitControl, PROFIT_CONTROL_ERROR_MESSAGES,
} from './profitControl.js'

import { defaultForm, normModelsList, buildGroupBody } from './groupEditorPayload.js'
import { saveGroupChanges, GroupSaveError } from './groupSave.js'

export function useGroupEditor(onSaved) {
  const controller = new AbortController()
  const request = (path, options = {}) => apiRequest(path, { ...options, signal: controller.signal })
  onScopeDispose(() => { controller.abort(); closeEditor(); candidateSerial += 1 })
  const editOpen = ref(false)
  const editSaving = ref(false)
  const editForm = ref(defaultForm())
  const activeKeys = ref(['basics']) // a-collapse 展开面板

  // 竞态序号：切换/关闭分组后让迟到的异步响应全部失效（对照旧 editSessionSerial）
  let sessionSerial = 0
  let pricingLoadSerial = 0
  // 编辑对象快照（分组原始行 + models_list_config 白名单原样回传）
  let editingRow = null
  let modelsListSnapshot = { enabled: false, models: [] }

  // ===== 专属定价（懒加载） =====
  const pricingForm = ref(createPricingFormState())
  const pricingLoading = ref(false)
  const pricingReady = ref(false)
  const pricingLoadError = ref(false)
  const pricingShared = ref(false)
  const formBaseline = ref(''), pricingBaseline = ref(''), routingBaseline = ref('')
  const routingSnapshot = () => JSON.stringify({ enabled: routingEnabled.value, rules: routingRules.value.map(rule => ({ pattern: rule.pattern, ids: rule.accounts.map(account => account.id) })) })
  const isDirty = computed(() => editOpen.value && (JSON.stringify(editForm.value) !== formBaseline.value
    || JSON.stringify(pricingForm.value) !== pricingBaseline.value || routingSnapshot() !== routingBaseline.value))
  const captureBaseline = () => {
    formBaseline.value = JSON.stringify(editForm.value); pricingBaseline.value = JSON.stringify(pricingForm.value); routingBaseline.value = routingSnapshot()
  }
  const beforeClose = useDiscardGuard(() => isDirty.value, () => editSaving.value)

  const loadPricing = async (group, serial = sessionSerial) => {
    const loadSerial = ++pricingLoadSerial
    pricingLoading.value = true
    pricingReady.value = false
    pricingLoadError.value = false
    try {
      const pricing = await request(`/admin/groups/${group.id}/pricing`, { method: 'GET' })
      if (serial !== sessionSerial || loadSerial !== pricingLoadSerial || !editOpen.value || editingRow?.id !== group.id) return
      pricingForm.value = pricingConfigToForm(pricing, group.platform)
      pricingBaseline.value = JSON.stringify(pricingForm.value)
      pricingShared.value = !!pricing.shared
      pricingReady.value = true
    } catch (e) {
      if (serial !== sessionSerial || loadSerial !== pricingLoadSerial || !editOpen.value || editingRow?.id !== group.id || isCancellation(e)) return
      pricingLoadError.value = true
      message.error(`读取分组价格失败：${e.message}`)
    } finally {
      if (serial === sessionSerial && loadSerial === pricingLoadSerial) pricingLoading.value = false
    }
  }
  const retryPricing = () => { if (editingRow) loadPricing(editingRow) }

  // ===== 模型映射候选（models-list-candidates + 映射精确源合并） =====
  const apiCandidates = ref([])
  const candidatesLoading = ref(false)
  let candidateSerial = 0
  const loadCandidates = async (groupId, platform) => {
    const seq = ++candidateSerial
    candidatesLoading.value = true
    try {
      const data = await request(`/admin/groups/${groupId}/models-list-candidates`, { method: 'GET', query: { platform } })
      if (seq === candidateSerial) apiCandidates.value = data.models || []
    } catch {
      if (seq === candidateSerial) apiCandidates.value = []
    } finally {
      if (seq === candidateSerial) candidatesLoading.value = false
    }
  }
  // 映射的精确源模型并入候选（对照旧 mergeModelsListCandidates 用途）
  const mappingCandidates = computed(() => {
    const sources = editForm.value.model_mapping_rows
      .map((r) => r.from.trim())
      .filter((m) => m && !m.includes('*'))
    return [...new Set([...apiCandidates.value, ...sources])]
  })

  // ===== 分流目标路由健康（仅 openai 编辑态） =====
  const routeHealth = ref([])
  const loadRouteHealth = async (groupId, serial = sessionSerial) => {
    try {
      const data = await request(`/admin/groups/${groupId}/model-route-health`, { method: 'GET' })
      if (serial === sessionSerial && editOpen.value) routeHealth.value = data.targets ?? []
    } catch {
      if (serial === sessionSerial) routeHealth.value = [] // 健康数据不可用不影响编辑
    }
  }
  // 手动清除某个分流目标的冷却：DELETE + 重新拉取（对照旧 handleResetRouteHealth）
  const resetRouteHealth = async (publicModel, targetModel) => {
    const groupId = editingRow?.id, serial = sessionSerial
    if (!groupId) return
    try {
      await request(`/admin/groups/${groupId}/model-route-health`, {
        method: 'DELETE',
        body: { public_model: publicModel, target_model: targetModel },
      })
      if (serial !== sessionSerial || !editOpen.value) return
      routeHealth.value = []
      await loadRouteHealth(groupId, serial)
      message.success('已重置该分流目标的健康状态')
    } catch (e) {
      if (serial === sessionSerial && !isCancellation(e)) message.error(`重置健康状态失败：${e.message}`)
    }
  }

  // ===== anthropic 模型路由规则（UI↔API 互转） =====
  const routingEnabled = ref(false)
  const routingRules = ref([])
  // 先同步保留路由 ID；名称只是展示信息，迟到响应不能替换规则或重置草稿基线。
  const hydrateRoutingNames = async (serial) => {
    const ids = [...new Set(routingRules.value.flatMap(rule => rule.accounts.map(account => account.id)))]
    for (const id of ids) {
      try {
        const account = await request(`/admin/accounts/${id}`, { method: 'GET' })
        if (serial !== sessionSerial || !editOpen.value) return
        for (const rule of routingRules.value) {
          for (const current of rule.accounts) {
            if (current.id === id && current.name === `#${id}`) current.name = account.name || `#${id}`
          }
        }
      } catch {
        if (serial !== sessionSerial || !editOpen.value) return
        // 已删除/暂时不可读的账号仍保留 ID，不改变路由提交内容。
      }
    }
  }
  // ===== 兜底分组候选（anthropic） =====
  const anthropicGroups = ref([])
  const loadAnthropicGroups = async () => {
    try {
      const list = await request('/admin/groups/all', { method: 'GET', query: { platform: 'anthropic' } })
      anthropicGroups.value = list || []
    } catch {
      anthropicGroups.value = []
    }
  }
  // Claude Code 降级分组候选：anthropic + 非仅CC + active，编辑时排除自身（旧 4399-4415）
  const fallbackGroupOptions = computed(() => {
    const currentId = editForm.value.id
    return [{ value: null, label: '关闭兜底' }, ...anthropicGroups.value
      .filter((g) => !g.claude_code_only && g.status === 'active' && g.id !== currentId)
      .map((g) => ({ value: g.id, label: g.name }))]
  })
  // 无效请求兜底候选：anthropic + active + 非订阅 + 未配置无效兜底，排除自身（旧 4436-4453）
  const invalidFallbackOptions = computed(() => {
    const currentId = editForm.value.id
    return [{ value: null, label: '关闭兜底' }, ...anthropicGroups.value
      .filter((g) => g.status === 'active' && g.subscription_type !== 'subscription'
        && g.fallback_group_id_on_invalid_request == null && g.id !== currentId)
      .map((g) => ({ value: g.id, label: g.name }))]
  })

  // ===== openai 可用性兜底候选（一期已有，迁移进来统一管理） =====
  const failoverGroups = ref([])
  const loadFailoverGroups = async () => {
    try {
      failoverGroups.value = (await request('/admin/groups/all', { method: 'GET', query: { platform: 'openai' } })) || []
    } catch {
      failoverGroups.value = []
    }
  }
  const failoverSelectOptions = computed(() => [
    { value: null, label: '关闭跨分组兜底' },
    ...failoverGroups.value.filter(group => group.id !== editForm.value.id && group.status === 'active').map((g) => ({ value: g.id, label: g.name })),
  ])

  const compositeEnabled = ref(false), compositeReady = ref(false)
  const loadCompositeSetting = async () => {
    compositeReady.value = false
    try { const setting = await request('/admin/settings/composite-groups', { method: 'GET' }); compositeEnabled.value = setting?.enabled === true }
    catch (error) { if (!isCancellation(error)) compositeEnabled.value = false }
    finally { if (!controller.signal.aborted) compositeReady.value = true }
  }
  const copySources = ref([])
  const loadCopySources = async () => {
    try { copySources.value = await request('/admin/groups/all', { method: 'GET' }) || [] }
    catch (error) { if (!isCancellation(error)) copySources.value = [] }
  }
  const copyAccountsOptions = computed(() => copySources.value.filter(group => group.id !== editForm.value.id
    && (editForm.value.platform === 'composite' || group.platform === editForm.value.platform) && group.account_count > 0)
    .map(group => ({ value: group.id, label: `${group.name}（${group.account_count} 个账号）` })))

  // ===== 打开/关闭 =====
  const resetEditorState = () => {
    pricingForm.value = createPricingFormState()
    pricingLoading.value = false
    pricingReady.value = false
    pricingLoadError.value = false
    pricingShared.value = false
    routingEnabled.value = false
    routingRules.value = []
    routeHealth.value = []
    apiCandidates.value = []
    modelsListSnapshot = { enabled: false, models: [] }
  }

  const openCreate = async () => {
    if (editSaving.value || (editOpen.value && !await beforeClose())) return
    sessionSerial += 1
    pricingLoadSerial += 1
    editingRow = null
    resetEditorState()
    editForm.value = defaultForm()
    activeKeys.value = ['basics']
    loadFailoverGroups()
    loadAnthropicGroups()
    loadCandidates(0, 'anthropic')
    loadCopySources()
    loadCompositeSetting()
    editOpen.value = true
    captureBaseline()
  }

  // 回填对照旧 handleEdit：?? 与 || 语义保持一致（0/空串/null 兜底规则）
  const openEdit = async (r) => {
    if (editSaving.value || (editOpen.value && !await beforeClose())) return
    const serial = ++sessionSerial
    pricingLoadSerial += 1
    editingRow = r
    resetEditorState()
    const f = defaultForm()
    Object.assign(f, {
      id: r.id,
      name: r.name,
      description: r.description || '',
      display_brand: r.display_brand || '',
      display_icon: r.display_icon || '',
      rate_multiplier: r.rate_multiplier ?? 1,
      local_only: !!r.local_only,
      platform: r.platform || 'anthropic',
      status: r.status || 'active',
      subscription_type: r.subscription_type || 'standard',
      daily_limit_usd: r.daily_limit_usd ?? null,
      weekly_limit_usd: r.weekly_limit_usd ?? null,
      monthly_limit_usd: r.monthly_limit_usd ?? null,
      is_exclusive: !!r.is_exclusive,
      minimum_tier: r.minimum_tier ?? null,
      scheduling_mode: r.scheduling_mode || 'default',
      per_user_concurrency: r.per_user_concurrency || 0,
      rpm_limit: r.rpm_limit ?? 0,
      peak_rate_enabled: r.peak_rate_enabled ?? false,
      peak_start: r.peak_start || '18:00',
      peak_end: r.peak_end || '22:00',
      peak_rate_multiplier: r.peak_rate_multiplier ?? 1,
      peak_timezone: r.peak_timezone || 'Asia/Shanghai',
      billing_model_source: r.billing_model_source || 'requested',
      allow_blocked_keyword_requests: r.allow_blocked_keyword_requests ?? false,
      require_oauth_only: r.require_oauth_only ?? false,
      require_privacy_set: r.require_privacy_set ?? false,
      allow_openai_long_context_billing: r.allow_openai_long_context_billing ?? false,
      allow_live: r.allow_live ?? false,
      fallback_model: r.fallback_model || '',
      failover_group_id: r.failover_group_id || null,
      failover_strategy: r.failover_strategy || 'original_model_first',
      claude_code_only: r.claude_code_only || false,
      mcp_xml_inject: r.mcp_xml_inject ?? true,
      supported_model_scopes: r.supported_model_scopes || ['claude', 'gemini_text', 'gemini_image'],
      allow_image_generation: r.allow_image_generation ?? false,
      // —— 二期 A ——
      image_rate_independent: r.image_rate_independent ?? false,
      image_rate_multiplier: r.image_rate_multiplier ?? 1,
      image_price_1k: r.image_price_1k ?? null,
      image_price_2k: r.image_price_2k ?? null,
      image_price_4k: r.image_price_4k ?? null,
      video_rate_independent: r.video_rate_independent ?? false,
      video_rate_multiplier: r.video_rate_multiplier ?? 1,
      video_price_480p: r.video_price_480p ?? null,
      video_price_720p: r.video_price_720p ?? null,
      video_price_1080p: r.video_price_1080p ?? null,
      video_model_prices: normalizeGrokVideoMatrix(r.video_model_prices),
      web_search_price_per_call: r.web_search_price_per_call ?? null,
      search_price_per_1k: r.search_price_per_1k ?? null,
      audio_realtime_price_per_min: r.audio_realtime_price_per_min ?? null,
      audio_tts_price_per_million_chars: r.audio_tts_price_per_million_chars ?? null,
      audio_stt_price_per_hour: r.audio_stt_price_per_hour ?? null,
      model_mapping_rows: mappingToRows(r.model_mapping, r.weighted_model_mapping),
      fallback_group_id: r.fallback_group_id ?? null,
      fallback_group_id_on_invalid_request: r.fallback_group_id_on_invalid_request ?? null,
      response_model_mode: r.models_list_config?.response_model_mode || 'request',
      response_model_custom: r.models_list_config?.response_model_custom || '',
      // —— 二期 B ——
      allow_messages_dispatch: r.allow_messages_dispatch ?? false,
      default_mapped_model: r.default_mapped_model || '',
      // 家族映射留空回退内置默认；精确映射对象转行数组（按源模型排序）
      ...messagesDispatchConfigToFormState(r.messages_dispatch_model_config),
      max_reasoning_effort: normalizeReasoningEffort(r.platform, r.max_reasoning_effort),
      reasoning_effort_rows: reasoningEffortToRows(r.reasoning_effort_mappings),
      // —— v0.2.5：利润控制回填（小数原样，非数值兜底 0） ——
      profit_control_enabled: r.profit_control_enabled ?? false,
      profit_min_margin: Number(r.profit_min_margin) || 0,
      profit_safety_buffer: Number(r.profit_safety_buffer) || 0,
    })
    editForm.value = f
    routingEnabled.value = r.model_routing_enabled || false
    routingRules.value = Object.entries(r.model_routing || {}).map(([pattern, ids]) => ({
      pattern, accounts: (ids || []).map(id => ({ id, name: `#${id}` })),
    }))
    // /v1/models 白名单（models_list_config.enabled/models）：先快照原值，再接入
    // 表单双向绑定；未改动的值保存时原样回传（一期防清空语义保留）
    modelsListSnapshot = {
      enabled: !!r.models_list_config?.enabled,
      models: [...(r.models_list_config?.models || [])],
    }
    f.models_list_enabled = modelsListSnapshot.enabled
    f.models_list_models = [...modelsListSnapshot.models]
    activeKeys.value = ['basics']
    loadFailoverGroups()
    loadAnthropicGroups()
    if (r.platform === 'openai' && r.id) loadRouteHealth(r.id, serial) // 非阻塞加载
    loadCandidates(r.id, f.platform)
    loadPricing(r, serial) // 懒加载定价
    loadCopySources()
    loadCompositeSetting()
    editOpen.value = true
    captureBaseline()
    hydrateRoutingNames(serial)
  }

  const closeEditor = () => {
    // 关闭或切换分组后让迟到的异步响应失效（对照旧 closeEditModal）
    sessionSerial += 1
    pricingLoadSerial += 1
    candidateSerial += 1
    editOpen.value = false
    editingRow = null
    resetEditorState()
    editForm.value = defaultForm()
  }

  // ===== 保存 =====
  const save = async () => {
    if (editSaving.value) return
    const f = editForm.value
    if (!editOpen.value || controller.signal.aborted) return
    if (f.platform === 'composite' && (!compositeReady.value || !compositeEnabled.value)) { message.error('请在设置的功能页启用聚合分组总开关后再保存。'); return }
    if (!f.name.trim()) { message.error('名称为必填项'); return }
    if (f.peak_rate_enabled && f.peak_start === f.peak_end) {
      message.error('高峰时段：开始和结束时间不能相同'); return
    }
    // 模型映射校验（照旧：保存前阻断）
    const mappingError = validateMappingRows(f.model_mapping_rows, f.platform)
    if (mappingError) { message.error(MAPPING_ERROR_MESSAGES[mappingError] || mappingError); return }
    // 响应声明模型：custom 必填（后端 400 前置校验）
    if (f.response_model_mode === 'custom' && !f.response_model_custom.trim()) {
      message.error('选择“自定义”响应模型时必须填写模型名称'); return
    }
    // 推理力度映射：from/to 必填且源档位唯一、≤64 条（后端同样校验，前置提示更友好）
    const effortError = supportsReasoningEffort(f.platform)
      ? validateReasoningEffortRows(f.reasoning_effort_rows)
      : null
    if (effortError) { message.error(`推理力度映射：${effortError}`); return }
    // 白名单：开启时至少一个模型（后端 400：models is required）
    if (f.models_list_enabled && normModelsList(f.models_list_models).length === 0) {
      message.error('开启模型列表白名单后至少保留一个模型'); return
    }
    // 利润控制：照官方校验（margin/buffer ∈ [0,1) 且相加 < 1，仅开启且支持平台时）
    const profitError = validateProfitControl(f)
    if (profitError) { message.error(`利润控制：${PROFIT_CONTROL_ERROR_MESSAGES[profitError] || profitError}`); return }
    // 定价：读取成功前禁存（防止误关闭已有定价），再校验表单
    const isEdit = !!f.id
    if (isEdit && (!pricingReady.value || pricingLoading.value)) {
      message.error('分组定价尚未读取成功，请重试后再保存。'); return
    }
    const pricingError = validatePricingForm(pricingForm.value, f.platform)
    if (pricingError) {
      const code = pricingError.split(':')[0]
      message.error(code === 'interval' ? pricingError.slice('interval:'.length) : PRICING_ERROR_MESSAGES[code] || '定价配置不完整，请检查后保存。')
      return
    }

    editSaving.value = true
    try {
      const body = buildGroupBody(f, routingRules.value, routingEnabled.value)
      await saveGroupChanges({ request, form: f, body, pricing: pricingForm.value,
        onPricingSaved: () => { pricingBaseline.value = JSON.stringify(pricingForm.value) } })
      message.success(isEdit ? '分组已更新' : '分组已创建')
      closeEditor()
      onSaved?.()
    } catch (e) {
      if (isCancellation(e)) return
      if (e instanceof GroupSaveError && e.stage === 'base') {
        message.error('定价已保存，但分组基础设置保存失败：' + e.message)
      } else if (e instanceof GroupSaveError && e.created) {
        const reason = e.pricingSaved ? '定价已保存，启用失败' : '定价保存失败'
        message.error('分组「' + e.created.name + '」已保留为停用草稿，' + reason + '：' + e.message)
        // 已有草稿必须重新进入编辑，下一次保存使用 PUT，不能再次 POST 创建重复分组。
        closeEditor(); editSaving.value = false
        onSaved?.()
        await openEdit({ ...e.created, status: 'inactive' })
      } else message.error('保存失败：' + e.message)
    } finally {
      editSaving.value = false
    }
  }

  // 创建模式下切换平台时重载候选（id=0 平台默认模型），并复位不适用平台的策略字段
  watch(() => editForm.value.platform, (p, old) => {
    if (!editOpen.value || editForm.value.id) return
    if (p && p !== old) {
      loadCandidates(0, p)
      editForm.value.copy_accounts_from_group_ids = []
      // 平台切换副作用（对照旧 6194-6230 / 6270-6271）：策略字段随平台复位
      if (!supportsReasoningEffort(p)) {
        editForm.value.max_reasoning_effort = ''
        editForm.value.reasoning_effort_rows = []
      }
      if (p !== 'openai') {
        editForm.value.allow_messages_dispatch = false
        editForm.value.default_mapped_model = ''
        Object.assign(editForm.value, createDefaultMessagesDispatchConfigState())
      }
      // 利润控制随平台复位：不支持的平台关闭开关并清零阈值
      if (!isProfitControlPlatform(p)) {
        editForm.value.profit_control_enabled = false
        editForm.value.profit_min_margin = 0
        editForm.value.profit_safety_buffer = 0
      }
    }
  })

  return {
    editOpen, editSaving, editForm, activeKeys,
    pricingForm, pricingLoading, pricingReady, pricingLoadError, pricingShared,
    retryPricing,
    routingEnabled, routingRules,
    routeHealth, resetRouteHealth,
    mappingCandidates, candidatesLoading,
    apiCandidates, // 白名单候选（models-list-candidates 原始列表，供 ModelsListFields 点选）
    fallbackGroupOptions, invalidFallbackOptions,
    failoverGroups, failoverSelectOptions, copyAccountsOptions, compositeEnabled, compositeReady,
    isDirty, beforeClose,
    openCreate, openEdit, closeEditor, save,
  }
}
