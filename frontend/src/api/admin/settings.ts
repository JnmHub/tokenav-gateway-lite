/**
 * Admin Settings API endpoints
 * Handles system settings management for administrators
 */

import { apiClient } from "../client";
import type {
  CustomEndpoint,
  CustomMenuItem,
  NotifyEmailEntry,
} from "@/types";

// ── 平台限额类型 ──────────────────────────────────────────────────
export type PlatformType = "anthropic" | "openai" | "gemini" | "antigravity"
export type QuotaWindowType = "daily" | "weekly" | "monthly"

/** 单平台三档限额；null = 不限制，undefined = 未填（等价 null） */
export interface PlatformQuotaLimits {
  daily:   number | null
  weekly:  number | null
  monthly: number | null
}

/** 全平台默认限额 map（key = PlatformType） */
export type DefaultPlatformQuotasMap = Partial<Record<PlatformType, PlatformQuotaLimits>>

const PLATFORMS: PlatformType[] = ["anthropic", "openai", "gemini", "antigravity"]

/** 归一化为全 4 平台 × 3 窗口（缺失填 null），供模板非空绑定 */
export function normalizePlatformQuotasMap(input?: DefaultPlatformQuotasMap | null): DefaultPlatformQuotasMap {
  const result: DefaultPlatformQuotasMap = {}
  for (const p of PLATFORMS) {
    const src = input?.[p]
    result[p] = {
      daily:   typeof src?.daily === "number" ? src.daily : null,
      weekly:  typeof src?.weekly === "number" ? src.weekly : null,
      monthly: typeof src?.monthly === "number" ? src.monthly : null,
    }
  }
  return result
}

/** 提交前清洗：非有限数/负数/空字符串 → null（保留 0 = 显式禁用），返回全 4 平台嵌套 map */
export function sanitizePlatformQuotasMap(input?: DefaultPlatformQuotasMap | null): DefaultPlatformQuotasMap {
  const clean = (v: unknown): number | null => (typeof v === "number" && Number.isFinite(v) && v >= 0 ? v : null)
  const result: DefaultPlatformQuotasMap = {}
  for (const p of PLATFORMS) {
    const src = input?.[p]
    result[p] = { daily: clean(src?.daily), weekly: clean(src?.weekly), monthly: clean(src?.monthly) }
  }
  return result
}

/**
 * System settings interface
 */
export interface SystemSettings {
  frontend_url: string;
  totp_enabled: boolean; // TOTP 双因素认证
  totp_encryption_key_configured: boolean; // TOTP 加密密钥是否已配置
  default_balance: number;
  default_concurrency: number;
  default_user_rpm_limit: number;
  default_platform_quotas?: DefaultPlatformQuotasMap;
  // OEM settings
  site_name: string;
  site_logo: string;
  site_subtitle: string;
  api_base_url: string;
  contact_info: string;
  doc_url: string;
  home_content: string;
  hide_ccs_import_button: boolean;
  table_default_page_size: number;
  table_page_size_options: number[];
  backend_mode_enabled: boolean;
  custom_menu_items: CustomMenuItem[];
  custom_endpoints: CustomEndpoint[];
  // Cloudflare Turnstile settings
  turnstile_enabled: boolean;
  turnstile_site_key: string;
  turnstile_secret_key_configured: boolean;
  api_key_acl_trust_forwarded_ip: boolean;
  forwarded_client_ip_headers: string[];

  // LinuxDo Connect OAuth settings
  linuxdo_connect_enabled: boolean;
  linuxdo_connect_client_id: string;
  linuxdo_connect_client_secret_configured: boolean;
  linuxdo_connect_redirect_url: string;

  // DingTalk Connect OAuth settings
  dingtalk_connect_enabled: boolean;
  dingtalk_connect_client_id: string;
  dingtalk_connect_client_secret_configured: boolean;
  dingtalk_connect_redirect_url: string;
  dingtalk_connect_corp_restriction_policy: string;
  dingtalk_connect_internal_corp_id: string;
  dingtalk_connect_bypass_registration: boolean;
  dingtalk_connect_sync_corp_email: boolean;
  dingtalk_connect_sync_display_name: boolean;
  dingtalk_connect_sync_dept: boolean;
  dingtalk_connect_sync_corp_email_attr_key: string;
  dingtalk_connect_sync_display_name_attr_key: string;
  dingtalk_connect_sync_dept_attr_key: string;
  dingtalk_connect_sync_corp_email_attr_name: string;
  dingtalk_connect_sync_display_name_attr_name: string;
  dingtalk_connect_sync_dept_attr_name: string;

