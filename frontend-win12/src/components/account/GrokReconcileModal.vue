<script setup>
/* Grok OAuth 对账弹窗（三期）——旧实现契约：frontend/src/components/admin/account/GrokOAuthReconcileDialog.vue
 * 接口：POST /admin/grok/oauth/reconcile { dry_run, apply, after_id, limit }
 *   dry_run 扫描分页循环（has_more → next_after_id，最多 100 页，limit 固定 500——
 *   后端对过小 limit 返回 Invalid request），保存每页 preview_token + account_ids；
 *   apply 时逐页传 { dry_run:false, apply:true, after_id, limit, preview_token, account_ids }。
 * 结果字段：scanned/actionable/would_block/would_refresh/blocked/refreshed/skipped/failed/partial
 *   items[{account_id, reason, action(block_account|refresh_credentials), outcome, owner_account_id?}] */
import { ref, computed, watch } from 'vue'
import { request } from '../../api'
import { message } from 'ant-design-vue'

const open = defineModel('open', { type: Boolean, default: false })
const emit = defineEmits(['repaired'])

const URL_BASE = '/admin/grok/oauth/reconcile'
const loading = ref(false)
const applying = ref(false)
const confirming = ref(false)
const error = ref('')
const summary = ref(null)
const previewPages = ref([]) // [{ afterId, previewToken, accountIds }]
const selected = ref(new Set())
const page = ref(1)
const PAGE_SIZE = 100

const REASONS = {
  missing_refresh_token: '缺少 refresh_token', missing_access_token: '缺少 access_token',
  missing_expiry: '缺少过期时间', invalid_expiry: '过期时间非法', near_expiry: '临近过期',
  missing_stable_identity: '缺少稳定身份标识', owner_mismatch: '归属不匹配',
  owner_changed: '归属已变更', duplicate_stable_identity: '稳定身份重复', refresh_failed: '刷新凭据失败',
}
const OUTCOMES = { planned: '待执行', applied: '已执行', skipped: '已跳过', failed: '失败', partial: '部分成功' }

const totalPages = computed(() => Math.max(1, Math.ceil((summary.value?.items?.length || 0) / PAGE_SIZE)))
const visibleItems = computed(() => {
  const items = summary.value?.items || []
  return items.slice((page.value - 1) * PAGE_SIZE, page.value * PAGE_SIZE)
})
const allSelected = computed(() => {
  const items = summary.value?.items || []
  return items.length > 0 && items.every((i) => selected.value.has(i.account_id))
})
const selectedCount = computed(() => selected.value.size)

const emptySummary = (dryRun) => ({
  dry_run: dryRun, scanned: 0, actionable: 0, would_block: 0, would_refresh: 0,
  blocked: 0, refreshed: 0, skipped: 0, failed: 0, partial: 0, items: [],
})

const mergePage = (target, pg) => {
  for (const k of ['scanned', 'actionable', 'would_block', 'would_refresh', 'blocked', 'refreshed', 'skipped', 'failed', 'partial']) target[k] += pg[k] || 0
  target.items.push(...(pg.items || []))
}

const scan = async () => {
  loading.value = true; applying.value = false; confirming.value = false; page.value = 1; error.value = ''
  try {
    const agg = emptySummary(true)
    const pages = []
    let afterId = 0
    for (let i = 0; i < 100; i++) {
      const pg = await request(URL_BASE, {
        method: 'POST',
        body: { dry_run: true, apply: false, after_id: afterId, limit: 500 },
      })
      if (!pg.preview_token) throw new Error('预览令牌缺失（preview_token 为空）')
      pages.push({ afterId, previewToken: pg.preview_token, accountIds: (pg.items || []).map((x) => x.account_id) })
      mergePage(agg, pg)
      if (!pg.has_more) break
      if (!pg.next_after_id || pg.next_after_id <= afterId) throw new Error('分页游标异常（next_after_id 未前进）')
      afterId = pg.next_after_id
    }
    previewPages.value = pages
    summary.value = agg
    selected.value = new Set(agg.items.map((x) => x.account_id)) // 默认全选（旧版行为）
  } catch (e) {
    summary.value = null
    error.value = e.message
  } finally {
    loading.value = false
  }
}

const toggleAll = () => {
  selected.value = allSelected.value ? new Set() : new Set((summary.value?.items || []).map((x) => x.account_id))
}
const toggleOne = (id) => {
  const s = new Set(selected.value)
  s.has(id) ? s.delete(id) : s.add(id)
  selected.value = s
}

const applyRepair = async () => {
  loading.value = true; applying.value = true; confirming.value = false; error.value = ''
  try {
    const agg = emptySummary(false)
    for (const pg of previewPages.value) {
      const ids = pg.accountIds.filter((id) => selected.value.has(id))
      if (!ids.length) continue
      const r = await request(URL_BASE, {
        method: 'POST',
        body: { dry_run: false, apply: true, after_id: pg.afterId, limit: 500, preview_token: pg.previewToken, account_ids: ids },
        timeoutHint: '120s',
      })
      mergePage(agg, r)
    }
    summary.value = agg
    page.value = 1
    message.success(`对账修复完成：刷新 ${agg.refreshed}，阻断 ${agg.blocked}`)
    emit('repaired')
  } catch (e) {
    error.value = e.message
  } finally {
    applying.value = false
    loading.value = false
  }
}

