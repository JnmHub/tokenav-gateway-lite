import { describe, expect, it } from 'vitest'
import {
  applyCodexImageToolMode,
  buildCodexImageToolModePatch,
  resolveCodexImageToolMode
} from '../codexImageToolPolicy'

describe('codexImageToolPolicy', () => {
  it('优先识别彻底阻断，并兼容新旧桥接布尔字段', () => {
    expect(resolveCodexImageToolMode()).toBe('inherit')
    expect(resolveCodexImageToolMode({ codex_image_generation_bridge_enabled: true })).toBe('enabled')
    expect(resolveCodexImageToolMode({ codex_image_generation_bridge: false })).toBe('disabled')
    expect(
      resolveCodexImageToolMode({
        codex_image_generation_bridge: true,
        codex_image_generation_explicit_tool_policy: 'strip'
      })
    ).toBe('block')
  })

  it('完整 extra 快照只保留当前模式需要的字段', () => {
    const extra = {
      keep: 'value',
      codex_image_generation_bridge_enabled: true,
      codex_image_generation_explicit_tool_policy: 'strip'
    }

    expect(applyCodexImageToolMode(extra, 'disabled')).toEqual({
      keep: 'value',
      codex_image_generation_bridge: false
    })
    expect(applyCodexImageToolMode(extra, 'block')).toEqual({
      keep: 'value',
      codex_image_generation_explicit_tool_policy: 'strip'
    })
    expect(applyCodexImageToolMode(extra, 'inherit')).toEqual({ keep: 'value' })
  })

  it('批量恢复继承时使用 null 清除数据库中的旧覆盖', () => {
    expect(buildCodexImageToolModePatch('inherit')).toEqual({
      codex_image_generation_bridge: null,
      codex_image_generation_bridge_enabled: null,
      codex_image_generation_explicit_tool_policy: null
    })
    expect(buildCodexImageToolModePatch('enabled')).toMatchObject({
      codex_image_generation_bridge: true,
      codex_image_generation_explicit_tool_policy: null
    })
    expect(buildCodexImageToolModePatch('block')).toMatchObject({
      codex_image_generation_bridge: null,
      codex_image_generation_explicit_tool_policy: 'strip'
    })
  })
})
