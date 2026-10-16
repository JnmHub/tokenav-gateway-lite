import { describe, expect, it, vi } from 'vitest'
import { isStepUpCancelled, useStepUp } from '../useStepUp'

describe('useStepUp', () => {
  it('prompts once and replays the original action with a one-time token', async () => {
    const controller = useStepUp()
    const action = vi.fn()
      .mockRejectedValueOnce({ code: 'STEP_UP_REQUIRED' })
      .mockResolvedValueOnce('done')

    const pending = controller.run('accounts.export', action)
    await Promise.resolve()
    expect(controller.visible.value).toBe(true)
    expect(controller.actionScope.value).toBe('accounts.export')

    controller.onVerified('su_once')
    await expect(pending).resolves.toBe('done')
    expect(action).toHaveBeenNthCalledWith(1)
    expect(action).toHaveBeenNthCalledWith(2, 'su_once')
    expect(action).toHaveBeenCalledTimes(2)
  })

  it('does not prompt when TOTP is not enabled', async () => {
    const controller = useStepUp()
    const error = { code: 'STEP_UP_TOTP_NOT_ENABLED' }
    await expect(controller.run('audit_logs.clear', async () => { throw error })).rejects.toBe(error)
    expect(controller.visible.value).toBe(false)
    expect(controller.blockedReason.value).toBe('STEP_UP_TOTP_NOT_ENABLED')
  })

  it('surfaces cancellation without replaying the action', async () => {
    const controller = useStepUp()
    const action = vi.fn().mockRejectedValueOnce({ code: 'STEP_UP_REQUIRED' })
    const pending = controller.run('proxies.export', action)
    await Promise.resolve()
    controller.onCancel()
    try {
      await pending
      throw new Error('expected cancellation')
    } catch (error) {
      expect(isStepUpCancelled(error)).toBe(true)
    }
    expect(action).toHaveBeenCalledTimes(1)
  })
})
