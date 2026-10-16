export type CodexImageToolMode = 'inherit' | 'enabled' | 'disabled' | 'block'

const BRIDGE_KEY = 'codex_image_generation_bridge'
const LEGACY_BRIDGE_KEY = 'codex_image_generation_bridge_enabled'
const EXPLICIT_TOOL_POLICY_KEY = 'codex_image_generation_explicit_tool_policy'

export function resolveCodexImageToolMode(
  extra?: Record<string, unknown> | null
): CodexImageToolMode {
  if (extra?.[EXPLICIT_TOOL_POLICY_KEY] === 'strip') {
    return 'block'
  }

  const bridgeValue =
    typeof extra?.[BRIDGE_KEY] === 'boolean'
      ? extra[BRIDGE_KEY]
      : extra?.[LEGACY_BRIDGE_KEY]

  if (bridgeValue === true) return 'enabled'
  if (bridgeValue === false) return 'disabled'
  return 'inherit'
}

// 编辑和创建会发送完整 extra 快照，因此可以直接删除不再需要的覆盖字段。
export function applyCodexImageToolMode(
  extra: Record<string, unknown>,
  mode: CodexImageToolMode
): Record<string, unknown> {
  delete extra[BRIDGE_KEY]
  delete extra[LEGACY_BRIDGE_KEY]
  delete extra[EXPLICIT_TOOL_POLICY_KEY]

  if (mode === 'enabled' || mode === 'disabled') {
    extra[BRIDGE_KEY] = mode === 'enabled'
  } else if (mode === 'block') {
    extra[EXPLICIT_TOOL_POLICY_KEY] = 'strip'
  }

  return extra
}

// 批量更新使用 PostgreSQL JSONB 增量合并。恢复继承时必须写 null 覆盖旧键，
// 仅省略字段不会删除数据库里的旧策略，界面会显示已保存但请求仍沿用旧值。
export function buildCodexImageToolModePatch(
  mode: CodexImageToolMode
): Record<string, unknown> {
  const patch: Record<string, unknown> = {
    [BRIDGE_KEY]: null,
    [LEGACY_BRIDGE_KEY]: null,
    [EXPLICIT_TOOL_POLICY_KEY]: null
  }

  if (mode === 'enabled' || mode === 'disabled') {
    patch[BRIDGE_KEY] = mode === 'enabled'
  } else if (mode === 'block') {
    patch[EXPLICIT_TOOL_POLICY_KEY] = 'strip'
  }

  return patch
}