  // WeChat Connect OAuth settings
  wechat_connect_enabled: boolean;
  wechat_connect_app_id: string;
  wechat_connect_app_secret_configured: boolean;
  wechat_connect_open_app_id?: string;
  wechat_connect_open_app_secret_configured?: boolean;
  wechat_connect_mp_app_id?: string;
  wechat_connect_mp_app_secret_configured?: boolean;
  wechat_connect_mobile_app_id?: string;
  wechat_connect_mobile_app_secret_configured?: boolean;
  wechat_connect_open_enabled?: boolean;
  wechat_connect_mp_enabled?: boolean;
  wechat_connect_mobile_enabled?: boolean;
  wechat_connect_mode: string;
  wechat_connect_scopes: string;
  wechat_connect_redirect_url: string;
  wechat_connect_frontend_redirect_url: string;

  // Generic OIDC OAuth settings
  oidc_connect_enabled: boolean;
  oidc_connect_provider_name: string;
  oidc_connect_client_id: string;
  oidc_connect_client_secret_configured: boolean;
  oidc_connect_issuer_url: string;
  oidc_connect_discovery_url: string;
  oidc_connect_authorize_url: string;
  oidc_connect_token_url: string;
  oidc_connect_userinfo_url: string;
  oidc_connect_jwks_url: string;
  oidc_connect_scopes: string;
  oidc_connect_redirect_url: string;
  oidc_connect_frontend_redirect_url: string;
  oidc_connect_token_auth_method: string;
  oidc_connect_use_pkce: boolean;
  oidc_connect_validate_id_token: boolean;
  oidc_connect_allowed_signing_algs: string;
  oidc_connect_clock_skew_seconds: number;
  oidc_connect_require_email_verified: boolean;
  oidc_connect_userinfo_email_path: string;
  oidc_connect_userinfo_id_path: string;
  oidc_connect_userinfo_username_path: string;
  github_oauth_enabled: boolean;
  github_oauth_client_id: string;
  github_oauth_client_secret_configured: boolean;
  github_oauth_redirect_url: string;
  github_oauth_frontend_redirect_url: string;
  google_oauth_enabled: boolean;
  google_oauth_client_id: string;
  google_oauth_client_secret_configured: boolean;
  google_oauth_redirect_url: string;
  google_oauth_frontend_redirect_url: string;

  // Model fallback configuration
  enable_model_fallback: boolean;
  fallback_model_anthropic: string;
  fallback_model_openai: string;
  fallback_model_gemini: string;
  fallback_model_antigravity: string;

  // Identity patch configuration (Claude -> Gemini)
  enable_identity_patch: boolean;
  identity_patch_prompt: string;

  // Ops Monitoring (vNext)
  ops_monitoring_enabled: boolean;
  ops_realtime_monitoring_enabled: boolean;
  ops_query_mode_default: "auto" | "raw" | "preagg" | string;
  ops_metrics_interval_seconds: number;

  // Claude Code version check
  min_claude_code_version: string;
  max_claude_code_version: string;

  // 分组隔离
  allow_ungrouped_key_scheduling: boolean;

  // Gateway forwarding behavior
  gateway_custom_error_message: string;
  enable_fingerprint_unification: boolean;
  enable_metadata_passthrough: boolean;
  enable_cch_signing: boolean;
  enable_claude_oauth_system_prompt_injection: boolean;
  claude_oauth_system_prompt: string;
  claude_oauth_system_prompt_blocks: string;
  enable_anthropic_cache_ttl_1h_injection: boolean;
  rewrite_message_cache_control: boolean;
  enable_client_dateline_normalization: boolean;
  antigravity_user_agent_version: string;
  openai_codex_user_agent: string;
  openai_codex_client_version: string;
  openai_codex_client_version_synced: string;
  openai_codex_version_auto_sync_enabled: boolean;
  openai_allow_claude_code_codex_plugin: boolean;
  web_search_emulation_enabled?: boolean;

  // Payment configuration
  payment_enabled: boolean;
  risk_control_enabled: boolean;

  // Cyber session block
  cyber_session_block_enabled: boolean;
  cyber_session_block_ttl_seconds: number;

