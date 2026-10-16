<script setup>
/* ============================================================
 * 系统版本卡（供「系统配置」页接入，自取数：onMounted fetch）
 * 接口契约：internal/handler/admin/system_handler.go + routes/admin.go
 * registerSystemRoutes（standard 模式注册；gateway-lite 模式 404 → 整卡降级）：
 * - GET  /admin/system/version       当前版本（内部走 CheckUpdate 缓存）
 * - GET  /admin/system/check-updates 检查更新（?force=true 强制拉取 GitHub Release）
 * - POST /admin/system/update        立即更新（下载并替换二进制，幂等键保护，无 step-up）
 * - POST /admin/system/rollback      回滚上一版本（幂等键保护，无 step-up）
 * 说明：POST /admin/system/restart（重启服务）handler 已实现，但属于运维域操作，
 *       不在版本卡范围，由主会话决定接入位置。
 * 所有请求 404 时显示「当前运行模式不提供此功能（standard 模式专属）」，不弹错误。
 * ============================================================ */
import { onMounted, ref } from 'vue'
import { message } from 'ant-design-vue'
import {
  ArrowUpOutlined,
  CloudDownloadOutlined,
  InfoCircleOutlined,
  ReloadOutlined,
  RollbackOutlined,
  SearchOutlined,
} from '@ant-design/icons-vue'
import { NOT_FOUND_TIP, checkUpdates, getVersion, performUpdate, rollback } from './systemApi'

const notFound = ref(false)
const loading = ref(false)
const loadErr = ref('')
const version = ref('')

/* 检查更新结果（UpdateInfo：current_version / latest_version / has_update /
 * release_info{name,body,published_at,html_url} / cached / warning / build_type） */
const upd = ref(null)
const checked = ref(false)
const checking = ref(false)
const updating = ref(false)
const rollingBack = ref(false)

const fmtDate = (iso) => (iso ? new Date(iso).toLocaleString() : '-')
const buildTypeText = (t) => (t === 'release' ? '发行构建（release）' : t === 'source' ? '源码构建（source）' : t || '-')

const load = async () => {
  loading.value = true
  try {
    version.value = (await getVersion()).version
    loadErr.value = ''
  } catch (e) {
    if (e.notFound) { notFound.value = true; return } // 整卡降级，不再提示
    loadErr.value = e.message
  } finally {
    loading.value = false
  }
}

/* 检查更新：force=true 绕过缓存直连 GitHub Release API */
const onCheck = async () => {
  checking.value = true
  try {
    upd.value = await checkUpdates(true)
    checked.value = true
  } catch (e) {
    if (e.notFound) message.info(NOT_FOUND_TIP)
    else message.error(`检查更新失败：${e.message}`)
  } finally {
    checking.value = false
  }
}

/* 立即更新：下载并替换程序二进制（耗时操作），完成后需重启服务生效 */
const onUpdate = async () => {
  updating.value = true
  try {
    const res = await performUpdate()
    if (res?.already_up_to_date) message.info('当前已是最新版本')
    else message.success(`${res?.message || '更新完成'}，请重启服务后生效`)
    await load() // 运行中的进程仍是旧版本，重启后 version 才会变化
  } catch (e) {
    if (e.notFound) message.info(NOT_FOUND_TIP)
    else message.error(`更新失败：${e.message}`)
  } finally {
    updating.value = false
  }
}

