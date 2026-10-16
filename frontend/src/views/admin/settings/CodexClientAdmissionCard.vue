<template>
  <section class="codex-admission-panel">
    <div class="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
      <div>
        <div class="flex flex-wrap items-center gap-2">
          <h3 class="text-base font-semibold text-gray-900 dark:text-white">
            {{ localText("Codex 客户端准入", "Codex client admission") }}
          </h3>
          <span :class="['mode-badge', settings.mode === 'audit_only' ? 'is-audit' : 'is-enforce']">
            {{ settings.mode === "audit_only" ? localText("只审计", "Audit only") : localText("正在拦截", "Enforcing") }}
          </span>
        </div>
        <p class="mt-1 max-w-3xl text-sm leading-6 text-gray-500 dark:text-gray-400">
          {{ localText("默认只统计、不拦截。全局拒绝在额度租约和计费前执行；全局允许后，账号自己的 codex_cli_only 仍可继续收紧。", "Audit-only by default. Global deny runs before lease and billing; a global allow never bypasses an account's codex_cli_only rule.") }}
        </p>
      </div>
      <button type="button" class="btn btn-secondary btn-sm shrink-0" :disabled="loading" @click="load">
        {{ localText("刷新统计", "Refresh stats") }}
      </button>
    </div>

    <div v-if="loading" class="mt-5 flex items-center gap-2 text-sm text-gray-500">
      <span class="spinner-sm" /> {{ localText("读取准入策略…", "Loading admission policy…") }}
    </div>

    <template v-else>
      <div class="mt-5 grid gap-4 lg:grid-cols-2">
        <div class="control-block">
          <span class="control-label">{{ localText("运行模式", "Mode") }}</span>
          <div class="choice-grid two">
            <button type="button" data-testid="codex-mode-audit" :class="['choice-button', { active: settings.mode === 'audit_only' }]" @click="settings.mode = 'audit_only'">
              <strong>{{ localText("只审计", "Audit only") }}</strong>
              <small>{{ localText("记录命中，所有请求继续", "Observe and continue") }}</small>
            </button>
            <button type="button" data-testid="codex-mode-enforce" :class="['choice-button danger', { active: settings.mode === 'enforce' }]" @click="settings.mode = 'enforce'">
              <strong>{{ localText("启用拦截", "Enforce") }}</strong>
              <small>{{ localText("违规请求在租约前拒绝", "Reject before lease") }}</small>
            </button>
          </div>
        </div>

        <div class="control-block">
          <span class="control-label">{{ localText("App Server", "App Server") }}</span>
          <div class="flex min-h-[76px] items-center justify-between gap-4 rounded-xl border border-gray-200 bg-white px-4 dark:border-dark-600 dark:bg-dark-900">
            <div>
              <strong class="text-sm text-gray-800 dark:text-gray-100">{{ localText("允许未列名 App Server", "Allow unlisted App Server clients") }}</strong>
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ localText("必须同时携带 Codex 引擎头和另一个独立信号；Claude Code 不属于 App Server。", "Requires a Codex engine header plus another signal; Claude Code is not App Server.") }}</p>
            </div>
            <Toggle
              v-model="settings.app_server_enabled"
              :aria-label="localText('允许未列名 App Server', 'Allow unlisted App Server clients')"
            />
          </div>
        </div>
      </div>

      <div class="mt-4 grid gap-4 lg:grid-cols-2">
        <div class="control-block">
          <span class="control-label">{{ localText("最低版本", "Minimum version") }}</span>
          <select v-model="settings.min_version" class="input">
            <option v-for="version in versionOptions" :key="`min-${version || 'none'}`" :value="version">
              {{ version || localText("不限制", "No limit") }}
            </option>
          </select>
        </div>
        <div class="control-block">
          <span class="control-label">{{ localText("最高版本", "Maximum version") }}</span>
          <select v-model="settings.max_version" class="input">
            <option v-for="version in versionOptions" :key="`max-${version || 'none'}`" :value="version">
              {{ version || localText("不限制", "No limit") }}
            </option>
          </select>
        </div>
      </div>

      <div class="mt-4 control-block">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <span class="control-label !mb-0">{{ localText("全局客户端规则", "Global client rules") }}</span>
          <span class="text-xs text-gray-500 dark:text-gray-400">{{ localText("拒绝优先；这里只能点固定预设，不能手写请求头规则。", "Deny wins; only fixed presets are accepted.") }}</span>
        </div>
        <div class="mt-3 grid gap-2 sm:grid-cols-2 xl:grid-cols-4">
          <div v-for="client in knownClients" :key="client" class="client-rule">
            <span class="text-sm font-medium text-gray-800 dark:text-gray-100">{{ clientLabel(client) }}</span>
            <div class="inline-flex rounded-lg bg-gray-100 p-1 dark:bg-dark-800">
              <button type="button" :data-testid="`codex-client-${client}-allow`" :class="['rule-button', { active: clientAction(client) === 'allow' }]" @click="setClientAction(client, 'allow')">
                {{ localText("允许", "Allow") }}
              </button>
              <button type="button" :data-testid="`codex-client-${client}-deny`" :class="['rule-button deny', { active: clientAction(client) === 'deny' }]" @click="setClientAction(client, 'deny')">
                {{ localText("拒绝", "Deny") }}
              </button>
            </div>
          </div>
        </div>
      </div>

      <div class="mt-4 grid grid-cols-2 gap-2 sm:grid-cols-4">
        <div v-for="item in statCards" :key="item.label" class="stat-card">
          <span>{{ item.label }}</span>
          <strong>{{ item.value.toLocaleString() }}</strong>
        </div>
      </div>
      <div v-if="view.stats.versions.length || view.stats.reasons.length" class="mt-3 flex flex-wrap gap-2">
        <span v-for="item in view.stats.versions.slice(0, 4)" :key="`version-${item.key}`" class="stat-chip">v{{ item.key }} · {{ item.count }}</span>
        <span v-for="item in view.stats.reasons.slice(0, 4)" :key="`reason-${item.key}`" class="stat-chip">{{ reasonLabel(item.key) }} · {{ item.count }}</span>
      </div>

      <div v-if="topProfiles.length" class="profile-block" data-testid="codex-profile-list">
        <div class="profile-heading">
          <span>{{ localText("主要识别组合", "Top detected profiles") }}</span>
          <small>{{ localText("仅展示脱敏分类，不保存原始请求头", "Normalized only; raw headers are not retained") }}</small>
        </div>
        <div class="profile-grid">
          <article
            v-for="(profile, index) in topProfiles"
            :key="`${profile.client}-${profile.version || 'unknown'}-${profile.signals.join('-')}`"
            class="profile-card"
            :data-testid="`codex-profile-${index}`"
          >
            <div class="profile-summary">
              <strong>{{ clientLabel(profile.client) }}</strong>
              <span>{{ profile.version ? `v${profile.version}` : localText("版本未知", "Unknown version") }}</span>
              <b>{{ profile.count.toLocaleString() }}</b>
            </div>
            <div class="profile-signals">
              <span v-for="signal in profile.signals" :key="signal">{{ signalLabel(signal) }}</span>
            </div>
          </article>
        </div>
      </div>

      <div class="mt-5 flex justify-end">
        <button type="button" data-testid="codex-admission-save" class="btn btn-primary btn-sm min-w-28" :disabled="saving" @click="save">
          <span v-if="saving" class="spinner-sm" />
          {{ saving ? localText("保存中", "Saving") : localText("保存准入策略", "Save policy") }}
        </button>
      </div>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { useI18n } from "vue-i18n";