  payment_min_amount: number;
  payment_max_amount: number;
  payment_daily_limit: number;
  payment_order_timeout_minutes: number;
  payment_max_pending_orders: number;
  payment_enabled_types: string[];
  payment_balance_disabled: boolean;
  payment_balance_recharge_multiplier: number;
  payment_recharge_fee_rate: number;
  payment_load_balance_strategy: string;
  payment_product_name_prefix: string;
  payment_product_name_suffix: string;
  payment_help_image_url: string;
  payment_help_text: string;
  payment_cancel_rate_limit_enabled: boolean;
  payment_cancel_rate_limit_max: number;
  payment_cancel_rate_limit_window: number;
  payment_cancel_rate_limit_unit: string;
  payment_cancel_rate_limit_window_mode: string;
  payment_alipay_force_qrcode?: boolean;
  openai_advanced_scheduler_enabled?: boolean;
  grok_default_text_model: string;
  grok_cross_client_model_map_enabled: boolean;
  grok_scheduling_threshold_percent: number;
  upstream_billing_auto_probe_enabled: boolean;
  upstream_billing_probe_interval_minutes: number;
  upstream_billing_probe_batch_size: number;
  upstream_billing_probe_concurrency: number;
  openai_upstream_cost_scheduling_enabled: boolean;
  openai_upstream_cost_weight: number;
  openai_quota_headroom_weight: number;
  openai_prefer_soonest_reset_weight: number;

  // 余额、订阅到期与账号限额通知
  balance_low_notify_enabled: boolean;
  balance_low_notify_threshold: number;
  balance_low_notify_recharge_url: string;
  subscription_expiry_notify_enabled: boolean;
  account_quota_notify_enabled: boolean;
  account_quota_notify_emails: NotifyEmailEntry[];

  // Channel Monitor feature switch
  channel_monitor_enabled: boolean;
  channel_monitor_mode: 'v1' | 'v2';
  channel_monitor_default_interval_seconds: number;
  channel_monitor_hide_throughput: boolean;

  // Available Channels feature switch
  available_channels_enabled: boolean;

  // Affiliate (邀请返利) feature switch
  affiliate_enabled: boolean;

  // OpenAI fast/flex policy
  openai_fast_policy_settings?: OpenAIFastPolicySettings;

  // Allow user view error requests
  allow_user_view_error_requests: boolean;
}

