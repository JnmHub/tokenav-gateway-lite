import { ref, computed, onMounted, onUnmounted } from 'vue'
import { request, requestWithMeta, subscribeSessionReset } from '../api/index.js'
import { createLatestRequest, runLatest } from '../api/latestRequest.js'
import { ALL_COLUMNS, DEFAULT_HIDDEN, HIDDEN_COLUMNS_KEY, SORT_STORAGE_KEY, AUTO_REFRESH_KEY, AUTO_REFRESH_INTERVALS, AUTO_REFRESH_SILENT_MS } from '../components/account/columnsConfig.js'

export function useAccountsQuery(isAnyModalOpen) {
  const loading = ref(false), statsEpoch = ref(0), rows = ref([]), total = ref(0)
  const page = ref(1), pageSize = ref(20), selectedIds = ref([]), err = ref('')
  const fPlatform = ref(''), fStatus = ref(''), fSearch = ref(''), fType = ref('')
  const fGroup = ref(''), fPrivacy = ref(''), fWsProbe = ref('')
  const lane = createLatestRequest()
  const autoEnabled = ref(false), autoInterval = ref(30), autoCountdown = ref(0)
  const autoEtag = ref(null), autoFetching = ref(false), silentUntil = ref(0)
  const SORTABLE = new Set(['id', 'name', 'status', 'schedulable', 'priority', 'rate_multiplier', 'last_used_at', 'created_at', 'expires_at'])
  let savedSort, savedColumns, savedAuto
  try { savedSort = JSON.parse(localStorage.getItem(SORT_STORAGE_KEY) || '') } catch { /* 默认排序 */ }
  try { savedColumns = JSON.parse(localStorage.getItem(HIDDEN_COLUMNS_KEY) || '') } catch { /* 默认列 */ }
  try { savedAuto = JSON.parse(localStorage.getItem(AUTO_REFRESH_KEY) || '') } catch { /* 默认轮询 */ }
  const sortKey = ref(SORTABLE.has(savedSort?.key) ? savedSort.key : 'priority')
  const sortOrder = ref(savedSort?.order === 'desc' ? 'desc' : 'asc')
  const hiddenKeys = ref(Array.isArray(savedColumns) ? savedColumns : [...DEFAULT_HIDDEN])
  autoEnabled.value = savedAuto?.enabled === true
  if (AUTO_REFRESH_INTERVALS.includes(Number(savedAuto?.interval_seconds))) autoInterval.value = Number(savedAuto.interval_seconds)

  const buildFilters = () => ({
    platform: fPlatform.value || undefined, status: fStatus.value || undefined,
    type: fType.value || undefined, group: fGroup.value || undefined,
    privacy_mode: fPrivacy.value || undefined, ws_probe: fWsProbe.value || undefined,
    search: fSearch.value.trim() || undefined, sort_by: sortKey.value, sort_order: sortOrder.value,
  })
  const query = () => ({ page: page.value, page_size: pageSize.value, ...buildFilters() })
  const apply = (data) => {
    rows.value = data.items || []; total.value = data.total || 0; err.value = ''; statsEpoch.value += 1
  }
  const load = async () => {
    loading.value = true
    autoFetching.value = false
    autoEtag.value = null
    await runLatest(lane, (signal) => request('/admin/accounts', { method: 'GET', query: query(), signal }), {
      apply,
      error: (e) => { err.value = e.message; rows.value = []; total.value = 0 },
      finish: () => { loading.value = false },
    })
  }
  const resetAutoCache = () => { autoEtag.value = null }
  const onFilter = () => { selectedIds.value = []; page.value = 1; load() }
  const onPage = (p, ps) => { page.value = p; if (ps) pageSize.value = ps; load() }
  const onSort = (key, order) => {
    sortKey.value = key; sortOrder.value = order
    localStorage.setItem(SORT_STORAGE_KEY, JSON.stringify({ key, order }))
    page.value = 1; load()
  }
  const toggleColumn = (key) => {
    hiddenKeys.value = hiddenKeys.value.includes(key) ? hiddenKeys.value.filter((k) => k !== key) : [...hiddenKeys.value, key]
    localStorage.setItem(HIDDEN_COLUMNS_KEY, JSON.stringify(hiddenKeys.value))
  }
  const saveAuto = () => localStorage.setItem(AUTO_REFRESH_KEY, JSON.stringify({ enabled: autoEnabled.value, interval_seconds: autoInterval.value }))
  const setAutoEnabled = (v) => { autoEnabled.value = v; saveAuto(); autoCountdown.value = v ? autoInterval.value : 0 }
  const setAutoIntervalSec = (s) => { autoInterval.value = s; saveAuto(); if (autoEnabled.value) autoCountdown.value = s }
  const enterSilent = () => { silentUntil.value = Date.now() + AUTO_REFRESH_SILENT_MS; autoCountdown.value = autoInterval.value }

  // 普通读取和 ETag 轮询共用查询代际；切筛选会取消旧轮询，旧总数/ETag不能写回新筛选。
  const refreshIncremental = async () => {
    if (autoFetching.value || loading.value) return
    autoFetching.value = true
    await runLatest(lane, (signal) => requestWithMeta('/admin/accounts', {
      method: 'GET', query: query(), signal, allowNotModified: true,
      headers: autoEtag.value ? { 'If-None-Match': autoEtag.value } : undefined,
    }), {
      apply: (res) => {
        if (res.headers.get('ETag')) autoEtag.value = res.headers.get('ETag')
        if (!res.notModified && res.data) {
          if (JSON.stringify(res.data.items || []) !== JSON.stringify(rows.value)) apply(res.data)
          else { total.value = res.data.total || 0; err.value = '' }
        }
      },
      finish: () => { autoFetching.value = false },
    })
  }
  let autoTimer
  const stop = () => {
    lane.cancel(); if (autoTimer) clearInterval(autoTimer)
    rows.value = []; total.value = 0; selectedIds.value = []; err.value = ''
    autoFetching.value = false; autoEtag.value = null; loading.value = false
  }
  const unsubscribe = subscribeSessionReset(stop)
  const autoTick = () => {
    if (!autoEnabled.value || document.hidden || loading.value || autoFetching.value || isAnyModalOpen.value) return
    if (Date.now() < silentUntil.value) { autoCountdown.value = Math.max(0, Math.ceil((silentUntil.value - Date.now()) / 1000)); return }
    if (autoCountdown.value <= 0) { autoCountdown.value = autoInterval.value; refreshIncremental(); return }
    autoCountdown.value -= 1
  }
  onMounted(() => { load(); autoTimer = setInterval(autoTick, 1000); if (autoEnabled.value) autoCountdown.value = autoInterval.value })
  onUnmounted(() => { unsubscribe(); stop(); lane.dispose() })
  return {
    loading, statsEpoch, rows, total, page, pageSize, fPlatform, fStatus, fSearch, fType, fGroup, fPrivacy, fWsProbe,
    selectedIds, selectedRows: computed(() => rows.value.filter((r) => selectedIds.value.includes(r.id))), err,
    buildFilters, sortKey, sortOrder, onSort, hiddenKeys, toggleColumn, load, onFilter, onPage,
    autoEnabled, autoInterval, autoCountdown, autoFetching, setAutoEnabled, setAutoIntervalSec, resetAutoCache, enterSilent,
    ALL_COLUMNS, AUTO_REFRESH_INTERVALS,
  }
}