import { adminAPI } from "@/api";
import Toggle from "@/components/common/Toggle.vue";
import { useAppStore } from "@/stores/app";
import { extractApiErrorMessage } from "@/utils/apiError";
import type {
  CodexAdmissionClient,
  CodexClientAdmissionSettings,
  CodexClientAdmissionView,
} from "@/api/admin/settings";

const { locale } = useI18n();
const appStore = useAppStore();
const loading = ref(true);
const saving = ref(false);

const emptyView = (): CodexClientAdmissionView => ({
  settings: {
    mode: "audit_only",
    min_version: "",
    max_version: "",
    allowed_clients: ["desktop", "cli", "vscode", "app_server"],
    denied_clients: [],
    app_server_enabled: false,
  },
  stats: {
    started_at: "",
    inspected: 0,
    observed: 0,
    detected: 0,
    audit_violations: 0,
    denied: 0,
    clients: [],
    reasons: [],
    signals: [],
    versions: [],
    profiles: [],
  },
  known_clients: ["desktop", "cli", "vscode", "app_server"],
  version_presets: ["", "0.144.1", "0.150.0"],
});

const view = reactive<CodexClientAdmissionView>(emptyView());
const settings = reactive<CodexClientAdmissionSettings>({ ...view.settings });
const knownClients = computed(() => view.known_clients || emptyView().known_clients);
const versionOptions = computed(() => Array.from(new Set(["", settings.min_version, settings.max_version, ...(view.version_presets || [])])));
const topProfiles = computed(() => [...(view.stats.profiles || [])]
  .sort((left, right) => right.count - left.count)
  .slice(0, 6));