export interface UpdateSettingsRequest {
  frontend_url?: string;
  totp_enabled?: boolean; // TOTP 双因素认证
  default_balance?: number;
  default_concurrency?: number;
  default_user_rpm_limit?: number;
  default_platform_quotas?: DefaultPlatformQuotasMap;
  site_name?: string;
  site_logo?: string;
  site_subtitle?: string;
  api_base_url?: string;
  contact_info?: string;
  doc_url?: string;
  home_content?: string;
  hide_ccs_import_button?: boolean;
  table_default_page_size?: number;
  table_page_size_options?: number[];
  backend_mode_enabled?: boolean;
  custom_menu_items?: CustomMenuItem[];
  custom_endpoints?: CustomEndpoint[];
  turnstile_enabled?: boolean;
  turnstile_site_key?: string;
  turnstile_secret_key?: string;
  api_key_acl_trust_forwarded_ip?: boolean;
  forwarded_client_ip_headers?: string[];
  linuxdo_connect_enabled?: boolean;
  linuxdo_connect_client_id?: string;
  linuxdo_connect_client_secret?: string;
  linuxdo_connect_redirect_url?: string;
  dingtalk_connect_enabled?: boolean;
  dingtalk_connect_client_id?: string;
  dingtalk_connect_client_secret?: string;
  dingtalk_connect_redirect_url?: string;
  dingtalk_connect_corp_restriction_policy?: string;
  dingtalk_connect_internal_corp_id?: string;
  dingtalk_connect_bypass_registration?: boolean;
  dingtalk_connect_sync_corp_email?: boolean;
  dingtalk_connect_sync_display_name?: boolean;
  dingtalk_connect_sync_dept?: boolean;
  dingtalk_connect_sync_corp_email_attr_key?: string;
  dingtalk_connect_sync_display_name_attr_key?: string;
  dingtalk_connect_sync_dept_attr_key?: string;
  dingtalk_connect_sync_corp_email_attr_name?: string;
  dingtalk_connect_sync_display_name_attr_name?: string;
  dingtalk_connect_sync_dept_attr_name?: string;
  wechat_connect_enabled?: boolean;
  wechat_connect_app_id?: string;
  wechat_connect_app_secret?: string;
  wechat_connect_open_app_id?: string;
  wechat_connect_open_app_secret?: string;
  wechat_connect_mp_app_id?: string;
  wechat_connect_mp_app_secret?: string;
  wechat_connect_mobile_app_id?: string;
  wechat_connect_mobile_app_secret?: string;
  wechat_connect_open_enabled?: boolean;
  wechat_connect_mp_enabled?: boolean;
  wechat_connect_mobile_enabled?: boolean;
  wechat_connect_mode?: string;
  wechat_connect_scopes?: string;
  wechat_connect_redirect_url?: string;
  wechat_connect_frontend_redirect_url?: string;
  oidc_connect_enabled?: boolean;
  oidc_connect_provider_name?: string;
  oidc_connect_client_id?: string;
  oidc_connect_client_secret?: string;
  oidc_connect_issuer_url?: string;
  oidc_connect_discovery_url?: string;
  oidc_connect_authorize_url?: string;
  oidc_connect_token_url?: string;
  oidc_connect_userinfo_url?: string;
  oidc_connect_jwks_url?: string;
  oidc_connect_scopes?: string;
  oidc_connect_redirect_url?: string;
  oidc_connect_frontend_redirect_url?: string;
  oidc_connect_token_auth_method?: string;
  oidc_connect_use_pkce?: boolean;
  oidc_connect_validate_id_token?: boolean;
  oidc_connect_allowed_signing_algs?: string;
  oidc_connect_clock_skew_seconds?: number;
  oidc_connect_require_email_verified?: boolean;
  oidc_connect_userinfo_email_path?: string;
  oidc_connect_userinfo_id_path?: string;
  oidc_connect_userinfo_username_path?: string;
  github_oauth_enabled?: boolean;
  github_oauth_client_id?: string;
  github_oauth_client_secret?: string;
  github_oauth_redirect_url?: string;
  github_oauth_frontend_redirect_url?: string;
  google_oauth_enabled?: boolean;
  google_oauth_client_id?: string;
  google_oauth_client_secret?: string;
  google_oauth_redirect_url?: string;
  google_oauth_frontend_redirect_url?: string;
  enable_model_fallback?: boolean;
  fallback_model_anthropic?: string;
  fallback_model_openai?: string;
  fallback_model_gemini?: string;
  fallback_model_antigravity?: string;
  enable_identity_patch?: boolean;
  identity_patch_prompt?: string;
  ops_monitoring_enabled?: boolean;
  ops_realtime_monitoring_enabled?: boolean;
  ops_query_mode_default?: "auto" | "raw" | "preagg" | string;
  ops_metrics_interval_seconds?: number;
  min_claude_code_version?: string;
  max_claude_code_version?: string;
  allow_ungrouped_key_scheduling?: boolean;
  gateway_custom_error_message?: string;
  enable_fingerprint_unification?: boolean;
  enable_metadata_passthrough?: boolean;
  enable_cch_signing?: boolean;
  enable_claude_oauth_system_prompt_injection?: boolean;
  claude_oauth_system_prompt?: string;
  claude_oauth_system_prompt_blocks?: string;
  enable_anthropic_cache_ttl_1h_injection?: boolean;
  rewrite_message_cache_control?: boolean;
  enable_client_dateline_normalization?: boolean;
  antigravity_user_agent_version?: string;
  openai_codex_user_agent?: string;
  openai_codex_client_version?: string;
  openai_codex_version_auto_sync_enabled?: boolean;
  openai_allow_claude_code_codex_plugin?: boolean;
  // Payment configuration
  payment_enabled?: boolean;
  risk_control_enabled?: boolean;

  // Cyber session block
  cyber_session_block_enabled?: boolean;
  cyber_session_block_ttl_seconds?: number;

