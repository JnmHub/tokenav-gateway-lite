import { defineComponent, defineAsyncComponent, h, markRaw, shallowRef } from 'vue'
import { ReloadOutlined, LoadingOutlined } from '@ant-design/icons-vue'

// 重试创建新的异步实例，不能只重开已缓存为失败态的注册组件。
export function lazyApp(loader) {
  return markRaw(defineComponent({
    name: 'W12LazyApplication',
    setup(_, { attrs }) {
      const current = shallowRef(null)
      const retry = () => {
        current.value = markRaw(defineAsyncComponent({ loader, delay: 120, timeout: 30_000,
          loadingComponent: defineComponent({ setup: () => () => h('div', { class: 'w12-module-state', role: 'status' }, [h(LoadingOutlined, { spin: true }), '正在加载应用…']) }),
          errorComponent: defineComponent({ setup: () => () => h('div', { class: 'w12-module-state', role: 'alert' }, [
            h('p', '应用加载失败，请检查连接后重试。'),
            h('button', { class: 'w12-retry', onClick: retry }, [h(ReloadOutlined), '重试加载']),
          ]) }),
        }))
      }
      retry()
      return () => h(current.value, attrs)
    },
  }))
}
