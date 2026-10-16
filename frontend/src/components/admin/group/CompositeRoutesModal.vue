<template>
  <BaseDialog
    :show="show"
    :title="group ? `聚合路由 · ${group.name}` : '聚合路由'"
    width="extra-wide"
    @close="emit('close')"
  >
    <div class="grid gap-5 xl:grid-cols-[minmax(0,1.2fr)_minmax(360px,0.8fr)]">
      <section class="min-w-0 space-y-3">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div>
            <h3 class="text-sm font-semibold text-gray-900 dark:text-white">已保存路由</h3>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">勾选后可一键启停；停用规则不会进入请求快照。</p>
          </div>
          <div class="flex gap-2">
            <button class="btn btn-secondary btn-sm" :disabled="!selectedRouteIds.length || busy" @click="setSelectedEnabled(true)">启用</button>
            <button class="btn btn-secondary btn-sm" :disabled="!selectedRouteIds.length || busy" @click="setSelectedEnabled(false)">停用</button>
            <button class="btn btn-secondary btn-sm" :disabled="loading" @click="load">刷新</button>
          </div>
        </div>

        <div class="overflow-hidden rounded-xl border border-gray-200 dark:border-dark-600">
          <div v-if="loading" class="flex h-36 items-center justify-center text-sm text-gray-500">正在读取…</div>
          <div v-else-if="routes.length === 0" class="flex h-36 flex-col items-center justify-center gap-2 text-sm text-gray-500">
            <Icon name="swap" size="lg" />
            <span>还没有显式规则，已知模型会使用内置平台识别。</span>
          </div>
          <div v-else class="max-h-[34rem] overflow-auto">
            <table class="min-w-full divide-y divide-gray-200 text-sm dark:divide-dark-600">
              <thead class="sticky top-0 bg-gray-50 text-left text-xs text-gray-500 dark:bg-dark-800 dark:text-gray-400">
                <tr>
                  <th class="w-10 px-3 py-2"><input v-model="allSelected" type="checkbox" class="rounded border-gray-300" /></th>
                  <th class="px-3 py-2">用户模型</th>
                  <th class="px-3 py-2">真实去向</th>
                  <th class="px-3 py-2">范围</th>
                  <th class="px-3 py-2 text-right">操作</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-100 bg-white dark:divide-dark-700 dark:bg-dark-900">
                <tr v-for="route in routes" :key="route.id" :class="!route.enabled && 'opacity-55'">
                  <td class="px-3 py-2"><input v-model="selectedRouteIds" type="checkbox" :value="route.id" class="rounded border-gray-300" /></td>
                  <td class="max-w-56 px-3 py-2">
                    <div class="break-all font-medium text-gray-900 dark:text-white">{{ route.public_model }}</div>
                    <span class="mt-1 inline-flex rounded-full bg-gray-100 px-2 py-0.5 text-[11px] text-gray-600 dark:bg-dark-700 dark:text-gray-300">
                      {{ route.match_type === 'exact' ? '精确' : '前缀' }} · P{{ route.priority }}
                    </span>
                  </td>
                  <td class="px-3 py-2">
                    <div class="flex items-center gap-1.5 font-medium text-gray-800 dark:text-gray-100">
                      <PlatformIcon :platform="route.target_platform" size="xs" />
                      {{ platformLabel(route.target_platform) }}
                    </div>
                    <div class="mt-1 break-all text-xs text-gray-500">{{ route.upstream_model || route.public_model }}</div>
                  </td>
                  <td class="px-3 py-2 text-xs text-gray-600 dark:text-gray-300">{{ endpointLabel(route.endpoint) }}</td>
                  <td class="px-3 py-2">
                    <div class="flex justify-end gap-1">
                      <button class="rounded-lg p-2 text-gray-500 hover:bg-gray-100 hover:text-primary-600 dark:hover:bg-dark-700" title="编辑" @click="editRoute(route)"><Icon name="edit" size="sm" /></button>
                      <button
                        class="rounded-lg px-2 py-1.5 text-xs text-gray-500 hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20"
                        :class="deleteConfirmId === route.id && 'bg-red-50 text-red-600 dark:bg-red-900/20'"
                        @click="deleteConfirmId === route.id ? removeRoute(route.id) : (deleteConfirmId = route.id)"
                      >{{ deleteConfirmId === route.id ? '确认删除' : '删除' }}</button>
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </section>

      <section class="space-y-4 rounded-xl border border-gray-200 bg-gray-50/70 p-4 dark:border-dark-600 dark:bg-dark-800/40">
        <div class="flex items-center justify-between">
          <div>
            <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ editingId ? '修改规则' : '新增规则' }}</h3>
            <p class="mt-1 text-xs text-gray-500">选择模型后自动推断平台，保存前由服务端预演冲突。</p>
          </div>
          <button v-if="editingId" class="text-xs text-primary-600" @click="resetForm">取消修改</button>
        </div>

        <div>
          <label class="input-label">常用模型（点击填入）</label>
          <div class="mt-2 flex max-h-24 flex-wrap gap-1.5 overflow-auto">
            <button
              v-for="model in visibleCandidates"
              :key="model"
              type="button"
              class="rounded-full border border-gray-200 bg-white px-2.5 py-1 text-xs text-gray-600 hover:border-primary-400 hover:text-primary-600 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-300"
              @click="pickModel(model)"
            >{{ model }}</button>
          </div>
        </div>

        <div>
          <label class="input-label">用户请求的模型</label>
          <input v-model.trim="form.public_model" class="input" list="composite-model-candidates" placeholder="点选上方模型，或输入自定义别名" @change="inferFormPlatform" />
          <datalist id="composite-model-candidates"><option v-for="model in candidates" :key="model" :value="model" /></datalist>
        </div>

        <div class="grid grid-cols-2 gap-3">
          <div><label class="input-label">匹配方式</label><Select v-model="form.match_type" :options="matchOptions" /></div>
          <div><label class="input-label">接口范围</label><Select v-model="form.endpoint" :options="endpointOptions" /></div>
        </div>

        <div class="grid grid-cols-2 gap-3">
          <div><label class="input-label">目标平台</label><Select v-model="form.target_platform" :options="platformOptions" /></div>
          <div>
            <label class="input-label">优先级（越小越优先）</label>
            <div class="grid grid-cols-4 gap-1.5">
              <button v-for="value in [10, 50, 100, 200]" :key="value" type="button" class="rounded-lg border px-2 py-2 text-xs" :class="form.priority === value ? 'border-primary-500 bg-primary-50 text-primary-700 dark:bg-primary-900/20' : 'border-gray-200 bg-white text-gray-600 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-300'" @click="form.priority = value">{{ value }}</button>
            </div>
          </div>
        </div>

        <div>
          <label class="input-label">上游真实模型</label>
          <input v-model.trim="form.upstream_model" class="input" list="composite-model-candidates" placeholder="默认与用户模型相同" />
        </div>

        <label class="flex cursor-pointer items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
          <input v-model="form.enabled" type="checkbox" class="rounded border-gray-300 text-primary-600" /> 保存后立即启用
        </label>

        <div v-if="preview" class="rounded-xl border p-3 text-sm" :class="preview.candidate_wins ? 'border-emerald-200 bg-emerald-50 text-emerald-800 dark:border-emerald-800 dark:bg-emerald-900/20 dark:text-emerald-200' : 'border-amber-200 bg-amber-50 text-amber-800 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-200'">
          <div class="font-semibold">{{ preview.candidate_wins ? '预览通过' : '规则会被其他规则覆盖' }}</div>
          <div class="mt-1 break-all">{{ preview.public_model }} → {{ platformLabel(preview.target_platform) }} → {{ preview.upstream_model }}</div>
          <div class="mt-1 text-xs">价格来源：{{ preview.price_available ? priceSourceLabel(preview.price_source) : '未找到价格（启用后请求会失败关闭）' }}</div>
          <div v-if="!preview.candidate_wins && preview.route" class="mt-1 text-xs">实际胜出：规则 #{{ preview.route.id }}（{{ preview.route.public_model }}）</div>
        </div>

        <button class="btn btn-primary w-full" :disabled="busy || !form.public_model || !form.target_platform" @click="previewAndSave">
          {{ busy ? '正在验证…' : editingId ? '预览并保存修改' : '预览并创建' }}
        </button>
      </section>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { adminAPI } from "@/api/admin";
