<script setup>
/* TLS 指纹配置弹窗（三期）——旧实现契约：frontend/src/components/admin/TLSFingerprintProfilesModal.vue
 * + frontend/src/api/admin/tlsFingerprintProfile.ts
 * 接口：GET/POST /admin/tls-fingerprint-profiles，PUT/DELETE /admin/tls-fingerprint-profiles/:id
 * 字段：name/description/enable_grease/cipher_suites/curves/point_formats/
 *       signature_algorithms/alpn_protocols/supported_versions/key_share_groups/psk_modes/extensions
 * 数值字段支持 0x 十六进制或十进制逗号分隔输入（旧 parseNumericArray），
 * cipher_suites/signature_algorithms/supported_versions/extensions 显示为 0x 四位十六进制（旧 formatHex）。
 * 保留旧版 YAML 粘贴解析（tls-fingerprint-web 输出的扁平 key: value 结构）。 */
import { ref, reactive, watch } from 'vue'
import { confirmAction } from '../../utils/confirmAction'
import { request } from '../../api'
import { message } from 'ant-design-vue'

const open = defineModel('open', { type: Boolean, default: false })

const URL_BASE = '/admin/tls-fingerprint-profiles'
const profiles = ref([])
const loading = ref(false)
const showForm = ref(false)
const editingId = ref(null)
const saving = ref(false)

// 字段元数据：input=hex 显示 0xxxxx；plain=十进制；str=字符串数组
const NUM_FIELDS = [
  { key: 'cipher_suites', label: 'Cipher Suites', fmt: 'hex' },
  { key: 'curves', label: 'Curves', fmt: 'plain' },
  { key: 'point_formats', label: 'Point Formats', fmt: 'plain' },
  { key: 'signature_algorithms', label: 'Signature Algorithms', fmt: 'hex' },
  { key: 'supported_versions', label: 'Supported Versions', fmt: 'hex' },
  { key: 'key_share_groups', label: 'Key Share Groups', fmt: 'plain' },
  { key: 'psk_modes', label: 'PSK Modes', fmt: 'plain' },
  { key: 'extensions', label: 'Extensions', fmt: 'hex' },
]
const form = reactive({ name: '', description: '', enable_grease: true, alpn_protocols: '' })
const fieldInputs = reactive(Object.fromEntries(NUM_FIELDS.map((f) => [f.key, ''])))
const yamlText = ref('')

const load = async () => {
  loading.value = true
  try { profiles.value = (await request(URL_BASE, { method: 'GET' })) || [] }
  catch (e) { message.error(`配置读取失败：${e.message}`) }
  finally { loading.value = false }
}
watch(open, (o) => { if (o) load() })

const toHex = (n) => '0x' + Number(n).toString(16).padStart(4, '0')
const fmtField = (f, arr) => (f.fmt === 'hex' ? (arr || []).map(toHex) : (arr || []).map(String)).join(', ')

// 旧 parseNumericArray：支持 0x 前缀与十进制
const parseNums = (input) => input.split(',').map((s) => s.trim()).filter(Boolean)
  .map((s) => (/^0x/i.test(s) ? parseInt(s, 16) : parseInt(s, 10))).filter((n) => !Number.isNaN(n))
const parseStrs = (input) => input.split(',').map((s) => s.trim()).filter(Boolean)

const resetForm = () => {
  form.name = ''; form.description = ''; form.enable_grease = true; form.alpn_protocols = ''
  Object.keys(fieldInputs).forEach((k) => { fieldInputs[k] = '' })
  yamlText.value = ''
}
const startCreate = () => { editingId.value = null; resetForm(); showForm.value = true }
const startEdit = (p) => {
  editingId.value = p.id
  form.name = p.name; form.description = p.description || ''; form.enable_grease = p.enable_grease
  form.alpn_protocols = (p.alpn_protocols || []).join(', ')
  for (const f of NUM_FIELDS) fieldInputs[f.key] = fmtField(f, p[f.key])
  yamlText.value = ''
  showForm.value = true
}