const statCards = computed(() => [
  { label: localText("已观察", "Observed"), value: view.stats.observed },
  { label: localText("可信识别", "Detected"), value: view.stats.detected },
  { label: localText("违规命中", "Violations"), value: view.stats.audit_violations },
  { label: localText("真实拒绝", "Denied"), value: view.stats.denied },
]);

function localText(zh: string, en: string): string {
  return locale.value === "zh-CN" ? zh : en;
}

function applyView(next: CodexClientAdmissionView): void {
  Object.assign(view, next);
  Object.assign(settings, next.settings);
  settings.allowed_clients = [...(next.settings.allowed_clients || [])];
  settings.denied_clients = [...(next.settings.denied_clients || [])];
}

async function load(): Promise<void> {
  loading.value = true;
  try {
    applyView(await adminAPI.settings.getCodexClientAdmissionSettings());
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, localText("读取 Codex 准入策略失败", "Failed to load Codex admission policy")));
  } finally {
    loading.value = false;
  }
}

async function save(): Promise<void> {
  saving.value = true;
  try {
    applyView(await adminAPI.settings.updateCodexClientAdmissionSettings({
      ...settings,
      allowed_clients: [...settings.allowed_clients],
      denied_clients: [...settings.denied_clients],
    }));
    appStore.showSuccess(localText("Codex 准入策略已保存", "Codex admission policy saved"));
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, localText("保存 Codex 准入策略失败", "Failed to save Codex admission policy")));
  } finally {
    saving.value = false;
  }
}

function clientAction(client: CodexAdmissionClient): "allow" | "deny" {
  // 后端采用显式 allowlist（允许名单）：既不在 allow 也不在 deny 的
  // 客户端同样会被 client_not_allowed 拒绝，界面必须展示真实生效状态。
  return settings.denied_clients.includes(client) || !settings.allowed_clients.includes(client)
    ? "deny"
    : "allow";
}

function setClientAction(client: CodexAdmissionClient, action: "allow" | "deny"): void {
  settings.allowed_clients = settings.allowed_clients.filter((item) => item !== client);
  settings.denied_clients = settings.denied_clients.filter((item) => item !== client);
  if (action === "allow") settings.allowed_clients.push(client);
  else settings.denied_clients.push(client);
}

function clientLabel(client: CodexAdmissionClient): string {
  const labels: Record<CodexAdmissionClient, string> = {
    desktop: "Codex Desktop",
    cli: "Codex CLI",
    vscode: "Codex VS Code",
    app_server: localText("App Server（未列名）", "App Server (unlisted)"),
  };
  return labels[client];
}

function reasonLabel(reason: string): string {
  const labels: Record<string, string> = {
    allowed: localText("已允许", "Allowed"),
    insufficient_signals: localText("信号不足", "Insufficient signals"),
	account_policy_client: localText("交由账号规则判断", "Delegated to account policy"),
    client_type_unknown: localText("未知客户端", "Unknown client"),
    fingerprint_conflict: localText("指纹冲突", "Fingerprint conflict"),
    client_denied: localText("命中拒绝", "Denied client"),
    client_not_allowed: localText("未在允许列表", "Not allowed"),
    app_server_disabled: "App Server disabled",
    version_missing: localText("缺少版本", "Missing version"),
    version_signal_conflict: localText("版本信号冲突", "Version conflict"),
    version_too_low: localText("版本过低", "Version too low"),
    version_too_high: localText("版本过高", "Version too high"),
  };
  return labels[reason] || reason;
}

