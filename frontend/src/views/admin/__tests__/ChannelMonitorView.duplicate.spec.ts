import { describe, expect, it } from 'vitest'

import channelMonitorViewSource from '../ChannelMonitorView.vue?raw'
import actionsSource from '@/components/admin/monitor/MonitorActionsCell.vue?raw'

describe('ChannelMonitorView duplicate flow', () => {
  it('提供防双击复制入口、确认摘要和复制后编辑引导', () => {
    expect(actionsSource).toContain('data-testid="channel-monitor-duplicate"')
    expect(actionsSource).toContain(':disabled="duplicating"')
    expect(actionsSource).toContain("$emit('duplicate', row)")
    expect(channelMonitorViewSource).toContain('@duplicate="handleDuplicate"')
    expect(channelMonitorViewSource).toContain(':message="duplicateConfirmMessage"')
    expect(channelMonitorViewSource).toContain('if (!source || duplicatingId.value != null) return')
    expect(channelMonitorViewSource).toContain('const duplicate = await adminAPI.channelMonitor.duplicate(source.id)')
    expect(channelMonitorViewSource).toContain('await reload()')
    expect(channelMonitorViewSource).toContain('openEditDialog(duplicate)')
  })
})
