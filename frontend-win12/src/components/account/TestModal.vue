<script setup>
/* 连通性测试弹窗（三期从主文件迁出并增强）——旧实现契约：frontend/src/components/admin/account/AccountTestModal.vue
 * 接口：GET /admin/accounts/:id/models（可选模型，Sonnet 优先/Gemini 取第一个）
 *       POST /admin/accounts/:id/test（SSE 流：test_start/content/status/image/audio/video/test_complete/error）
 * 模式：
 * - OpenAI：mode = default | compact（二期已有）
 * - Grok（三期新增）：mode ∈ text|image|video|search|tts|stt|realtime
 *   · text/image/video 需选模型；search/tts/stt/realtime 强制 model_id=''
 *   · prompt 仅 image/video/search/tts 显示；image/video 支持图片上传(image_data_url)；
 *     stt 支持音频上传(audio_data_url) */
import { ref, computed, watch, nextTick } from 'vue'
import { request, isCancellation } from '../../api'
import { useDialogRequestLifetime } from '../../composables/useDialogRequestLifetime'
import { readAccountTestStream } from '../../api/accountTestStream'
import W12Dialog from '../design/W12Dialog.vue'
import { message } from 'ant-design-vue'
import { UploadOutlined } from '@ant-design/icons-vue'

const open = defineModel('open', { type: Boolean, default: false })
const props = defineProps({ account: { type: Object, default: null } })

const models = ref([])
const modelId = ref('')
const loadingModels = ref(false)
const openaiMode = ref('default')
const grokMode = ref('text')
const prompt = ref('')
const imageMsg = ref('')
const audioMsg = ref('')
const lines = ref([])
const status = ref('idle')
const termRef = ref(null)
const fileReaders = new Set()
const dialog = useDialogRequestLifetime(() => open.value, () => {
  for (const reader of fileReaders) reader.abort()
  fileReaders.clear()
  models.value = []; modelId.value = ''; loadingModels.value = false
  lines.value = []; status.value = 'idle'; prompt.value = ''; imageMsg.value = ''; audioMsg.value = ''
}, () => props.account?.id)

const isGrok = computed(() => props.account?.platform === 'grok')
const GROK_MODES = [
  { value: 'text', label: '文本' },
  { value: 'image', label: '生图' },
  { value: 'video', label: '生视频' },
  { value: 'search', label: '联网搜索' },
  { value: 'tts', label: '语音合成' },
  { value: 'stt', label: '语音识别' },
  { value: 'realtime', label: '实时音频' },
]
const grokNeedModel = computed(() => ['text', 'image', 'video'].includes(grokMode.value))
const showPrompt = computed(() => !isGrok.value ? isImgModel(modelId.value) : ['image', 'video', 'search', 'tts'].includes(grokMode.value))
const promptLabel = computed(() => ({ video: '视频提示词', search: '搜索关键词', tts: '合成文本' }[grokMode.value] || '生图提示词'))
const showImageUpload = computed(() => isGrok.value && ['image', 'video'].includes(grokMode.value))
const showAudioUpload = computed(() => isGrok.value && grokMode.value === 'stt')
const canStart = computed(() => {
  if (['connecting', 'streaming'].includes(status.value)) return false
  if (isGrok.value) {
    if (grokNeedModel.value && !modelId.value.trim()) return false
    if (grokMode.value === 'stt' && !audioMsg.value) return false
    return true
  }
  return !!modelId.value.trim()
})

const isImgModel = (m) => {
  const id = (m || '').toLowerCase()
  const p = props.account?.platform
  if (p === 'gemini' || (p === 'antigravity' && props.account?.type === 'apikey')) return id.startsWith('gemini-') && id.includes('-image')
  if (p === 'openai') return id.startsWith('gpt-image-')
  return false
}

// grok 模式切换时清空上传并填默认提示词（旧 watch(grokTestMode) 等价）
watch(grokMode, (m) => {
  prompt.value = ({ video: '一只橘猫在窗台上晒太阳，微风吹动窗帘', search: '今天的科技新闻有哪些', tts: '你好，这是一段语音合成测试文本', image: '画一只橘猫，坐在窗台上' }[m]) || ''
  imageMsg.value = ''
  audioMsg.value = ''
})

const addLine = (text, cls = '') => {
  lines.value.push({ text, cls })
  nextTick(() => { if (termRef.value) termRef.value.scrollTop = termRef.value.scrollHeight })
}
const scrollBottom = () => nextTick(() => { if (termRef.value) termRef.value.scrollTop = termRef.value.scrollHeight })

