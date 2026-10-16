<script setup>
/* 初始化向导 v1（对照旧 SetupWizardView.vue + api/setup.ts）
 * 接口（无鉴权、不走 /api/v1）：GET /setup/status、POST /setup/install
 * 四步：数据库 → Redis → 管理员账号 → 服务器；install 成功后网关自动重启
 * 注意：网关发行版推荐用 config.yaml 启动；本向导等价保留旧前端能力 */
import { ref, onMounted } from 'vue'
import { message } from 'ant-design-vue'
import { CheckCircleFilled } from '@ant-design/icons-vue'

const loading = ref(true)
const installing = ref(false)
const step = ref(0)
const status = ref(null)
const done = ref(false)
const doneMsg = ref('')

const db = ref({ host: '127.0.0.1', port: 5432, user: 'postgres', password: '', dbname: 'sub2api', sslmode: 'disable' })
const rd = ref({ host: '127.0.0.1', port: 6379, password: '', db: 0, enable_tls: false })
const admin = ref({ email: '', password: '' })
const server = ref({ host: '0.0.0.0', port: 8081, mode: 'release' })

// setup 接口挂在根路径、无 Bearer；与业务 request 层（/api/v1 + token）隔离
const setupFetch = async (path, opts = {}) => {
  const res = await fetch(path, {
    method: opts.method || 'GET',
    headers: { 'Content-Type': 'application/json' },
    body: opts.body ? JSON.stringify(opts.body) : undefined,
  })
  const payload = await res.json()
  if (payload && typeof payload === 'object' && 'code' in payload) {
    if (payload.code !== 0) throw new Error(payload.message || '接口错误')
    return payload.data
  }
  return payload
}

const steps = ['数据库', 'Redis 缓存', '管理员账号', '服务器']

const next = () => { step.value = Math.min(step.value + 1, 3) }
const prev = () => { step.value = Math.max(step.value - 1, 0) }

const valid = () => {
  if (step.value === 0 && (!db.value.host || !db.value.dbname || !db.value.user)) { message.error('请填写数据库连接信息'); return false }
  if (step.value === 1 && (!rd.value.host || !rd.value.port)) { message.error('请填写 Redis 连接信息'); return false }
  if (step.value === 2) {
    if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(admin.value.email || '')) { message.error('请填写合法的管理员邮箱'); return false }
    if ((admin.value.password || '').length < 8) { message.error('管理员密码至少 8 位'); return false }
  }
  return true
}

const install = async () => {
  if (!valid()) return
  installing.value = true
  try {
    const res = await setupFetch('/setup/install', {
      method: 'POST',
      body: { database: db.value, redis: rd.value, admin: admin.value, server: server.value },
    })
    done.value = true
    doneMsg.value = res?.message || '安装完成'
    message.success(`${doneMsg.value}${res?.restart ? '，网关正在重启…' : ''}`)
  } catch (e) {
    message.error(`安装失败：${e.message}`)
  } finally {
    installing.value = false
  }
}

onMounted(async () => {
  loading.value = true
  try {
    status.value = await setupFetch('/setup/status')
  } catch { status.value = null }
  finally { loading.value = false }
})
</script>