  payment_min_amount?: number;
  payment_max_amount?: number;
  payment_daily_limit?: number;
  payment_order_timeout_minutes?: number;
  payment_max_pending_orders?: number;
  payment_enabled_types?: string[];
  payment_balance_disabled?: boolean;
  payment_balance_recharge_multiplier?: number;
  payment_recharge_fee_rate?: number;
  payment_load_balance_strategy?: string;
  payment_product_name_prefix?: string;
  payment_product_name_suffix?: string;
  payment_help_image_url?: string;
  payment_help_text?: string;
  payment_cancel_rate_limit_enabled?: boolean;
  payment_cancel_rate_limit_max?: number;
  payment_cancel_rate_limit_window?: number;
  payment_cancel_rate_limit_unit?: string;
  payment_cancel_rate_limit_window_mode?: string;
  payment_alipay_force_qrcode?: boolean;
  openai_advanced_scheduler_enabled?: boolean;
  grok_default_text_model?: string;
  grok_cross_client_model_map_enabled?: boolean;
  grok_scheduling_threshold_percent?: number;
  upstream_billing_auto_probe_enabled?: boolean;
  upstream_billing_probe_interval_minutes?: number;
  upstream_billing_probe_batch_size?: number;
  upstream_billing_probe_concurrency?: number;
  openai_upstream_cost_scheduling_enabled?: boolean;
  openai_upstream_cost_weight?: number;
  openai_quota_headroom_weight?: number;
  openai_prefer_soonest_reset_weight?: number;
  // 余额、订阅到期与账号限额通知
  balance_low_notify_enabled?: boolean;
  balance_low_notify_threshold?: number;
  balance_low_notify_recharge_url?: string;
  subscription_expiry_notify_enabled?: boolean;
  account_quota_notify_enabled?: boolean;
  account_quota_notify_emails?: NotifyEmailEntry[];

  // Channel Monitor feature switch
  channel_monitor_enabled?: boolean;
  channel_monitor_mode?: 'v1' | 'v2';
  channel_monitor_default_interval_seconds?: number;
  channel_monitor_hide_throughput?: boolean;

  // Available Channels feature switch
  available_channels_enabled?: boolean;

  // Affiliate (邀请返利) feature switch
  affiliate_enabled?: boolean;

  // OpenAI fast/flex policy
  openai_fast_policy_settings?: OpenAIFastPolicySettings;

  allow_user_view_error_requests?: boolean;
}

export interface GatewayLiteControlPlaneConfig {
  region: string;
  gateway_code: string;
  redis_prefix: string;
  admin_login_path: string;
  login_title: string;
  login_subtitle: string;
  control_plane_url: string;
  control_plane_token_configured: boolean;
  control_plane_timeout_ms: number;
  runtime_health_interval_seconds: number;
  runtime_active_window_seconds: number;
  config_sync_interval_seconds: number;
  cache_invalidation_interval_seconds: number;
  usage_queue_pending_alert_threshold: number;
  usage_queue_dead_alert_threshold: number;
  allow_live: boolean;
  config_path: string;
  restart_required: boolean;
}

export interface UpdateGatewayLiteControlPlaneConfig {
  region: string;
  gateway_code: string;
  redis_prefix: string;
  admin_login_path: string;
  login_title: string;
  login_subtitle: string;
  control_plane_url: string;
  control_plane_token?: string;
  control_plane_timeout_ms: number;
  runtime_health_interval_seconds: number;
  runtime_active_window_seconds: number;
  config_sync_interval_seconds: number;
  cache_invalidation_interval_seconds: number;
  usage_queue_pending_alert_threshold: number;
  usage_queue_dead_alert_threshold: number;
  allow_live: boolean;
}

export type BillingSettlementMode = "legacy" | "idempotent_v2";

/**
 * gateway-lite（轻量网关）不足一美分结算模式。
 * legacy_warning 由服务端给出，避免前端自行猜测旧模式的风险状态。
 */
export interface BillingSettlementSettings {
  idempotent_enabled: boolean;
  mode: BillingSettlementMode;
  legacy_warning: boolean;
}

export interface UpdateBillingSettlementSettings {
  idempotent_enabled: boolean;
}

/** 聚合分组节点总开关；保存后即时作用于管理端与用户请求。 */
export interface CompositeGroupsSettings {
  enabled: boolean;
}

export type CodexAdmissionMode = "audit_only" | "enforce";
export type CodexAdmissionClient = "desktop" | "cli" | "vscode" | "app_server";

export interface CodexClientAdmissionSettings {
  mode: CodexAdmissionMode;
  min_version: string;
  max_version: string;
  allowed_clients: CodexAdmissionClient[];
  denied_clients: CodexAdmissionClient[];
  app_server_enabled: boolean;
}

export interface CodexAdmissionCount {
  key: string;
  count: number;
}

export interface CodexAdmissionProfile {
  client: CodexAdmissionClient;
  version?: string;
  signals: string[];
  count: number;
}

export interface CodexClientAdmissionStats {
  started_at: string;
  inspected: number;
  observed: number;
  detected: number;
  audit_violations: number;
  denied: number;
  clients: CodexAdmissionCount[];
  reasons: CodexAdmissionCount[];
  signals: CodexAdmissionCount[];
  versions: CodexAdmissionCount[];
  profiles: CodexAdmissionProfile[];
}

