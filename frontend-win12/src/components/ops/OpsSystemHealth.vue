<script setup>
/* 系统健康 6 小卡：CPU / 内存 / 数据库 / Redis / Goroutines / Jobs
 * 契约来源：旧 OpsDashboardHeader.vue “System health (secondary)” + system_metrics 字段：
 * - CPU：cpu_usage_percent（≥95 红 / ≥80 黄）
 * - 内存：memory_usage_percent（≥95 红 / ≥85 黄）
 * - DB：db_ok + 连接池占用 (active+idle)/db_max_open_conns（≥90 红 / ≥70 黄）
 * - Redis：redis_ok + redis_conn_total/redis_pool_size（≥90 红 / ≥70 黄）
 * - Goroutines：goroutine_count（≥15000 critical / ≥8000 warning）
 * - Jobs：job_heartbeats（last_error_at 晚于 last_success_at 计为告警） */
import { computed } from 'vue'
import { WarningOutlined } from '@ant-design/icons-vue'

const props = defineProps({
  overview: { type: Object, default: null },
})
const sm = computed(() => props.overview?.system_metrics || null)
const jobs = computed(() => props.overview?.job_heartbeats || [])

const GREEN = '#16a34a'
const YELLOW = '#d97706'
const RED = '#dc2626'
const GRAY = 'var(--text2)'

const cpu = computed(() => {
  const v = sm.value?.cpu_usage_percent
  if (typeof v !== 'number') return { main: '-', color: GRAY, sub: '暂无数据' }
  const color = v >= 95 ? RED : v >= 80 ? YELLOW : GREEN
  return { main: `${v.toFixed(1)}%`, color, sub: v >= 80 ? '偏高' : '正常' }
})
const mem = computed(() => {
  const v = sm.value?.memory_usage_percent
  if (typeof v !== 'number') return { main: '-', color: GRAY, sub: '暂无数据' }
  const color = v >= 95 ? RED : v >= 85 ? YELLOW : GREEN
  return { main: `${v.toFixed(1)}%`, color, sub: sm.value?.memory_used_mb != null ? `${sm.value.memory_used_mb} MB 已用` : '' }
})
const db = computed(() => {
  const s = sm.value
  if (!s) return { main: '-', color: GRAY, sub: '暂无数据' }
  if (s.db_ok === false) return { main: 'FAIL', color: RED, sub: '连接异常' }
  const open = (s.db_conn_active ?? null), idle = (s.db_conn_idle ?? null), max = s.db_max_open_conns
  if (open != null && idle != null && typeof max === 'number' && max > 0) {
    const p = Math.min(100, ((open + idle) / max) * 100)
    const color = p >= 90 ? RED : p >= 70 ? YELLOW : GREEN
    return { main: `${p.toFixed(0)}%`, color, sub: `连接 ${open + idle}/${max}` }
  }
  return { main: '正常', color: GREEN, sub: '连接正常' }
})
const redis = computed(() => {
  const s = sm.value
  if (!s) return { main: '-', color: GRAY, sub: '暂无数据' }
  if (s.redis_ok === false) return { main: 'FAIL', color: RED, sub: '连接异常' }
  const total = s.redis_conn_total, pool = s.redis_pool_size
  if (typeof total === 'number' && typeof pool === 'number' && pool > 0) {
    const p = Math.min(100, (total / pool) * 100)
    const color = p >= 90 ? RED : p >= 70 ? YELLOW : GREEN
    return { main: `${p.toFixed(0)}%`, color, sub: `连接 ${total}/${pool}` }
  }
  return { main: '正常', color: GREEN, sub: '连接正常' }
})
const gor = computed(() => {
  const n = sm.value?.goroutine_count
  if (typeof n !== 'number') return { main: '-', color: GRAY, sub: '暂无数据' }
  const color = n >= 15000 ? RED : n >= 8000 ? YELLOW : GREEN
  return { main: n.toLocaleString('zh-CN'), color, sub: n >= 8000 ? '数量偏高' : '正常' }
})
const jobsStatus = computed(() => {
  const list = jobs.value
  if (!list.length) return { main: '-', color: GRAY, sub: '暂无心跳' }
  const warn = list.filter((hb) => hb && hb.last_error_at && (!hb.last_success_at || hb.last_error_at > hb.last_success_at))
  return warn.length
    ? { main: `${warn.length} 异常`, color: YELLOW, sub: `${list.length} 个后台任务` }
    : { main: '正常', color: GREEN, sub: `${list.length} 个后台任务` }
})

const CARDS = computed(() => [
  { key: 'cpu', label: 'CPU', ...cpu.value },
  { key: 'mem', label: '内存', ...mem.value },
  { key: 'db', label: '数据库', ...db.value },
  { key: 'redis', label: 'Redis', ...redis.value },
  { key: 'gor', label: 'Goroutines', ...gor.value },
  { key: 'jobs', label: '后台任务', ...jobsStatus.value },
])
</script>

<template>
  <div class="sgrid">
    <div v-for="c in CARDS" :key="c.key" class="scard">
      <div class="sk">
        {{ c.label }}
        <a-popover v-if="c.key === 'jobs' && jobs.length" trigger="click">
          <template #content>
            <div class="jb">
              <div v-for="hb in jobs" :key="hb.job_name" class="jb-row">
                <div class="jb-name">
                  {{ hb.job_name }}
                  <a-tag v-if="hb.last_error_at && (!hb.last_success_at || hb.last_error_at > hb.last_success_at)" color="red" style="margin-left: 4px">异常</a-tag>
                </div>
                <div class="jb-sub">最近成功：{{ (hb.last_success_at || '-').replace('T', ' ').slice(0, 19) }}</div>
                <div v-if="hb.last_error" class="jb-err">{{ hb.last_error }}</div>
              </div>
            </div>
          </template>
          <WarningOutlined v-if="jobsStatus.color === YELLOW" style="color: #d97706; font-size: 11px; margin-left: 2px" />
        </a-popover>
      </div>
      <div class="sv" :style="{ color: c.color }">{{ c.main }}</div>
      <div class="ss">{{ c.sub }}</div>
    </div>
  </div>
</template>

<style scoped>
/* 6 张健康卡固定 3×2 填满（旧 auto-fit 在宽容器下排 4+2，第二行空两格） */
.sgrid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 12px; }
@media (max-width: 760px) { .sgrid { grid-template-columns: 1fr; } }
.scard { padding: 10px 14px; border-radius: 10px; background: var(--card); border: 1px solid var(--hr); }
.sk { font-size: 12px; color: var(--text2); }
.sv { margin-top: 3px; font-size: 18px; font-weight: 600; }
.ss { margin-top: 2px; font-size: 11px; color: var(--text2); }
.jb { max-width: 380px; max-height: 320px; overflow: auto; }
.jb-row { padding: 6px 0; border-top: 1px solid var(--hr); }
.jb-row:first-child { border-top: none; }
.jb-name { font-size: 12px; font-weight: 500; }
.jb-sub { font-size: 11px; color: var(--text2); margin-top: 2px; }
.jb-err { font-size: 11px; color: #dc2626; margin-top: 2px; word-break: break-all; }
</style>