const loadModels = async () => {
  const r = props.account
  if (!r?.id) return
  const turn = dialog.capture()
  loadingModels.value = true
  models.value = []
  modelId.value = ''
  try {
    const list = (await request(`/admin/accounts/${r.id}/models`, { ...turn, method: 'GET' })) || []
    turn.assertCurrent()
    models.value = list.map((m) => ({ value: m.id, label: m.id }))
    if (list.length) {
      const sonnet = list.find((m) => m.id.includes('sonnet'))
      modelId.value = (r.platform === 'gemini' ? list[0].id : sonnet?.id) || list[0].id
    }
  } catch (e) {
    if (turn.isCurrent() && !isCancellation(e)) message.warning(`读取可用模型失败：${e.message}（仍可手填模型名）`)
  } finally {
    if (turn.isCurrent()) loadingModels.value = false
  }
}

watch([open, () => props.account?.id], ([o]) => {
  if (o) {
    lines.value = []
    status.value = 'idle'
    openaiMode.value = 'default'
    grokMode.value = 'text'
    prompt.value = ''
    imageMsg.value = ''
    audioMsg.value = ''
    loadModels()
  }
}, { flush: 'sync' })

const readAsDataURL = (file) => new Promise((resolve, reject) => {
  const reader = new FileReader()
  fileReaders.add(reader)
  reader.onload = () => { fileReaders.delete(reader); resolve(String(reader.result || '')) }
  reader.onerror = () => { fileReaders.delete(reader); reject(new Error('读取文件失败')) }
  reader.onabort = () => { fileReaders.delete(reader); resolve('') }
  reader.readAsDataURL(file)
})
const readUpload = async (info, target) => {
  const turn = dialog.capture()
  try {
    const data = info?.file ? await readAsDataURL(info.file.originFileObj || info.file) : ''
    if (turn.isCurrent()) target.value = data
  } catch (e) { if (turn.isCurrent()) message.error(e.message) }
}
const onImageFile = (info) => readUpload(info, imageMsg)
const onAudioFile = (info) => readUpload(info, audioMsg)

const runTest = async () => {
  const r = props.account
  if (!r || !canStart.value) return
  lines.value = []
  status.value = 'connecting'
  addLine(`开始测试「${r.name}」（${r.type}）`, 'dim')
  addLine('')
  const turn = dialog.capture()
  let imgCount = 0, streamText = ''
  let completed = false
  try {
    // 请求体契约（旧 AccountTestModal.startTest）：grok 的 search/tts/stt/realtime 强制空模型
    const body = {
      model_id: isGrok.value && !grokNeedModel.value ? '' : modelId.value.trim(),
      prompt: showPrompt.value ? prompt.value.trim() : '',
    }
    if (r.platform === 'openai') body.mode = openaiMode.value
    if (isGrok.value) {
      body.mode = grokMode.value
      if (showImageUpload.value && imageMsg.value) body.image_data_url = imageMsg.value
      if (showAudioUpload.value && audioMsg.value) body.audio_data_url = audioMsg.value
    }
    status.value = 'streaming'
    await readAccountTestStream(r.id, body, turn, (ev) => {
      turn.assertCurrent()
        switch (ev.type) {
          case 'test_start':
            addLine('已连接上游 API', 'ok')
            if (ev.model) addLine(`使用模型：${ev.model}`, 'dim')
            addLine('发送测试消息…', 'dim'); addLine(''); addLine('—— 响应 ——', 'dim')
            break
          case 'content':
            if (ev.text) { streamText += ev.text; scrollBottom() }
            break
          case 'status': if (ev.text) addLine(ev.text, 'dim'); break
          case 'image':
            if (ev.image_url) { lines.value.push({ text: `收到图片 #${++imgCount}`, cls: 'ok', img: ev.image_url, mime: ev.mime_type }); scrollBottom() }
            break
          case 'audio':
            if (ev.audio_url) { lines.value.push({ text: '收到音频', cls: 'ok', audio: ev.audio_url, mime: ev.mime_type }); scrollBottom() }
            break
          case 'video':
            if (ev.video_url) { lines.value.push({ text: '收到视频', cls: 'ok', video: ev.video_url, mime: ev.mime_type }); scrollBottom() }
            break
          case 'test_complete':
            completed = true
            if (streamText) { addLine(streamText, 'out'); streamText = '' }
            if (ev.success) { status.value = 'success'; addLine('✓ 测试通过', 'ok') }
            else { status.value = 'error'; addLine(`✗ 测试失败：${ev.error || '未知错误'}`, 'err') }
            break
          case 'error':
            completed = true
            if (streamText) { addLine(streamText, 'out'); streamText = '' }
            status.value = 'error'
            addLine(`✗ ${ev.error || '未知错误'}`, 'err')
            break
        }
    })
    if (turn.isCurrent() && !completed) {
      status.value = 'error'; addLine('事件流结束，但后台未返回完成结果', 'err')
    }
  } catch (e) {
    if (!turn.isCurrent() || isCancellation(e)) return
    status.value = 'error'
    addLine(`✗ ${e.message}`, 'err')
  }
}
const close = () => { dialog.cancel(); open.value = false }
</script>