/* 回滚到上一版本（危险操作：popconfirm 红色警示二次确认） */
const onRollback = async () => {
  rollingBack.value = true
  try {
    const res = await rollback()
    message.success(`${res?.message || '回滚完成'}，请重启服务后生效`)
    await load()
  } catch (e) {
    if (e.notFound) message.info(NOT_FOUND_TIP)
    else message.error(`回滚失败：${e.message}`)
  } finally {
    rollingBack.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="card version-card">
    <!-- 运行模式降级：gateway-lite 未注册系统路由（404） -->
    <div v-if="notFound" class="na">
      <InfoCircleOutlined class="na-icon" />
      <div class="na-text">{{ NOT_FOUND_TIP }}</div>
      <div class="na-sub">版本与更新管理由 standard 模式提供，当前运行模式未注册相关接口。</div>
    </div>

    <template v-else>
      <div class="head">
        <div>
          <div class="card-title">系统版本</div>
          <div class="card-sub">查看当前版本、检查上游发行版并执行更新或回滚。</div>
        </div>
        <a-button size="small" :loading="checking" @click="onCheck">
          <template #icon><SearchOutlined /></template>
          检查更新
        </a-button>
      </div>

      <div v-if="loadErr" class="hint">
        版本信息读取失败：{{ loadErr }} <a class="retry" @click="load()">重试</a>
      </div>

      <div class="form">
        <div class="frow">
          <label>当前版本</label>
          <div class="fmain">
            <a-spin :spinning="loading" size="small">
              <span class="static mono">{{ version || '…' }}</span>
            </a-spin>
            <span style="flex: 1" />
            <a-button size="small" :loading="loading" @click="load">
              <template #icon><ReloadOutlined /></template>
              刷新
            </a-button>
          </div>
        </div>
      </div>

      <!-- 检查更新结果 -->
      <template v-if="checked && upd">
        <div class="form result">
          <div class="frow">
            <label>最新版本</label>
            <span class="static mono">{{ upd.latest_version || '-' }}</span>
          </div>
          <div class="frow">
            <label>构建类型</label>
            <span class="static">{{ buildTypeText(upd.build_type) }}</span>
          </div>
          <div class="frow">
            <label>发布信息</label>
            <div class="fmain">
              <span class="static">
                {{ upd.release_info?.name || upd.latest_version || '-' }}
                <span v-if="upd.release_info?.published_at" class="dim">· {{ fmtDate(upd.release_info.published_at) }}</span>
              </span>
              <a
                v-if="upd.release_info?.html_url" class="retry" :href="upd.release_info.html_url"
                target="_blank" rel="noopener"
              >查看发布页</a>
            </div>
          </div>
        </div>

        <a-alert
          v-if="upd.warning" type="warning" show-icon :message="upd.warning"
          style="margin-top: 8px"
        />
        <a-alert
          v-if="upd.has_update" type="info" show-icon
          :message="`发现新版本 ${upd.latest_version || ''}`"
          description="更新会下载并替换程序二进制，完成后需要重启服务生效。"
          style="margin-top: 8px"
        >
          <template #action>
            <!-- 立即更新：确认后执行（幂等键保护；无可用更新时后端返回 already_up_to_date） -->
            <a-popconfirm
              title="确认立即更新？" ok-text="确认更新" cancel-text="取消"
              description="将下载并替换当前程序二进制，完成后需要重启服务。"
              @confirm="onUpdate"
            >
              <a-button size="small" type="primary" :loading="updating">
                <template #icon><ArrowUpOutlined /></template>
                立即更新
              </a-button>
            </a-popconfirm>
          </template>
        </a-alert>
        <a-alert v-else type="success" show-icon message="已是最新版本" style="margin-top: 8px" />
      </template>

      <div class="hr-line" />

      <!-- 危险操作：回滚（红色警示二次确认） -->
      <div class="maint">
        <div>
          <div class="mt-title">版本回滚</div>
          <div class="mt-desc">回滚到上一个版本并替换当前二进制，通常在更新后出现异常时使用。</div>
        </div>
        <a-popconfirm
          title="危险操作：确认回滚？" ok-text="确认回滚" cancel-text="取消"
          :ok-button-props="{ danger: true }" @confirm="onRollback"
        >
          <template #description>
            当前程序将被上一版本替换，操作不可撤销，完成后需要重启服务。
          </template>
          <a-button danger :loading="rollingBack">
            <template #icon><RollbackOutlined /></template>
            回滚上一版本
          </a-button>
        </a-popconfirm>
      </div>
    </template>
  </div>
</template>

<style scoped>
@import './settings.css';

/* 任务规范：区块标题 500（settings.css 默认 600，这里按 Win12 卡片规范覆盖） */
.card-title { font-weight: 500; }
.card-sub { color: var(--text2); }

.head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.hr-line { height: 1px; background: var(--hr); opacity: 0.55; margin: 14px 0; }
.retry { margin-left: 6px; cursor: pointer; }
.dim { font-size: 11.5px; color: var(--text2); }

/* 检查更新结果与维护区 */
.result { margin-top: 10px; }
.maint { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.mt-title { font-size: 13px; font-weight: 500; }
.mt-desc { font-size: 12px; color: var(--text2); margin-top: 2px; line-height: 1.6; }

/* 运行模式降级提示 */
.na { padding: 34px 16px; text-align: center; }
.na-icon { font-size: 28px; color: var(--text2); }
.na-text { margin-top: 10px; font-size: 13px; }
.na-sub { margin-top: 4px; font-size: 12px; color: var(--text2); }

/* 手机端：标题行与维护行纵向堆叠（表单单列由 settings.css 媒体查询处理） */
@media (max-width: 760px) {
  .head, .maint { flex-direction: column; align-items: flex-start; }
}
</style>
