<script setup>
/* 页面只编排模块；配置白名单、真实回显与请求代际由 useRiskConfig 管理。 */
import { ref, computed, onMounted, nextTick } from 'vue'
import { useRiskConfig } from '../composables/useRiskConfig.js'
import { buildRiskConfigPatch, riskModeLabel } from '../api/riskControl.js'
import { useDirtyGuard } from '../core/windowContext.js'
import W12Button from '../components/design/W12Button.vue'
import W12Status from '../components/design/W12Status.vue'
import W12Toolbar from '../components/design/W12Toolbar.vue'
import RiskGeneralPanel from '../components/risk/RiskGeneralPanel.vue'
import KeywordPanel from '../components/risk/KeywordPanel.vue'
import KeyTestPanel from '../components/risk/KeyTestPanel.vue'
import AIReadinessSummary from '../components/risk/AIReadinessSummary.vue'
import AIConfigPanel from '../components/risk/AIConfigPanel.vue'
import AITestPanel from '../components/risk/AITestPanel.vue'
import AIRuntimePanel from '../components/risk/AIRuntimePanel.vue'
import RiskLogsPanel from '../components/risk/RiskLogsPanel.vue'
import RiskFingerprints from '../components/risk/RiskFingerprints.vue'
const activeTab = ref('config'), fingerprints = ref(null)
const aiTestPanel = ref(null), saveTesting = ref(false)
const {
  cfg, saved, status, metrics, catalog, groups, loading, saving, err, statusError, catalogError,
  catalogSyncing, refreshing, apiKey, clearAPIKey, load, save, saveKeywords, refreshStatus, syncCatalog,
} = useRiskConfig()
const dirty = computed(() => cfg.value && saved.value && Object.keys(buildRiskConfigPatch(cfg.value, saved.value, { apiKey: apiKey.value, clearAPIKey: clearAPIKey.value })).length > 0)
useDirtyGuard(() => saving.value || !!dirty.value)
async function saveConfig(testAfterSave = false) {
  if (saveTesting.value) return
  saveTesting.value = testAfterSave
  try {
    await save()
    // Keep tests tied to the server's saved response; failed saves or edits
    // made during saving must not test a different draft and report success.
    if (testAfterSave && !err.value && !dirty.value && saved.value?.ai?.config_fingerprint) {
      activeTab.value = 'aitest'
      await nextTick()
      await aiTestPanel.value?.run()
    }
  } finally { saveTesting.value = false }
}
onMounted(load)
</script>
<template>
  <div class="risk w12-design">
    <W12Toolbar>
      <span class="static">内容审查</span>
      <W12Status :tone="status ? '' : 'warn'">{{ !status ? '状态未知' : status.enabled && status.risk_control_enabled ? riskModeLabel(status.mode) : '未启用' }}</W12Status>
      <span v-if="dirty" class="static">有未保存草稿</span>
      <W12Button compact :disabled="saving" @click="load">重新读取</W12Button>
      <span v-if="catalog" class="static">词库 {{ catalog.catalog_version || '—' }} · {{ catalog.pack_count ?? '未知' }} 包 / {{ catalog.keyword_count ?? '未知' }} 词条</span>
      <W12Button compact :loading="catalogSyncing" @click="syncCatalog">同步词库目录</W12Button>
    </W12Toolbar>
    <a-alert v-if="err" type="error" show-icon :message="err" />
    <a-alert v-if="catalogError" type="warning" show-icon :message="`词库目录状态：${catalogError}`" />
    <a-tabs v-model:activeKey="activeTab">
      <a-tab-pane key="config" tab="配置">
        <a-spin :spinning="loading">
          <div v-if="cfg" class="sw">
            <RiskGeneralPanel :cfg="cfg" :saved="saved" :groups="groups" :saving="saving" />
            <KeywordPanel :cfg="cfg" :saving="saving" @save="saveKeywords" />
            <RiskFingerprints ref="fingerprints" />
            <div class="savebar"><W12Button variant="primary" :loading="saving" @click="saveConfig()">保存审查配置</W12Button></div>
          </div>
          <p v-else class="hint">审查配置尚未成功读取。</p>
        </a-spin>
      </a-tab-pane>
      <a-tab-pane key="ai" tab="AI 配置">
        <div v-if="cfg" class="sw">
          <AIReadinessSummary :saved="saved" :metrics="metrics" :dirty="dirty" :error="statusError" />
          <AIConfigPanel :cfg="cfg" :saved="saved" v-model:api-key="apiKey" v-model:clearAPIKey="clearAPIKey" :saving="saving" />
          <div class="savebar">
            <W12Button :loading="saving" :disabled="saveTesting" @click="saveConfig()">保存配置</W12Button>
            <W12Button variant="primary" :loading="saveTesting" :disabled="saving" @click="saveConfig(true)">保存并合成测试</W12Button>
          </div>
        </div>
        <p v-else class="hint">请先成功读取审查配置。</p>
      </a-tab-pane>
      <a-tab-pane key="aitest" tab="AI 合成测试" force-render><AITestPanel ref="aiTestPanel" :saved="saved" :saving="saving" :dirty="dirty" @tested="refreshStatus" /></a-tab-pane>
      <a-tab-pane key="runtime" tab="运行状态"><AIRuntimePanel :status="status" :metrics="metrics" :saved="saved" :error="statusError" :refreshing="refreshing" @refresh="refreshStatus" /></a-tab-pane>
      <a-tab-pane key="logs" tab="命中日志"><RiskLogsPanel @fingerprints-changed="fingerprints?.refresh()" /></a-tab-pane>
      <a-tab-pane v-if="cfg && cfg.keyword_blocking_mode !== 'ai_only'" key="keytest" tab="旧审核接口测试"><KeyTestPanel :cfg="saved" /></a-tab-pane>
    </a-tabs>
  </div>
</template>
<style scoped>
@import '../components/settings/settings.css';
.risk { height: 100%; overflow-y: auto; }
</style>