import { useAppStore } from "@/stores/app";
import { CONCRETE_PLATFORM_OPTIONS } from "@/constants/platforms";
import type {
  AdminGroup,
  CompositeModelRoute,
  CompositeModelRouteInput,
  CompositeRouteDecision,
  CompositeRouteEndpoint,
  GroupPlatform,
} from "@/types";
import BaseDialog from "@/components/common/BaseDialog.vue";
import Select from "@/components/common/Select.vue";
import PlatformIcon from "@/components/common/PlatformIcon.vue";
import Icon from "@/components/icons/Icon.vue";
import { extractApiErrorMessage } from "@/utils/apiError";

type ConcretePlatform = Exclude<GroupPlatform, "composite">;
type FormState = Required<CompositeModelRouteInput>;

const props = defineProps<{ show: boolean; group: AdminGroup | null }>();
const emit = defineEmits<{ close: []; changed: [] }>();
const appStore = useAppStore();
const routes = ref<CompositeModelRoute[]>([]);
const candidates = ref<string[]>([]);
const selectedRouteIds = ref<number[]>([]);
const loading = ref(false);
const busy = ref(false);
const editingId = ref<number | null>(null);
const deleteConfirmId = ref<number | null>(null);
const preview = ref<CompositeRouteDecision | null>(null);

