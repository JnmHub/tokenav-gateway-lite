import { describe, expect, it } from 'vitest'
import { getFloatingPanelPosition } from '../floatingPanel'

describe('getFloatingPanelPosition', () => {
  it('uses full safe width on a narrow viewport', () => {
    const position = getFloatingPanelPosition({ top: 40, right: 370, bottom: 80 }, 390, 844)
    expect(position.left).toBe(16)
    expect(position.width).toBe(320)
    expect(position.top).toBe(88)
  })

  it('opens above when there is not enough room below', () => {
    const position = getFloatingPanelPosition({ top: 700, right: 1000, bottom: 740 }, 1024, 768)
    expect(position.top).toBeNull()
    expect(position.bottom).toBe(76)
    expect(position.maxHeight).toBeLessThanOrEqual(537)
  })

  it('clamps a very small viewport without negative dimensions', () => {
    const position = getFloatingPanelPosition({ top: 5, right: 20, bottom: 25 }, 24, 40)
    expect(position.width).toBe(0)
    expect(position.maxHeight).toBeGreaterThanOrEqual(0)
  })
})