<template>
  <div class="setup">
    <a-spin :spinning="loading">
      <template v-if="done">
        <div class="done">
          <div class="done-icon"><CheckCircleFilled /></div>
          <div class="done-title">安装完成</div>
          <div class="done-msg">{{ doneMsg }}。网关重启后请使用新管理员账号重新登录。</div>
        </div>
      </template>
      <template v-else>
        <a-alert
          v-if="status && status.needs_setup === false"
          message="系统看起来已完成初始化；再次安装可能覆盖现有配置，请谨慎操作"
          type="warning" show-icon class="warn"
        />
        <!-- 步骤条 -->
        <div class="steps">
          <template v-for="(s, i) in steps" :key="s">
            <div class="stp" :class="{ on: i === step, past: i < step }">
              <div class="dot">{{ i < step ? '✓' : i + 1 }}</div>
              <div class="lbl">{{ s }}</div>
            </div>
            <div v-if="i < steps.length - 1" class="line" :class="{ past: i < step }" />
          </template>
        </div>
        <!-- 步骤表单 -->
        <div class="pane">
          <div v-if="step === 0" class="grid">
            <div class="row"><span>主机</span><a-input v-model:value="db.host" /></div>
            <div class="row"><span>端口</span><a-input-number v-model:value="db.port" :min="1" :max="65535" style="width: 100%" /></div>
            <div class="row"><span>用户</span><a-input v-model:value="db.user" /></div>
            <div class="row"><span>密码</span><a-input-password v-model:value="db.password" /></div>
            <div class="row"><span>数据库名</span><a-input v-model:value="db.dbname" /></div>
            <div class="row"><span>SSL 模式</span>
              <a-select v-model:value="db.sslmode" :options="['disable', 'require', 'verify-ca', 'verify-full'].map((v) => ({ value: v, label: v }))" />
            </div>
          </div>
          <div v-else-if="step === 1" class="grid">
            <div class="row"><span>主机</span><a-input v-model:value="rd.host" /></div>
            <div class="row"><span>端口</span><a-input-number v-model:value="rd.port" :min="1" :max="65535" style="width: 100%" /></div>
            <div class="row"><span>密码</span><a-input-password v-model:value="rd.password" /></div>
            <div class="row"><span>数据库编号</span><a-input-number v-model:value="rd.db" :min="0" :max="15" style="width: 100%" /></div>
            <div class="row"><span>TLS</span><a-switch v-model:checked="rd.enable_tls" /></div>
          </div>
          <div v-else-if="step === 2" class="grid">
            <div class="row"><span>管理员邮箱</span><a-input v-model:value="admin.email" placeholder="admin@example.com" /></div>
            <div class="row"><span>管理员密码（≥8 位）</span><a-input-password v-model:value="admin.password" /></div>
            <a-alert message="发行二进制不内置固定账号；此处创建首个管理员" type="info" show-icon />
          </div>
          <div v-else class="grid">
            <div class="row"><span>监听地址</span><a-input v-model:value="server.host" /></div>
            <div class="row"><span>监听端口</span><a-input-number v-model:value="server.port" :min="1" :max="65535" style="width: 100%" /></div>
            <div class="row"><span>运行模式</span>
              <a-select v-model:value="server.mode" :options="[{ value: 'release', label: 'release（生产）' }, { value: 'debug', label: 'debug（调试）' }]" />
            </div>
          </div>
        </div>
        <div class="nav">
          <a-button v-if="step > 0" @click="prev">上一步</a-button>
          <a-button v-if="step < 3" type="primary" @click="() => { if (valid()) next() }">下一步</a-button>
          <a-button v-else type="primary" danger :loading="installing" @click="install">执行安装</a-button>
        </div>
      </template>
    </a-spin>
  </div>
</template>

<style scoped>
.setup { height: 100%; overflow-y: auto; }
.warn { margin-bottom: 12px; }
.steps { display: flex; align-items: center; justify-content: center; gap: 8px; margin: 6px 0 18px; }
.stp { display: flex; flex-direction: column; align-items: center; gap: 4px; min-width: 64px; }
.stp .dot {
  width: 30px; height: 30px; border-radius: 50%;
  display: flex; align-items: center; justify-content: center;
  background: var(--card); border: 1px solid var(--hr); font-size: 13px; color: var(--text2);
}
.stp.on .dot { background: var(--accent); color: #fff; border: 0; }
.stp.past .dot { color: var(--theme-2); border-color: var(--theme-2); }
.stp .lbl { font-size: 12px; color: var(--text2); }
.stp.on .lbl { color: var(--text); font-weight: 600; } /* 原引用未定义的 --text1（外壳层只有 --text/--text2），color 被整条丢弃 */
.line { width: 46px; height: 2px; background: var(--hr); border-radius: 1px; }
.line.past { background: var(--theme-2); }
.pane { max-width: 520px; margin: 0 auto; }
.grid { display: flex; flex-direction: column; gap: 10px; }
.row { display: grid; grid-template-columns: 130px 1fr; align-items: center; gap: 10px; font-size: 13px; }
.nav { display: flex; justify-content: center; gap: 10px; margin-top: 18px; }
.done { text-align: center; padding: 60px 0; }
.done-icon { font-size: 44px; }
.done-icon .anticon { color: var(--theme-2); }
.done-title { font-size: 18px; font-weight: 600; margin: 10px 0 6px; }
.done-msg { font-size: 13px; color: var(--text2); }
@media (max-width: 640px) { .row { grid-template-columns: 1fr; } .line { width: 18px; } }
</style>




