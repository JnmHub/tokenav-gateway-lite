import { ref, onUnmounted } from 'vue'
import { readSettings, saveSettingsPart, settingsErrorMessage } from '../api/settings.js'
import { createLatestRequest } from '../api/latestRequest.js'
import { cancellationError } from '../api/session.js'
import { useRequestLifetime } from './useRequestLifetime.js'

export function useSettingsContract(clear) {
  const version = ref('')
  const conflict = ref('')
  const lane = createLatestRequest()
  let writing = false
  const lifetime = useRequestLifetime(() => { lane.cancel(); version.value = ''; conflict.value = ''; clear?.() })
  onUnmounted(() => lane.dispose())
  return {
    version, conflict, lifetime,
    async read(options = {}) {
      if (writing) throw cancellationError('保存中，请稍后读取')
      const turn = lane.begin()
      const settings = await readSettings({ ...lifetime, signal: AbortSignal.any([lifetime.signal, turn.signal, ...(options.signal ? [options.signal] : [])]) })
      if (!turn.isCurrent()) throw cancellationError()
      version.value = settings.version
      conflict.value = ''
      return settings
    },
    async save(fields) {
      if (writing) throw cancellationError('设置正在保存')
      writing = true
      lane.cancel()
      try {
        const settings = await saveSettingsPart(version.value, fields, lifetime)
        version.value = settings.version
        conflict.value = ''
        return settings
      } catch (error) {
        if (error.reason === 'SETTINGS_VERSION_CONFLICT') conflict.value = settingsErrorMessage(error)
        throw error
      } finally { writing = false }
    },
  }
}
