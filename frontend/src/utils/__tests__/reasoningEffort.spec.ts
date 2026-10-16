import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

import { formatReasoningEffort } from '../format'

const zhLabels: Record<string, string> = {
  'usage.reasoningEffortUnspecified': '—（未指定）',
  'usage.reasoningEffortLevels.none': '关闭',
  'usage.reasoningEffortLevels.minimal': '最低',
  'usage.reasoningEffortLevels.low': '低',
  'usage.reasoningEffortLevels.medium': '中',
  'usage.reasoningEffortLevels.high': '高',
  'usage.reasoningEffortLevels.xhigh': '极高',
  'usage.reasoningEffortLevels.max': '最高'
}

const translate = (key: string) => zhLabels[key] ?? key

describe('formatReasoningEffort', () => {
  it('仅对真正缺失的档次显示横线', () => {
    expect(formatReasoningEffort(null, translate)).toBe('—（未指定）')
    expect(formatReasoningEffort('', translate)).toBe('—（未指定）')
    expect(formatReasoningEffort('none', translate)).toBe('None')
    expect(formatReasoningEffort('minimal', translate)).toBe('Minimal')
  })

  it('统一格式化所有内置档次及兼容拼写', () => {
    expect(formatReasoningEffort('low', translate)).toBe('Low')
    expect(formatReasoningEffort('medium', translate)).toBe('Medium')
    expect(formatReasoningEffort('high', translate)).toBe('High')
    expect(formatReasoningEffort('x-high', translate)).toBe('XHigh')
    expect(formatReasoningEffort('extra_high', translate)).toBe('XHigh')
    expect(formatReasoningEffort('max', translate)).toBe('Max')
  })

  it('未知扩展档次保留原文且无翻译函数时使用英文标准名', () => {
    expect(formatReasoningEffort('future-ultra', translate)).toBe('future-ultra')
    expect(formatReasoningEffort('x_high')).toBe('XHigh')
  })

  it('轻量网关日志只读取最终实际上游档次且不读取客户请求档次', () => {
    const usageTable = readFileSync(
      resolve(process.cwd(), 'src/components/admin/usage/UsageTable.vue'),
      'utf8'
    )
    const usageView = readFileSync(resolve(process.cwd(), 'src/views/admin/UsageView.vue'), 'utf8')

    expect(usageTable).toContain('row.reasoning_effort')
    expect(usageTable).toContain('tooltipData?.reasoning_effort')
    expect(usageTable).not.toContain('requested_reasoning_effort')
    expect(usageView).toContain('log.reasoning_effort')
    expect(usageView).not.toContain('requested_reasoning_effort')
  })
})