export interface CodexClientAdmissionView {
  settings: CodexClientAdmissionSettings;
  stats: CodexClientAdmissionStats;
  known_clients: CodexAdmissionClient[];
  version_presets: string[];
}

/**
 * Get all system settings
 * @returns System settings
 */
export async function getSettings(): Promise<SystemSettings> {
  const { data } = await apiClient.get<SystemSettings>("/admin/settings");
  return data;
}

/**
 * Update system settings
 * @param settings - Partial settings to update
 * @returns Updated settings
 */
export async function updateSettings(
  settings: UpdateSettingsRequest,
): Promise<SystemSettings> {
  const { data } = await apiClient.put<SystemSettings>(
    "/admin/settings",
    settings,
  );
  return data;
}

export async function getGatewayLiteControlPlaneConfig(): Promise<GatewayLiteControlPlaneConfig> {
  const { data } = await apiClient.get<GatewayLiteControlPlaneConfig>(
    "/admin/gateway-lite/config",
  );
  return data;
}

export async function updateGatewayLiteControlPlaneConfig(
  settings: UpdateGatewayLiteControlPlaneConfig,
): Promise<GatewayLiteControlPlaneConfig> {
  const { data } = await apiClient.put<GatewayLiteControlPlaneConfig>(
    "/admin/gateway-lite/config",
    settings,
  );
  return data;
}

export async function getBillingSettlementSettings(): Promise<BillingSettlementSettings> {
  const { data } = await apiClient.get<BillingSettlementSettings>(
    "/admin/settings/billing-settlement",
  );
  return data;
}

export async function updateBillingSettlementSettings(
  settings: UpdateBillingSettlementSettings,
): Promise<BillingSettlementSettings> {
  const { data } = await apiClient.put<BillingSettlementSettings>(
    "/admin/settings/billing-settlement",
    settings,
  );
  return data;
}

export async function getCompositeGroupsSettings(): Promise<CompositeGroupsSettings> {
  const { data } = await apiClient.get<CompositeGroupsSettings>(
    "/admin/settings/composite-groups",
  );
  return data;
}

export async function updateCompositeGroupsSettings(
  settings: CompositeGroupsSettings,
): Promise<CompositeGroupsSettings> {
  const { data } = await apiClient.put<CompositeGroupsSettings>(
    "/admin/settings/composite-groups",
    settings,
  );
  return data;
}

export async function getCodexClientAdmissionSettings(): Promise<CodexClientAdmissionView> {
  const { data } = await apiClient.get<CodexClientAdmissionView>(
    "/admin/settings/codex-client-admission",
  );
  return data;
}

export async function updateCodexClientAdmissionSettings(
  settings: CodexClientAdmissionSettings,
): Promise<CodexClientAdmissionView> {
  const { data } = await apiClient.put<CodexClientAdmissionView>(
    "/admin/settings/codex-client-admission",
    settings,
  );
  return data;
}

/**
 * Admin API Key status response
 */
export interface AdminApiKeyStatus {
  exists: boolean;
  masked_key: string;
}

/**
 * Get admin API key status
 * @returns Status indicating if key exists and masked version
 */
export async function getAdminApiKey(): Promise<AdminApiKeyStatus> {
  const { data } = await apiClient.get<AdminApiKeyStatus>(
    "/admin/settings/admin-api-key",
  );
  return data;
}

/**
 * Reveal the existing admin API key on explicit administrator action.
 * The server only accepts an authenticated admin browser session and marks
 * the response as non-cacheable.
 */
export async function revealAdminApiKey(): Promise<{ key: string }> {
  const { data } = await apiClient.post<{ key: string }>(
    "/admin/settings/admin-api-key/reveal",
  );
  return data;
}

/**
 * Regenerate admin API key
 * @returns The new full API key
 */
export async function regenerateAdminApiKey(): Promise<{ key: string }> {
  const { data } = await apiClient.post<{ key: string }>(
    "/admin/settings/admin-api-key/regenerate",
  );
  return data;
}

/**
 * Delete admin API key
 * @returns Success message
 */
export async function deleteAdminApiKey(): Promise<{ message: string }> {
  const { data } = await apiClient.delete<{ message: string }>(
    "/admin/settings/admin-api-key",
  );
  return data;
}

// ==================== Overload Cooldown Settings ====================