const blankForm = (): FormState => ({ public_model: "", match_type: "exact", target_platform: "openai", upstream_model: "", endpoint: "any", priority: 100, enabled: true, notes: "" });
const form = reactive<FormState>(blankForm());
const visibleCandidates = computed(() => candidates.value.slice(0, 18));
const allSelected = computed({
  get: () => routes.value.length > 0 && selectedRouteIds.value.length === routes.value.length,
  set: (checked: boolean) => { selectedRouteIds.value = checked ? routes.value.map((route) => route.id) : []; },
});

const platformOptions = [...CONCRETE_PLATFORM_OPTIONS];
const matchOptions = [{ value: "exact", label: "精确模型名" }, { value: "prefix", label: "模型名前缀" }];
const endpointOptions = [
  { value: "any", label: "全部接口" }, { value: "messages", label: "Messages" }, { value: "count_tokens", label: "Count Tokens" },
  { value: "responses", label: "Responses / Compact" }, { value: "chat_completions", label: "Chat Completions" },
  { value: "embeddings", label: "Embeddings" }, { value: "images", label: "图片生成" }, { value: "gemini", label: "Gemini 原生" },
];

const platformLabel = (platform: string) => platformOptions.find((item) => item.value === platform)?.label || platform || "未知";
const endpointLabel = (endpoint: CompositeRouteEndpoint) => endpointOptions.find((item) => item.value === endpoint)?.label || endpoint;
const priceSourceLabel = (source?: string) => ({ channel: "渠道覆盖", litellm: "模型目录", fallback: "内置价格" }[source || ""] || source || "未知");
const detectPlatform = (model: string): ConcretePlatform | null => {
  const value = model.trim().toLowerCase().replace(/^models\//, "");
  if (/^(claude-|anthropic\/|claude\/)/.test(value)) return "anthropic";
  if (/^(gemini-|learnlm-|google\/|google-ai-studio\/|gemini\/)/.test(value)) return "gemini";
  if (/^(grok($|-)|xai\/|x-ai\/)/.test(value)) return "grok";
  if (/^(kimi-|moonshot-|kimi\/|moonshot\/)/.test(value)) return "kimi";
  if (/^(glm-|zhipu\/|bigmodel\/)/.test(value)) return "zhipu";
  if (/^(deepseek-|deepseek\/)/.test(value)) return "deepseek";
  if (/^(gpt-|chatgpt-|codex-|text-embedding-|dall-e-|gpt-image-|o[1345]($|-)|openai\/)/.test(value)) return "openai";
  return null;
};

const inferFormPlatform = () => { const detected = detectPlatform(form.public_model); if (detected) form.target_platform = detected; };
const pickModel = (model: string) => { form.public_model = model; form.upstream_model = model; inferFormPlatform(); preview.value = null; };
const resetForm = () => { Object.assign(form, blankForm()); editingId.value = null; preview.value = null; deleteConfirmId.value = null; };
const editRoute = (route: CompositeModelRoute) => {
  editingId.value = route.id;
  Object.assign(form, { public_model: route.public_model, match_type: route.match_type, target_platform: route.target_platform,
    upstream_model: route.upstream_model, endpoint: route.endpoint, priority: route.priority, enabled: route.enabled, notes: route.notes || "" });
  preview.value = null;
};

const load = async () => {
  if (!props.group) return;
  loading.value = true;
  try {
    [routes.value, candidates.value] = await Promise.all([
      adminAPI.groups.listCompositeRoutes(props.group.id),
      adminAPI.groups.getModelsListCandidates(props.group.id, "composite"),
    ]);
    selectedRouteIds.value = selectedRouteIds.value.filter((id) => routes.value.some((route) => route.id === id));
  } catch (error) { appStore.showError(extractApiErrorMessage(error, "读取聚合路由失败")); }
  finally { loading.value = false; }
};

const previewAndSave = async () => {
  if (!props.group) return;
  busy.value = true;
  preview.value = null;
  const payload: CompositeModelRouteInput = { ...form, upstream_model: form.upstream_model || form.public_model };
  try {
    preview.value = await adminAPI.groups.previewCompositeRoute(props.group.id, {
      model: form.match_type === "prefix" ? `${form.public_model}preview` : form.public_model,
      endpoint: form.endpoint,
      candidate: payload,
      editing_route_id: editingId.value || undefined,
    });
    if (!preview.value.candidate_wins) {
      appStore.showError("该规则会被已有规则覆盖，请调整接口范围、匹配方式或优先级");
      return;
    }
    if (editingId.value) await adminAPI.groups.updateCompositeRoute(props.group.id, editingId.value, payload);
    else await adminAPI.groups.createCompositeRoute(props.group.id, payload);
    appStore.showSuccess(editingId.value ? "聚合路由已更新" : "聚合路由已创建");
    resetForm();
    await load();
    emit("changed");
  } catch (error) { appStore.showError(extractApiErrorMessage(error, "保存聚合路由失败")); }
  finally { busy.value = false; }
};

const removeRoute = async (routeId: number) => {
  if (!props.group) return;
  busy.value = true;
  try { await adminAPI.groups.deleteCompositeRoute(props.group.id, routeId); resetForm(); await load(); emit("changed"); appStore.showSuccess("聚合路由已删除"); }
  catch (error) { appStore.showError(extractApiErrorMessage(error, "删除聚合路由失败")); }
  finally { busy.value = false; }
};

const setSelectedEnabled = async (enabled: boolean) => {
  if (!props.group || !selectedRouteIds.value.length) return;
  busy.value = true;
  try { await adminAPI.groups.batchSetCompositeRoutesEnabled(props.group.id, selectedRouteIds.value, enabled); await load(); emit("changed"); appStore.showSuccess(enabled ? "所选路由已启用" : "所选路由已停用"); }
  catch (error) { appStore.showError(extractApiErrorMessage(error, "批量修改失败")); }
  finally { busy.value = false; }
};

watch(() => props.show, (show) => { if (show) { resetForm(); selectedRouteIds.value = []; void load(); } });
</script>