<template>
  <W12Dialog :open="open" :title="`连通性测试 · ${account?.name || ''}`" width="680px" :footer="null" @cancel="close">
    <div class="ctl">
      <a-select
        v-if="!isGrok || grokNeedModel"
        v-model:value="modelId" show-search :options="models" :loading="loadingModels"
        placeholder="选择或输入模型 ID" style="width: 240px"
      />
      <a-select v-if="account?.platform === 'openai'" v-model:value="openaiMode" style="width: 130px">
        <a-select-option value="default">标准测试</a-select-option>
        <a-select-option value="compact">精简测试</a-select-option>
      </a-select>
      <a-select v-if="isGrok" v-model:value="grokMode" style="width: 130px" :options="GROK_MODES" />
      <a-button type="primary" :disabled="!canStart" @click="runTest">开始测试</a-button>
    </div>
    <div v-if="showPrompt" class="prow">
      <span class="plabel">{{ promptLabel }}</span>
      <a-textarea v-model:value="prompt" :rows="2" style="flex: 1" />
    </div>
    <div v-if="showImageUpload" class="prow">
      <span class="plabel">参考图片</span>
      <a-upload :show-upload-list="false" :before-upload="() => false" accept="image/*" @change="onImageFile">
        <a-button size="small"><UploadOutlined /> 选择图片{{ imageMsg ? '（已选）' : '' }}</a-button>
      </a-upload>
    </div>
    <div v-if="showAudioUpload" class="prow">
      <span class="plabel">音频文件</span>
      <a-upload :show-upload-list="false" :before-upload="() => false" accept="audio/*" @change="onAudioFile">
        <a-button size="small"><UploadOutlined /> 选择音频{{ audioMsg ? '（已选）' : '' }}</a-button>
      </a-upload>
    </div>
    <div ref="termRef" class="term">
      <div v-if="!lines.length" class="t-empty">选择模式后点击「开始测试」，输出将实时显示在这里</div>
      <template v-for="(l, i) in lines" :key="i">
        <div :class="['t-line', l.cls]">{{ l.text }}</div>
        <img v-if="l.img" :src="l.img" class="t-img" alt="生成图片" />
        <audio v-if="l.audio" :src="l.audio" controls class="t-media" />
        <video v-if="l.video" :src="l.video" controls class="t-media" />
      </template>
      <div v-if="status === 'connecting' || status === 'streaming'" class="t-line dim">…</div>
    </div>
    <div class="foot">
      <span v-if="status === 'success'" class="ok">✓ 测试通过</span>
      <span v-else-if="status === 'error'" class="bad">✗ 测试失败</span>
      <span v-else-if="status !== 'idle'" class="dim">测试中…</span>
      <span class="flex1" />
      <a-button size="small" @click="close">关闭</a-button>
    </div>
  </W12Dialog>
</template>

<style scoped>
.ctl { display: flex; gap: 8px; align-items: center; margin-bottom: 10px; flex-wrap: wrap; }
.prow { display: flex; gap: 8px; align-items: flex-start; margin-bottom: 8px; }
.plabel { font-size: 12px; color: var(--text2); line-height: 26px; flex: none; }
.term { height: 300px; overflow-y: auto; background: #1e1e22; border-radius: 8px; padding: 10px 12px; font-family: Consolas, monospace; font-size: 12.5px; line-height: 1.55; }
.t-empty { color: #6b6b70; }
.t-line { white-space: pre-wrap; word-break: break-all; color: #d6d6da; }
.t-line.dim { color: #8a8a90; } .t-line.ok { color: #6fd08c; } .t-line.err { color: #ff8a8a; } .t-line.out { color: #9fd0ff; }
.t-img { display: block; max-width: 240px; max-height: 180px; border-radius: 6px; margin: 6px 0; }
.t-media { display: block; max-width: 280px; margin: 6px 0; }
.foot { display: flex; align-items: center; gap: 8px; margin-top: 10px; }
.foot .flex1 { flex: 1; }
.ok { color: #1a7f37; font-size: 13px; } .bad { color: #c13438; font-size: 13px; }
.dim { color: var(--text2); font-size: 12px; }
</style>