/**
 * Overload cooldown settings interface (529 handling)
 */
export interface OverloadCooldownSettings {
  enabled: boolean;
  cooldown_minutes: number;
}

export async function getOverloadCooldownSettings(): Promise<OverloadCooldownSettings> {
  const { data } = await apiClient.get<OverloadCooldownSettings>(
    "/admin/settings/overload-cooldown",
  );
  return data;
}

export async function updateOverloadCooldownSettings(
  settings: OverloadCooldownSettings,
): Promise<OverloadCooldownSettings> {
  const { data } = await apiClient.put<OverloadCooldownSettings>(
    "/admin/settings/overload-cooldown",
    settings,
  );
  return data;
}

// ==================== 429 Rate Limit Cooldown Settings ====================

export interface RateLimit429CooldownSettings {
  enabled: boolean;
  cooldown_seconds: number;
}

export async function getRateLimit429CooldownSettings(): Promise<RateLimit429CooldownSettings> {
  const { data } = await apiClient.get<RateLimit429CooldownSettings>(
    "/admin/settings/rate-limit-429-cooldown",
  );
  return data;
}

export async function updateRateLimit429CooldownSettings(
  settings: RateLimit429CooldownSettings,
): Promise<RateLimit429CooldownSettings> {
  const { data } = await apiClient.put<RateLimit429CooldownSettings>(
    "/admin/settings/rate-limit-429-cooldown",
    settings,
  );
  return data;
}

// ==================== Fallback Queue Settings ====================

export interface FallbackQueueSettings {
  enabled: boolean;
  max_waiting: number;
  wait_timeout_seconds: number;
}

export async function getFallbackQueueSettings(): Promise<FallbackQueueSettings> {
  const { data } = await apiClient.get<FallbackQueueSettings>(
    "/admin/settings/fallback-queue",
  );
  return data;
}

export async function updateFallbackQueueSettings(
  settings: FallbackQueueSettings,
): Promise<FallbackQueueSettings> {
  const { data } = await apiClient.put<FallbackQueueSettings>(
    "/admin/settings/fallback-queue",
    settings,
  );
  return data;
}

// ==================== Stream Timeout Settings ====================

/**
 * Stream timeout settings interface
 */
export interface StreamTimeoutSettings {
  enabled: boolean;
  action: "temp_unsched" | "error" | "none";
  temp_unsched_minutes: number;
  threshold_count: number;
  threshold_window_minutes: number;
}

/**
 * Get stream timeout settings
 * @returns Stream timeout settings
 */
export async function getStreamTimeoutSettings(): Promise<StreamTimeoutSettings> {
  const { data } = await apiClient.get<StreamTimeoutSettings>(
    "/admin/settings/stream-timeout",
  );
  return data;
}

/**
 * Update stream timeout settings
 * @param settings - Stream timeout settings to update
 * @returns Updated settings
 */
export async function updateStreamTimeoutSettings(
  settings: StreamTimeoutSettings,
): Promise<StreamTimeoutSettings> {
  const { data } = await apiClient.put<StreamTimeoutSettings>(
    "/admin/settings/stream-timeout",
    settings,
  );
  return data;
}

// ==================== Rectifier Settings ====================

/**
 * Rectifier settings interface
 */
export interface RectifierSettings {
  enabled: boolean;
  thinking_signature_enabled: boolean;
  thinking_budget_enabled: boolean;
  apikey_signature_enabled: boolean;
  apikey_signature_patterns: string[];
}

/**
 * Get rectifier settings
 * @returns Rectifier settings
 */
export async function getRectifierSettings(): Promise<RectifierSettings> {
  const { data } = await apiClient.get<RectifierSettings>(
    "/admin/settings/rectifier",
  );
  return data;
}

/**
 * Update rectifier settings
 * @param settings - Rectifier settings to update
 * @returns Updated settings
 */
export async function updateRectifierSettings(
  settings: RectifierSettings,
): Promise<RectifierSettings> {
  const { data } = await apiClient.put<RectifierSettings>(
    "/admin/settings/rectifier",
    settings,
  );
  return data;
}

// ==================== OpenAI Fast Policy Settings ====================

/**
 * OpenAI fast/flex policy rule interface.
 * Matches backend dto.OpenAIFastPolicyRule.
 */
