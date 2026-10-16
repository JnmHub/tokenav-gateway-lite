<script setup>
/* ============================================================
 * 门户定制卡（对照 dto.SystemSettings 门户字段）
 * - home_content：门户首页内容（HTML/富文本，textarea 编辑）
 * - custom_menu_items：CustomMenuItem[]（settings.go）→
 *   { id, label, icon_svg, url, page_slug?, visibility, sort_order }；
 *   url 支持 "md:slug"（站内 Markdown 页）；id 留空由后端自动生成；
 *   visibility = user | admin；sort_order 按列表顺序在保存时自动编号
 * - custom_endpoints：CustomEndpoint[] → { name, endpoint, description }
 * - purchase_subscription_enabled / purchase_subscription_url
 *   （后端校验：enabled 时 URL 必填且必须是绝对 http(s) 地址，故一并暴露 URL 字段）
 * 数组均为整体替换语义（请求指针字段），保存走 SystemSettings.vue 快照+patch。
 * ============================================================ */
import { ref, computed } from 'vue'
import { message } from 'ant-design-vue'
import { PlusOutlined, DeleteOutlined, ArrowUpOutlined, ArrowDownOutlined } from '@ant-design/icons-vue'

const props = defineProps({
  s: { type: Object, required: true },
  save: { type: Function, required: true },
})

const saving = ref(false)

// 菜单项直接编辑快照数组（引用绑定）；保存时按顺序重编 sort_order
const menus = computed(() => {
  if (!Array.isArray(props.s.custom_menu_items)) props.s.custom_menu_items = []
  return props.s.custom_menu_items
})
const addMenu = () => menus.value.push({ id: '', label: '', icon_svg: '', url: '', visibility: 'user', sort_order: menus.value.length })
const removeMenu = (i) => menus.value.splice(i, 1)
const moveMenu = (i, dir) => {
  const j = i + dir
  if (j < 0 || j >= menus.value.length) return
  const arr = menus.value
  ;[arr[i], arr[j]] = [arr[j], arr[i]]
}

// 自定义端点行编辑
const endpoints = computed(() => {
  if (!Array.isArray(props.s.custom_endpoints)) props.s.custom_endpoints = []
  return props.s.custom_endpoints
})
const addEndpoint = () => endpoints.value.push({ name: '', endpoint: '', description: '' })
const removeEndpoint = (i) => endpoints.value.splice(i, 1)

const VIS_OPTIONS = [
  { value: 'user', label: '所有用户可见' },
  { value: 'admin', label: '仅管理员可见' },
]