// YAML 粘贴解析（旧 parseYamlInput：扁平 key: value，数组 [1,2,3] / ["h2","http/1.1"]）
const parseYaml = () => {
  let foundName = false
  for (const raw of yamlText.value.split('\n')) {
    const line = raw.trim()
    if (!line || line.startsWith('#')) continue
    const m = line.match(/^(\w+):\s*(.+)$/)
    if (!m) continue
    const [, key, value] = m
    const unquoted = value.trim().replace(/^["']|["']$/g, '')
    if (key === 'name' && unquoted) { form.name = unquoted; foundName = true }
    else if (key === 'enable_grease') form.enable_grease = value.trim() === 'true'
    else if (key === 'alpn_protocols') {
      const arr = value.trim().match(/^\[(.*)?\]$/)
      if (arr) fieldInputs.alpn_protocols = arr[1].split(',').map((s) => s.trim().replace(/^["']|["']$/g, '')).filter(Boolean).join(', ')
    } else {
      const f = NUM_FIELDS.find((x) => x.key === key)
      const arr = value.trim().match(/^\[(.*)?\]$/)
      if (f && arr) fieldInputs[f.key] = arr[1].split(',').map((s) => s.trim()).filter(Boolean).join(', ')
    }
  }
  if (foundName) message.success('YAML 解析成功（请核对各字段）')
  else message.error('未能解析出 name 字段，请检查 YAML 格式')
}

const submit = async () => {
  if (!form.name.trim()) { message.error('配置名称必填'); return }
  saving.value = true
  try {
    const body = {
      name: form.name.trim(),
      description: form.description.trim() || null,
      enable_grease: form.enable_grease,
      alpn_protocols: parseStrs(form.alpn_protocols),
    }
    for (const f of NUM_FIELDS) body[f.key] = parseNums(fieldInputs[f.key])
    if (editingId.value) await request(`${URL_BASE}/${editingId.value}`, { method: 'PUT', body })
    else await request(URL_BASE, { method: 'POST', body })
    message.success(editingId.value ? '配置已更新' : '配置已创建')
    showForm.value = false
    await load()
  } catch (e) {
    message.error(`保存失败：${e.message}`)
  } finally {
    saving.value = false
  }
}

const remove = async (p) => {
  if (!(await confirmAction({ title: '删除确认', content: `确认删除 TLS 指纹配置「${p.name}」？`, okText: '删除', danger: true }))) return
  try {
    await request(`${URL_BASE}/${p.id}`, { method: 'DELETE' })
    message.success('配置已删除')
    await load()
  } catch (e) { message.error(`删除失败：${e.message}`) }
}
</script>

<template>
  <a-modal v-model:open="open" title="TLS 指纹配置" width="720px" :footer="null">
    <div class="head">
      <span class="dim">为 OAuth 账号出站连接定制 TLS ClientHello 指纹（JA3），降低上游风控识别。</span>
      <a-button type="primary" size="small" @click="startCreate">新建配置</a-button>
    </div>
    <a-spin :spinning="loading">
      <div v-if="!profiles.length && !loading" class="empty">暂无配置</div>
      <div v-for="p in profiles" :key="p.id" class="pf">
        <div class="pfrow">
          <b>{{ p.name }}</b>
          <a-tag v-if="p.enable_grease" color="blue">GREASE</a-tag>
          <span class="dim">{{ p.description || '' }}</span>
          <span class="flex1" />
          <a-button size="small" @click="startEdit(p)">编辑</a-button>
          <a-button size="small" danger @click="remove(p)">删除</a-button>
        </div>
        <div class="pfcipher mono dim">Cipher Suites：{{ fmtField(NUM_FIELDS[0], p.cipher_suites) || '-' }}</div>
      </div>
    </a-spin>

    <a-modal :open="showForm" :title="editingId ? '编辑配置' : '新建配置'" width="620px" :footer="null" @cancel="showForm = false">
      <a-form layout="vertical" size="small">
        <div class="two">
          <a-form-item label="配置名称" required><a-input v-model:value="form.name" placeholder="如：chrome-120-windows" /></a-form-item>
          <a-form-item label="描述"><a-input v-model:value="form.description" /></a-form-item>
        </div>
        <a-form-item label="启用 GREASE"><a-switch v-model:checked="form.enable_grease" size="small" /></a-form-item>
        <a-form-item label="YAML 粘贴解析（可选：粘贴 tls-fingerprint-web 输出后点解析）">
          <a-textarea v-model:value="yamlText" :rows="3" class="mono" placeholder="name: chrome-120&#10;enable_grease: true&#10;cipher_suites: [4865, 4866]" />
          <a-button size="small" style="margin-top: 4px" @click="parseYaml">解析 YAML</a-button>
        </a-form-item>
        <a-form-item label="ALPN 协议（逗号分隔）"><a-input v-model:value="form.alpn_protocols" placeholder='h2, http/1.1' class="mono" /></a-form-item>
        <div class="two">
          <a-form-item v-for="f in NUM_FIELDS" :key="f.key" :label="f.label">
            <a-input v-model:value="fieldInputs[f.key]" class="mono" :placeholder="f.fmt === 'hex' ? '0x1301, 0x1302' : '1, 2'" />
          </a-form-item>
        </div>
        <div class="foot">
          <a-button @click="showForm = false">取消</a-button>
          <a-button type="primary" :loading="saving" @click="submit">{{ editingId ? '保存' : '创建' }}</a-button>
        </div>
      </a-form>
    </a-modal>
  </a-modal>
</template>

<style scoped>
.head { display: flex; align-items: center; justify-content: space-between; gap: 10px; margin-bottom: 10px; }
.dim { color: var(--text2); font-size: 12px; }
.empty { color: var(--text2); font-size: 13px; padding: 16px 0; text-align: center; }
.pf { padding: 8px 10px; border: 1px solid var(--hr); border-radius: 8px; margin-bottom: 8px; }
.pfrow { display: flex; align-items: center; gap: 8px; }
.pfcipher { margin-top: 4px; word-break: break-all; }
.two { display: grid; grid-template-columns: 1fr 1fr; gap: 0 12px; }
.foot { display: flex; justify-content: flex-end; gap: 8px; }
.mono { font-family: Consolas, monospace; }
.flex1 { flex: 1; }
</style>
