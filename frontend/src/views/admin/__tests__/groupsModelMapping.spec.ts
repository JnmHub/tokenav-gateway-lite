import { describe, expect, it } from 'vitest'
import {
  groupModelMappingRowsToRecord,
  groupModelMappingToRows,
  validateGroupModelMappingRows,
} from '../groupsModelMapping'

describe('groupsModelMapping', () => {
  it('hydrates deterministic editable rows from a mapping record', () => {
    expect(groupModelMappingToRows({ ' gpt-public ': ' gpt-upstream ', claude: 'claude-sonnet' })).toEqual([
      { from: 'claude', to: 'claude-sonnet' },
      { from: 'gpt-public', to: 'gpt-upstream' },
    ])
  })

  it('trims complete rows and ignores a fully empty draft row', () => {
    expect(groupModelMappingRowsToRecord([
      { from: ' gpt-public ', to: ' gpt-upstream ' },
      { from: '', to: '' },
    ])).toEqual({ 'gpt-public': 'gpt-upstream' })
  })

  it('rejects incomplete and duplicate source mappings', () => {
    expect(validateGroupModelMappingRows([{ from: 'gpt-public', to: '' }])).toBe('incomplete')
    expect(validateGroupModelMappingRows([
      { from: 'gpt-public', to: 'gpt-a' },
      { from: ' GPT-PUBLIC ', to: 'gpt-b' },
    ])).toBe('duplicate')
  })

  it('rejects invalid wildcards and direct or indirect cycles before submit', () => {
    expect(validateGroupModelMappingRows([{ from: 'gpt-*preview*', to: 'gpt-5.6-sol' }]))
      .toBe('sourceWildcard')
    expect(validateGroupModelMappingRows([{ from: '*', to: 'gpt-5.6-sol' }]))
      .toBe('sourceWildcard')
    expect(validateGroupModelMappingRows([{ from: 'gpt-public', to: 'gpt-*' }]))
      .toBe('targetWildcard')
    expect(validateGroupModelMappingRows([
      { from: 'model-a', to: 'model-b' },
      { from: 'MODEL-B', to: 'model-a' },
    ])).toBe('cycle')
  })

  it('rejects obvious cross-platform targets for normal groups but allows composite mappings', () => {
    const rows = [{ from: 'gpt-public', to: 'claude-sonnet-4-6' }]
    expect(validateGroupModelMappingRows(rows, 'openai')).toBe('crossPlatform')
    expect(validateGroupModelMappingRows(rows, 'composite')).toBeNull()
  })

  it('accepts split rows with empty to instead of applying the single-target incomplete rule', () => {
    // 分流行的 to 恒为空（目标在 targets 里），与单目标行混排时必须整体通过。
    expect(validateGroupModelMappingRows([
      { from: 'gpt-5.3-codex', to: 'gpt-5.6-luna' },
      { from: 'gpt-5.4-mini', to: '', split: true, targets: [
        { model: 'codex-auto-review', weight: 90 },
        { model: 'gpt-5.6-sol', weight: 10 },
      ] },
    ], 'openai')).toBeNull()
  })

  it('rejects non-openai split targets like glm before submit', () => {
    // glm- 是智谱平台模型，后端 DetectModelPlatform 会拒绝其作为 openai 分组
    // 的分流目标；前端识别表必须对齐，避免放行后只看到笼统的后端 400。
    expect(validateGroupModelMappingRows([
      { from: 'gpt-5.4-mini', to: '', split: true, targets: [
        { model: 'codex-auto-review', weight: 90 },
        { model: 'glm-5.3-flash', weight: 10 },
      ] },
    ], 'openai')).toBe('splitPlatform')
    expect(validateGroupModelMappingRows([
      { from: 'gpt-public', to: 'deepseek-chat' },
    ], 'openai')).toBe('crossPlatform')
  })

  it('validates split sources for emptiness, wildcards and key conflicts', () => {
    expect(validateGroupModelMappingRows([
      { from: '', to: '', split: true, targets: [
        { model: 'codex-auto-review', weight: 90 },
        { model: 'gpt-5.6-sol', weight: 10 },
      ] },
    ], 'openai')).toBe('incomplete')
    expect(validateGroupModelMappingRows([
      { from: 'gpt-*', to: '', split: true, targets: [
        { model: 'codex-auto-review', weight: 90 },
        { model: 'gpt-5.6-sol', weight: 10 },
      ] },
    ], 'openai')).toBe('sourceWildcard')
    expect(validateGroupModelMappingRows([
      { from: 'gpt-public', to: 'gpt-a' },
      { from: ' GPT-PUBLIC ', to: '', split: true, targets: [
        { model: 'codex-auto-review', weight: 90 },
        { model: 'gpt-5.6-sol', weight: 10 },
      ] },
    ], 'openai')).toBe('duplicate')
  })

  it('allows split targets sharing another mapping source (terminal semantics)', () => {
    // 分流目标是终端上游模型：选中后直接按它选账号，不会再叠加任何映射，
    // 所以目标与单目标映射的源（如 gpt-5.6-terra）同名是合法配置，不报链式。
    expect(validateGroupModelMappingRows([
      { from: 'gpt-5.3-codex', to: 'gpt-5.6-luna' },
      { from: 'gpt-5.6-terra', to: 'gpt-5.6-luna' },
      { from: 'gpt-5.5', to: '', split: true, targets: [
        { model: 'gpt-5.6-luna', weight: 64 },
        { model: 'gpt-5.6-terra', weight: 36 },
      ] },
    ], 'openai')).toBeNull()
  })

  it('still rejects a split target equal to its own source', () => {
    // 目标等于自身源等于没分流，保留防手滑拦截。
    expect(validateGroupModelMappingRows([
      { from: 'gpt-5.5', to: '', split: true, targets: [
        { model: 'GPT-5.5', weight: 64 },
        { model: 'gpt-5.6-terra', weight: 36 },
      ] },
    ], 'openai')).toBe('splitChain')
  })
})