const doSave = async () => {
  // 预校验后端硬规则，减少整包 400 往返（菜单 ≤20、端点 ≤10、label/name 必填）
  const items = menus.value.filter((m) => String(m.label || '').trim() || String(m.url || '').trim())
  if (items.some((m) => !String(m.label || '').trim())) {
    message.warning('自定义菜单项名称不能为空')
    return
  }
  if (items.some((m) => !String(m.url || '').trim())) {
    message.warning('菜单项地址不能为空（站内页可填 md:slug）')
    return
  }
  if (items.length > 20) {
    message.warning('自定义菜单最多 20 项')
    return
  }
  const eps = endpoints.value.filter((e) => String(e.name || '').trim() || String(e.endpoint || '').trim())
  if (eps.some((e) => !String(e.name || '').trim() || !String(e.endpoint || '').trim())) {
    message.warning('自定义端点的名称与地址均不能为空')
    return
  }
  if (eps.length > 10) {
    message.warning('自定义端点最多 10 个')
    return
  }
  if (props.s.purchase_subscription_enabled && !String(props.s.purchase_subscription_url || '').trim()) {
    message.warning('启用购买订阅入口时必须填写跳转地址')
    return
  }
  saving.value = true
  try {
    await props.save('portal', {
      home_content: String(props.s.home_content || ''),
      // 按显示顺序重编 sort_order；icon_svg 选填（≤10KB 由后端校验）
      custom_menu_items: items.map((m, i) => ({
        id: String(m.id || ''),
        label: String(m.label || '').trim(),
        icon_svg: String(m.icon_svg || ''),
        url: String(m.url || '').trim(),
        visibility: m.visibility === 'admin' ? 'admin' : 'user',
        sort_order: i,
      })),
      custom_endpoints: eps.map((e) => ({
        name: String(e.name || '').trim(),
        endpoint: String(e.endpoint || '').trim(),
        description: String(e.description || '').trim(),
      })),
      purchase_subscription_enabled: !!props.s.purchase_subscription_enabled,
      purchase_subscription_url: String(props.s.purchase_subscription_url || '').trim(),
    })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="card">
    <div class="card-title">门户定制</div>
    <div class="form">
      <div class="frow frow-col">
        <label>首页内容</label>
        <div class="fm-col">
          <a-textarea v-model:value="s.home_content" class="fld-full mono" :rows="6" auto-size
            placeholder="门户首页展示内容（支持 HTML）" />
        </div>
      </div>

      <div class="frow">
        <label>购买订阅入口</label>
        <div class="fmain"><a-switch v-model:checked="s.purchase_subscription_enabled" /></div>
      </div>
      <div class="frow">
        <label>购买订阅地址</label>
        <div class="fmain">
          <a-input v-model:value="s.purchase_subscription_url" class="fld-full" :disabled="!s.purchase_subscription_enabled"
            placeholder="https://...（启用时必填）" />
        </div>
      </div>
    </div>

    <div class="divider" />

    <div class="card-title">自定义菜单项</div>
    <div v-for="(m, i) in menus" :key="m.id || i" class="frow frow-col">
      <label>菜单 {{ i + 1 }}</label>
      <div class="box">
        <div class="brow">
          <a-input v-model:value="m.label" class="fld" placeholder="菜单名称（必填）" :maxlength="50" />
          <a-select v-model:value="m.visibility" class="fld-n" :options="VIS_OPTIONS" />
          <a-button size="small" type="text" :disabled="i === 0" @click="moveMenu(i, -1)"><ArrowUpOutlined /></a-button>
          <a-button size="small" type="text" :disabled="i === menus.length - 1" @click="moveMenu(i, 1)"><ArrowDownOutlined /></a-button>
          <a-button size="small" type="text" danger @click="removeMenu(i)"><DeleteOutlined /></a-button>
        </div>
        <a-input v-model:value="m.url" class="fld-full" placeholder="跳转地址：https://... 或站内页 md:slug" :maxlength="2048" />
        <a-collapse ghost size="small" class="iconfold">
          <a-collapse-panel key="icon" header="图标 SVG（选填，≤10KB）">
            <a-textarea v-model:value="m.icon_svg" class="fld-full mono" :rows="3" placeholder="<svg ...>...</svg>" />
          </a-collapse-panel>
        </a-collapse>
      </div>
    </div>
    <div class="frow">
      <label>菜单项</label>
      <div class="fmain">
        <a-button size="small" @click="addMenu"><PlusOutlined /> 添加菜单项</a-button>
        <span class="static">显示顺序即列表顺序，保存时自动编号</span>
      </div>
    </div>

    <div class="divider" />

    <div class="card-title">自定义端点（用户端快捷复制）</div>
    <div v-for="(e, i) in endpoints" :key="i" class="frow frow-col">
      <label>端点 {{ i + 1 }}</label>
      <div class="box">
        <div class="brow">
          <a-input v-model:value="e.name" class="fld" placeholder="名称（必填，≤50 字）" :maxlength="50" />
          <a-button size="small" type="text" danger @click="removeEndpoint(i)"><DeleteOutlined /></a-button>
        </div>
        <a-input v-model:value="e.endpoint" class="fld-full" placeholder="接口地址 https://...（必填）" :maxlength="2048" />
        <a-input v-model:value="e.description" class="fld-full" placeholder="说明（选填，≤200 字）" :maxlength="200" />
      </div>
    </div>
    <div class="frow">
      <label>端点</label>
      <div class="fmain">
        <a-button size="small" @click="addEndpoint"><PlusOutlined /> 添加端点</a-button>
        <span class="static">展示在用户端「接口地址」快捷复制区</span>
      </div>
    </div>

    <div class="savebar">
      <a-button type="primary" class="save" :loading="saving" @click="doSave">保存门户定制</a-button>
    </div>
  </div>
</template>

<style scoped>
@import './settings.css';
.box { display: flex; flex-direction: column; gap: 6px; width: 100%; }
.brow { display: flex; align-items: center; gap: 4px; }
.brow .ant-input { flex: 1; }
.iconfold { margin-left: -8px; }
.iconfold :deep(.ant-collapse-header) { padding: 4px 8px; font-size: 12px; color: var(--text2); }
.divider { border-top: 1px solid var(--hr); margin: 12px -16px; opacity: 0.6; }
</style>