const exportResults = () => {
  if (!summary.value) return
  const payload = JSON.stringify({
    exported_at: new Date().toISOString(),
    summary: summary.value,
    selected_account_ids: [...selected.value].sort((a, b) => a - b),
  }, null, 2)
  const blob = new Blob([payload], { type: 'application/json;charset=utf-8' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = `grok-oauth-reconcile-${Date.now()}.json`
  a.click()
  URL.revokeObjectURL(a.href)
}

watch(open, (o) => { if (o) scan() })
</script>

<template>
  <a-modal v-model:open="open" title="Grok OAuth 对账" width="760px" :footer="null">
    <a-alert type="info" show-icon message="只读扫描不改动任何账号；修复只对勾选账号执行（刷新凭据或阻断异常账号）。" style="margin-bottom: 12px" />
    <a-spin :spinning="loading" :tip="applying ? '正在修复…' : '扫描中…'">
      <a-alert v-if="error" type="error" show-icon :message="`对账失败：${error}`" />
      <template v-else-if="summary">
        <div class="cards">
          <div class="card"><span>扫描账号</span><b>{{ summary.scanned }}</b></div>
          <div class="card warn"><span>需处理</span><b>{{ summary.actionable }}</b></div>
          <div class="card info"><span>{{ summary.dry_run ? '拟刷新' : '已刷新' }}</span><b>{{ summary.dry_run ? summary.would_refresh : summary.refreshed }}</b></div>
          <div class="card bad"><span>{{ summary.dry_run ? '拟阻断' : '已阻断' }}</span><b>{{ summary.dry_run ? summary.would_block : summary.blocked }}</b></div>
        </div>
        <div v-if="visibleItems.length" class="tbl">
          <div v-for="it in visibleItems" :key="`${it.account_id}-${it.reason}`" class="tr">
            <a-checkbox :checked="selected.has(it.account_id)" @change="() => toggleOne(it.account_id)" />
            <span class="mono">#{{ it.account_id }}</span>
            <span class="reason">{{ REASONS[it.reason] || it.reason }}
              <i v-if="it.owner_account_id" class="dim">（归属 #{{ it.owner_account_id }}）</i>
            </span>
            <a-tag :color="it.action === 'block_account' ? 'red' : 'blue'">{{ it.action === 'block_account' ? '阻断账号' : '刷新凭据' }}</a-tag>
            <span class="dim">{{ OUTCOMES[it.outcome] || it.outcome }}</span>
          </div>
          <div v-if="totalPages > 1" class="pager">
            <a-button size="small" :disabled="page === 1" @click="page -= 1">上一页</a-button>
            <span class="dim">{{ page }} / {{ totalPages }}</span>
            <a-button size="small" :disabled="page === totalPages" @click="page += 1">下一页</a-button>
          </div>
        </div>
        <div v-else class="healthy">所有 Grok OAuth 账号状态健康，无需处理</div>

        <div v-if="confirming" class="confirm">
          <b>确认执行修复？</b>
          <div class="dim">将对选中的 {{ selectedCount }} 个账号执行刷新/阻断操作，此操作不可撤销。</div>
        </div>

        <div class="foot">
          <a-button :disabled="loading" @click="open = false">关闭</a-button>
          <a-button v-if="summary.items.length" :disabled="loading" @click="exportResults">导出结果</a-button>
          <a-button v-if="summary.dry_run && summary.items.length" :disabled="loading" @click="toggleAll">
            {{ allSelected ? '取消全选' : '全选' }}
          </a-button>
          <a-button v-if="!confirming" :disabled="loading" @click="scan">重新扫描</a-button>
          <a-button v-if="summary.dry_run && summary.actionable > 0 && !confirming" type="primary" :disabled="loading || selectedCount === 0" @click="confirming = true">
            执行修复
          </a-button>
          <template v-if="confirming">
            <a-button :disabled="loading" @click="confirming = false">取消</a-button>
            <a-button danger :disabled="loading" @click="applyRepair">确认修复</a-button>
          </template>
        </div>
      </template>
    </a-spin>
  </a-modal>
</template>

<style scoped>
.cards { display: grid; grid-template-columns: repeat(4, 1fr); gap: 8px; margin-bottom: 12px; }
.card { padding: 10px 12px; border-radius: 8px; background: var(--card); display: flex; flex-direction: column; gap: 4px; }
.card span { color: var(--text2); font-size: 12px; }
.card b { font-size: 20px; font-weight: 500; font-family: Consolas, monospace; }
.card.warn b { color: #a86307; } .card.info b { color: #1668dc; } .card.bad b { color: #cf3030; }
.tbl { border: 1px solid var(--hr); border-radius: 8px; max-height: 320px; overflow: auto; }
.tr { display: flex; align-items: center; gap: 10px; padding: 6px 10px; border-bottom: 1px solid var(--hr); font-size: 12px; }
.tr:last-child { border-bottom: none; }
.reason { flex: 1; }
.pager { display: flex; align-items: center; justify-content: center; gap: 12px; padding: 8px; }
.healthy { padding: 20px; text-align: center; color: #389e0d; font-size: 13px; background: rgba(82, 196, 26, 0.08); border-radius: 8px; }
.confirm { margin-top: 12px; padding: 10px 12px; border-radius: 8px; background: rgba(212, 140, 20, 0.1); }
.foot { display: flex; justify-content: flex-end; gap: 8px; margin-top: 14px; flex-wrap: wrap; }
.mono { font-family: Consolas, monospace; }
.dim { color: var(--text2); font-size: 11px; }
</style>
