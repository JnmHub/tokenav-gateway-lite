import { watch, onBeforeUnmount } from 'vue'
import { getSessionEpoch } from '../../api/session.js'

// 同一弹窗关开或换组即换代际；每条查询取消上一次，保存也必须锁定发起时的分组。
export function useGroupDialogRequests(props) {
  const lanes = new Map()
  let generation = 0, disposed = false
  const cancel = key => { lanes.get(key)?.abort(); lanes.delete(key) }
  const cancelAll = () => { generation += 1; for (const key of lanes.keys()) cancel(key) }
  watch(() => [props.open, props.group?.id], cancelAll, { flush: 'sync' })
  onBeforeUnmount(() => { disposed = true; cancelAll() })
  const begin = key => {
    cancel(key)
    const controller = new AbortController(), current = generation, groupId = props.group?.id, epoch = getSessionEpoch()
    lanes.set(key, controller)
    if (disposed || !props.open) controller.abort()
    return {
      signal: controller.signal,
      groupId,
      isCurrent: () => !disposed && props.open && !controller.signal.aborted && current === generation
        && lanes.get(key) === controller && props.group?.id === groupId && getSessionEpoch() === epoch,
    }
  }
  return { begin, cancel, cancelAll }
}