function signalLabel(signal: string): string {
  const labels: Record<string, string> = {
    user_agent: "User-Agent",
    originator: "Originator",
    codex_path: localText("Codex 路径", "Codex path"),
    client_version: localText("引擎版本", "Engine version"),
    codex_header: localText("Codex 引擎头", "Codex engine header"),
  };
  return labels[signal] || signal;
}

onMounted(load);
</script>

<style scoped>
.codex-admission-panel { @apply rounded-2xl border border-gray-200 bg-gray-50/70 p-5 dark:border-dark-600 dark:bg-dark-800/55; }
.mode-badge { @apply rounded-full px-2.5 py-1 text-xs font-semibold; }
.mode-badge.is-audit { @apply bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300; }
.mode-badge.is-enforce { @apply bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300; }
.control-block { @apply rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-600 dark:bg-dark-900; }
.control-label { @apply mb-2 block text-xs font-semibold uppercase tracking-wide text-gray-500 dark:text-gray-400; }
.choice-grid { @apply grid gap-2; }
.choice-grid.two { @apply grid-cols-2; }
.choice-button { @apply flex min-h-[76px] flex-col items-start justify-center rounded-xl border border-gray-200 px-4 text-left transition dark:border-dark-600; }
.choice-button:hover { @apply border-primary-300; }
.choice-button.active { @apply border-primary-500 bg-primary-50 text-primary-700 ring-1 ring-primary-500 dark:bg-primary-900/20 dark:text-primary-300; }
.choice-button.danger.active { @apply border-red-500 bg-red-50 text-red-700 ring-red-500 dark:bg-red-900/20 dark:text-red-300; }
.choice-button strong { @apply text-sm; }
.choice-button small { @apply mt-1 text-xs opacity-70; }
.client-rule { @apply flex items-center justify-between gap-3 rounded-xl border border-gray-200 bg-white px-3 py-2.5 dark:border-dark-600 dark:bg-dark-900; }
.rule-button { @apply rounded-md px-2.5 py-1 text-xs font-medium text-gray-500 transition dark:text-gray-400; }
.rule-button.active { @apply bg-white text-primary-700 shadow-sm dark:bg-dark-700 dark:text-primary-300; }
.rule-button.deny.active { @apply text-red-600 dark:text-red-300; }
.stat-card { @apply rounded-xl border border-gray-200 bg-white px-3 py-3 dark:border-dark-600 dark:bg-dark-900; }
.stat-card span { @apply block text-xs text-gray-500 dark:text-gray-400; }
.stat-card strong { @apply mt-1 block text-lg text-gray-900 dark:text-white; }
.stat-chip { @apply rounded-full bg-gray-100 px-2.5 py-1 text-xs text-gray-600 dark:bg-dark-700 dark:text-gray-300; }
.profile-block { @apply mt-4 rounded-xl border border-gray-200 bg-white p-3 dark:border-dark-600 dark:bg-dark-900; }
.profile-heading { @apply flex flex-col gap-0.5 sm:flex-row sm:items-center sm:justify-between; }
.profile-heading > span { @apply text-xs font-semibold uppercase tracking-wide text-gray-600 dark:text-gray-300; }
.profile-heading > small { @apply text-xs text-gray-500 dark:text-gray-400; }
.profile-grid { @apply mt-3 grid gap-2 sm:grid-cols-2 xl:grid-cols-3; }
.profile-card { @apply min-w-0 rounded-lg border border-gray-100 bg-gray-50 px-3 py-2.5 dark:border-dark-700 dark:bg-dark-800; }
.profile-summary { @apply flex min-w-0 items-center gap-2; }
.profile-summary strong { @apply min-w-0 truncate text-sm font-semibold text-gray-800 dark:text-gray-100; }
.profile-summary > span { @apply shrink-0 text-xs tabular-nums text-gray-500 dark:text-gray-400; }
.profile-summary > b { @apply ml-auto shrink-0 text-sm tabular-nums text-gray-900 dark:text-white; }
.profile-signals { @apply mt-2 flex flex-wrap gap-1.5; }
.profile-signals span { @apply rounded-md border border-gray-200 bg-white px-1.5 py-0.5 text-[11px] text-gray-500 dark:border-dark-600 dark:bg-dark-900 dark:text-gray-400; }
</style>
