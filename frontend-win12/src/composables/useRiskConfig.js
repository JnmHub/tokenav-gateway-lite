import { ref, onUnmounted } from 'vue'
import { message } from 'ant-design-vue'
import { request, isCancellation } from '../api/index.js'
import { buildRiskConfigPatch, mergeRiskConfigResponse, readRiskConfig, writeRiskConfig, readRiskMetrics, riskModeLabel } from '../api/riskControl.js'
import { createLatestRequest, runLatest } from '../api/latestRequest.js'
import { useRequestLifetime } from './useRequestLifetime.js'

const clone = (value) => value === undefined ? undefined : JSON.parse(JSON.stringify(value))
export function useRiskConfig() {
  const cfg = ref(null), saved = ref(null), status = ref(null), metrics = ref(null), catalog = ref(null), groups = ref([])
  const loading = ref(true), saving = ref(false), err = ref(''), statusError = ref(''), catalogError = ref('')
  const catalogSyncing = ref(false), refreshing = ref(false), apiKey = ref(''), clearAPIKey = ref(false)
  const configLane = createLatestRequest(), statusLane = createLatestRequest(), catalogLane = createLatestRequest()
  const lifetime = useRequestLifetime(() => {
    configLane.cancel(); statusLane.cancel(); catalogLane.cancel()
    cfg.value = saved.value = status.value = metrics.value = catalog.value = null
    apiKey.value = ''; clearAPIKey.value = false; groups.value = []; err.value = statusError.value = catalogError.value = ''
  })
  const accept = (value) => { saved.value = clone(value); cfg.value = clone(value); apiKey.value = ''; clearAPIKey.value = false }
  async function refreshStatus() {
    refreshing.value = true
    await runLatest(statusLane, (signal) => Promise.all([
      request('/admin/risk-control/status', { method: 'GET', ...lifetime, signal }),
      readRiskMetrics({ ...lifetime, signal }),
    ]), {
      apply: ([runtime, ai]) => { status.value = runtime; metrics.value = ai; statusError.value = '' },
      error: (e) => { status.value = metrics.value = null; statusError.value = e.message },
      finish: () => { refreshing.value = false },
    })
  }
  async function loadCatalog() {
    await runLatest(catalogLane, (signal) => request('/admin/risk-control/keyword-catalog/status', { method: 'GET', ...lifetime, signal }), {
      apply: (value) => { catalog.value = value; catalogError.value = '' },
      error: (e) => { catalog.value = null; catalogError.value = e.message },
    })
  }
  async function load() {
    if (saving.value || !lifetime.isAlive()) return
    loading.value = true
    await runLatest(configLane, (signal) => Promise.all([
      readRiskConfig({ ...lifetime, signal }),
      request('/admin/groups/all', { ...lifetime, signal, method: 'GET' }),
    ]), {
      apply: ([value, items]) => { accept(value); groups.value = items || []; err.value = '' },
      error: (e) => { err.value = e.message },
      finish: () => { loading.value = false },
    })
    if (lifetime.isAlive()) await Promise.all([refreshStatus(), loadCatalog()])
  }
  async function save(fields) {
    if (saving.value || !cfg.value || !saved.value || !lifetime.isAlive()) return
    const submitted = clone(cfg.value)
    if (submitted.blocked_keywords?.length > 10000) { message.error('屏蔽关键词最多 10000 个，请删减后保存'); return }
    const submittedKey = apiKey.value, submittedClear = clearAPIKey.value
    const body = buildRiskConfigPatch(submitted, saved.value, { apiKey: submittedKey, clearAPIKey: submittedClear, ...(fields ? { fields } : {}) })
    if (!Object.keys(body).length) { message.info('配置未变更'); return }
    // 保存是新的写入边界，旧重新读取不能晚到覆盖保存回显或清掉新密钥草稿。
    configLane.cancel()
    loading.value = false
    saving.value = true
    err.value = ''
    try {
      const value = await writeRiskConfig(body, lifetime)
      if (!lifetime.isAlive()) return
      cfg.value = mergeRiskConfigResponse(value, cfg.value, submitted, fields)
      saved.value = clone(value)
      if (!fields && apiKey.value === submittedKey) apiKey.value = ''
      if (!fields && clearAPIKey.value === submittedClear) clearAPIKey.value = false
      message.success(`后台已保存：${value.keyword_blocking_mode === 'ai_only' ? '纯 AI' : '本地关键词'} · ${riskModeLabel(value.mode)}`)
      await refreshStatus()
    } catch (e) {
      if (!isCancellation(e) && lifetime.isAlive()) { err.value = e.message; message.error(`保存失败：${e.message}`) }
    } finally { if (lifetime.isAlive()) saving.value = false }
  }
  async function saveKeywords(fields) {
    if (!cfg.value || saving.value) return
    Object.assign(cfg.value, fields)
    await save(['blocked_keywords', 'keyword_rule_packs', 'keyword_group_routing_enabled', 'block_message', 'block_status'])
  }
  async function syncCatalog() {
    if (catalogSyncing.value || !lifetime.isAlive()) return
    catalogLane.cancel()
    catalogSyncing.value = true
    try {
      catalog.value = await request('/admin/risk-control/keyword-catalog/sync', { ...lifetime, method: 'POST' })
      catalogError.value = ''; message.success('词库目录已同步')
    } catch (e) {
      if (!isCancellation(e)) { catalogError.value = e.message; message.error(`同步失败：${e.message}`) }
    } finally { if (lifetime.isAlive()) catalogSyncing.value = false }
  }
  onUnmounted(() => { configLane.dispose(); statusLane.dispose(); catalogLane.dispose() })
  return { cfg, saved, status, metrics, catalog, groups, loading, saving, err, statusError, catalogError, catalogSyncing,
    refreshing, apiKey, clearAPIKey, lifetime, load, save, saveKeywords, refreshStatus, loadCatalog, syncCatalog }
}
