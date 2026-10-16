/* ============================================================
 * OpenAI Messages 调度配置（messages_dispatch_model_config）助手（二期 B）
 * 契约来源：internal/domain/openai_messages_dispatch.go +
 *   internal/service/openai_messages_dispatch.go + 旧 views/admin/groupsMessagesDispatch.ts
 *  - 家族映射：claude opus/sonnet/haiku → OpenAI 模型；留空时后端使用内置默认
 *    （gpt-5.4 / gpt-5.3-codex / gpt-5.4-mini，与后端常量一致）
 *  - 精确映射 exact_model_mappings：单个 Claude 模型名 → 目标模型，
 *    优先级高于家族映射；空行在序列化时丢弃（与后端规范化一致）
 *  - default_mapped_model（默认映射模型）是账号映射未命中且请求为 Claude
 *    系列模型时的兜底改写目标，与 messages_dispatch_model_config 平级单独保存
 * ============================================================ */

export const MESSAGES_DISPATCH_FAMILY_DEFAULTS = {
  opus_mapped_model: 'gpt-5.4',
  sonnet_mapped_model: 'gpt-5.3-codex',
  haiku_mapped_model: 'gpt-5.4-mini',
}

// 新建分组的家族映射初始值（对照旧 createDefaultMessagesDispatchFormState：
// 预填内置默认，管理员可改可清空）
export const createDefaultMessagesDispatchConfigState = () => ({
  opus_mapped_model: MESSAGES_DISPATCH_FAMILY_DEFAULTS.opus_mapped_model,
  sonnet_mapped_model: MESSAGES_DISPATCH_FAMILY_DEFAULTS.sonnet_mapped_model,
  haiku_mapped_model: MESSAGES_DISPATCH_FAMILY_DEFAULTS.haiku_mapped_model,
  messages_exact_rows: [],
})

// API config → 表单字段：家族留空回退默认；精确映射按源模型排序保证回填顺序稳定
export const messagesDispatchConfigToFormState = (config) => {
  const defaults = createDefaultMessagesDispatchConfigState()
  return {
    opus_mapped_model: String(config?.opus_mapped_model ?? '').trim() || defaults.opus_mapped_model,
    sonnet_mapped_model: String(config?.sonnet_mapped_model ?? '').trim() || defaults.sonnet_mapped_model,
    haiku_mapped_model: String(config?.haiku_mapped_model ?? '').trim() || defaults.haiku_mapped_model,
    messages_exact_rows: Object.entries(config?.exact_model_mappings ?? {})
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([claude_model, target_model]) => ({ claude_model, target_model: String(target_model ?? '') })),
  }
}

// 表单字段 → API config：trim + 丢弃空行（后端会再次规范化，这里保持前置一致）
export const messagesDispatchFormStateToConfig = (form) => {
  const exactModelMappings = {}
  for (const row of form.messages_exact_rows ?? []) {
    const claudeModel = String(row?.claude_model ?? '').trim()
    const targetModel = String(row?.target_model ?? '').trim()
    if (claudeModel && targetModel) exactModelMappings[claudeModel] = targetModel
  }
  return {
    opus_mapped_model: String(form.opus_mapped_model ?? '').trim(),
    sonnet_mapped_model: String(form.sonnet_mapped_model ?? '').trim(),
    haiku_mapped_model: String(form.haiku_mapped_model ?? '').trim(),
    exact_model_mappings: exactModelMappings,
  }
}