export interface OpenAIFastPolicyRule {
  id?: string;
  service_tier: "all" | "priority" | "flex";
  action: "pass" | "filter" | "block" | "force_priority";
  scope: "all" | "oauth" | "apikey" | "bedrock";
  user_ids?: number[];
  error_message?: string;
  model_whitelist?: string[];
  fallback_action?: "pass" | "filter" | "block" | "force_priority";
  fallback_error_message?: string;
}

/**
 * OpenAI fast/flex policy settings interface.
 */
export interface OpenAIFastPolicySettings {
  version?: number;
  priority_detection_by_request_enabled: boolean;
  priority_detection_by_response_enabled: boolean;
  rules: OpenAIFastPolicyRule[];
}

// ==================== Beta Policy Settings ====================

/**
 * Beta policy rule interface
 */
export interface BetaPolicyRule {
  beta_token: string;
  action: "pass" | "filter" | "block";
  scope: "all" | "oauth" | "apikey" | "bedrock";
  error_message?: string;
  model_whitelist?: string[];
  fallback_action?: "pass" | "filter" | "block";
  fallback_error_message?: string;
}

/**
 * Beta policy settings interface
 */
export interface BetaPolicySettings {
  rules: BetaPolicyRule[];
}

/**
 * Get beta policy settings
 * @returns Beta policy settings
 */
export async function getBetaPolicySettings(): Promise<BetaPolicySettings> {
  const { data } = await apiClient.get<BetaPolicySettings>(
    "/admin/settings/beta-policy",
  );
  return data;
}

/**
 * Update beta policy settings
 * @param settings - Beta policy settings to update
 * @returns Updated settings
 */
export async function updateBetaPolicySettings(
  settings: BetaPolicySettings,
): Promise<BetaPolicySettings> {
  const { data } = await apiClient.put<BetaPolicySettings>(
    "/admin/settings/beta-policy",
    settings,
  );
  return data;
}

// --- Web Search Emulation Config ---

export interface WebSearchProviderConfig {
  type: "brave" | "tavily";
  api_key: string;
  api_key_configured: boolean;
  quota_limit: number | null;
  subscribed_at: number | null;
  quota_used?: number;
  proxy_id: number | null;
  expires_at: number | null;
}

export interface WebSearchEmulationConfig {
  enabled: boolean;
  providers: WebSearchProviderConfig[];
}

export interface WebSearchTestResult {
  provider: string;
  results: { url: string; title: string; snippet: string; page_age?: string }[];
  query: string;
}

export async function getWebSearchEmulationConfig(): Promise<WebSearchEmulationConfig> {
  const { data } = await apiClient.get<WebSearchEmulationConfig>(
    "/admin/settings/web-search-emulation",
  );
  return data;
}

export async function updateWebSearchEmulationConfig(
  config: WebSearchEmulationConfig,
): Promise<WebSearchEmulationConfig> {
  const { data } = await apiClient.put<WebSearchEmulationConfig>(
    "/admin/settings/web-search-emulation",
    config,
  );
  return data;
}

export async function testWebSearchEmulation(
  query: string,
): Promise<WebSearchTestResult> {
  const { data } = await apiClient.post<WebSearchTestResult>(
    "/admin/settings/web-search-emulation/test",
    { query },
  );
  return data;
}

export async function resetWebSearchUsage(payload: {
  provider_type: string;
}): Promise<void> {
  await apiClient.post(
    "/admin/settings/web-search-emulation/reset-usage",
    payload,
  );
}

export const settingsAPI = {
  getSettings,
  updateSettings,
  getGatewayLiteControlPlaneConfig,
  updateGatewayLiteControlPlaneConfig,
  getBillingSettlementSettings,
  updateBillingSettlementSettings,
  getCompositeGroupsSettings,
  updateCompositeGroupsSettings,
  getCodexClientAdmissionSettings,
  updateCodexClientAdmissionSettings,
  getAdminApiKey,
  revealAdminApiKey,
  regenerateAdminApiKey,
  deleteAdminApiKey,
  getOverloadCooldownSettings,
  updateOverloadCooldownSettings,
  getRateLimit429CooldownSettings,
  updateRateLimit429CooldownSettings,
  getFallbackQueueSettings,
  updateFallbackQueueSettings,
  getStreamTimeoutSettings,
  updateStreamTimeoutSettings,
  getRectifierSettings,
  updateRectifierSettings,
  getBetaPolicySettings,
  updateBetaPolicySettings,
  getWebSearchEmulationConfig,
  updateWebSearchEmulationConfig,
  testWebSearchEmulation,
  resetWebSearchUsage,
};

export default settingsAPI;
