import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it } from 'vitest'

import BaseDialog from '../BaseDialog.vue'

describe('BaseDialog', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    document.body.classList.remove('modal-open')
  })

  it('打开时聚焦首个控件，关闭后恢复原有焦点', async () => {
    const trigger = document.createElement('button')
    document.body.appendChild(trigger)
    trigger.focus()

    const wrapper = mount(BaseDialog, {
      attachTo: document.body,
      props: { show: true, title: 'Dialog' },
      global: {
        stubs: {
          Icon: { template: '<span />' }
        }
      }
    })
    await flushPromises()

    const closeButton = document.body.querySelector<HTMLButtonElement>('button[aria-label="Close modal"]')
    expect(closeButton).not.toBeNull()
    expect(document.activeElement).toBe(closeButton)
    expect(closeButton?.className).toContain('focus-visible:ring-2')

    await wrapper.setProps({ show: false })
    await flushPromises()
    expect(document.activeElement).toBe(trigger)
    wrapper.unmount()
  })
})
