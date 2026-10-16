import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { ref } from "vue";
import CodexClientAdmissionCard from "../CodexClientAdmissionCard.vue";

const mocks = vi.hoisted(() => ({
  get: vi.fn(),
  update: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn(),
}));

vi.mock("@/api", () => ({
  adminAPI: {
    settings: {
      getCodexClientAdmissionSettings: mocks.get,
      updateCodexClientAdmissionSettings: mocks.update,
    },
  },
}));

vi.mock("@/stores/app", () => ({
  useAppStore: () => ({ showSuccess: mocks.showSuccess, showError: mocks.showError }),
}));

vi.mock("vue-i18n", () => ({
  useI18n: () => ({ locale: ref("zh-CN") }),
}));

const response = () => ({
  settings: {
    mode: "audit_only" as const,
    min_version: "",
    max_version: "",
    allowed_clients: ["desktop", "cli", "vscode", "app_server"] as const,
    denied_clients: [] as const,
    app_server_enabled: false,
  },
  stats: {
    started_at: "2026-08-01T08:00:00Z",
    inspected: 20,
    observed: 10,
    detected: 8,
    audit_violations: 2,
    denied: 0,
    clients: [{ key: "cli", count: 8 }],
    reasons: [{ key: "allowed", count: 6 }, { key: "version_too_low", count: 2 }],
    signals: [{ key: "user_agent", count: 8 }, { key: "originator", count: 8 }],
    versions: [{ key: "0.144.1", count: 8 }],
    profiles: [{ client: "cli" as const, version: "0.144.1", signals: ["user_agent", "originator"], count: 8 }],
  },
  known_clients: ["desktop", "cli", "vscode", "app_server"] as const,
  version_presets: ["", "0.144.1", "0.150.0"],
});

describe("CodexClientAdmissionCard", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.get.mockResolvedValue(response());
    mocks.update.mockImplementation(async (settings) => ({ ...response(), settings }));
  });

  it("默认显示只审计并展示脱敏聚合", async () => {
    const wrapper = mount(CodexClientAdmissionCard, { global: { stubs: { Toggle: true } } });
    await flushPromises();

    expect(mocks.get).toHaveBeenCalledTimes(1);
    expect(wrapper.get('[data-testid="codex-mode-audit"]').classes()).toContain("active");
    expect(wrapper.text()).toContain("可信识别");
    expect(wrapper.text()).toContain("v0.144.1");
    expect(wrapper.get('[data-testid="codex-profile-list"]').text()).toContain("Codex CLI");
    expect(wrapper.get('[data-testid="codex-profile-list"]').text()).toContain("User-Agent");
    expect(wrapper.get('[data-testid="codex-profile-list"]').text()).toContain("Originator");
    expect(wrapper.text()).toContain("codex_cli_only");
    expect(wrapper.get("toggle-stub").attributes("aria-label")).toBe("允许未列名 App Server");
  });

  it("按计数展示前六个脱敏识别组合并适配未知版本", async () => {
    const profiles = [
      { client: "cli" as const, version: "0.146.0", signals: ["user_agent", "codex_path"], count: 3 },
      { client: "desktop" as const, version: "0.146.0", signals: ["user_agent", "originator"], count: 20 },
      { client: "vscode" as const, version: "0.146.0", signals: ["user_agent", "codex_header"], count: 12 },
      { client: "app_server" as const, version: "", signals: ["originator", "codex_header"], count: 15 },
      { client: "cli" as const, version: "0.144.3", signals: ["user_agent", "codex_path"], count: 8 },
      { client: "desktop" as const, version: "0.142.5", signals: ["user_agent", "codex_path"], count: 5 },
      { client: "cli" as const, version: "0.98.0", signals: ["user_agent", "codex_path"], count: 1 },
    ];
    mocks.get.mockResolvedValue({ ...response(), stats: { ...response().stats, profiles } });

    const wrapper = mount(CodexClientAdmissionCard, { global: { stubs: { Toggle: true } } });
    await flushPromises();

    const cards = wrapper.findAll('[data-testid^="codex-profile-"]:not([data-testid="codex-profile-list"])');
    expect(cards).toHaveLength(6);
    expect(cards[0].text()).toContain("Codex Desktop");
    expect(cards[0].text()).toContain("20");
    expect(wrapper.get('[data-testid="codex-profile-list"]').text()).toContain("版本未知");
    expect(wrapper.get('[data-testid="codex-profile-list"]').text()).not.toContain("v0.98.0");
  });

  it("用固定按钮切换全局拒绝并保存，不要求手写规则", async () => {
    const wrapper = mount(CodexClientAdmissionCard, { global: { stubs: { Toggle: true } } });
    await flushPromises();

    await wrapper.get('[data-testid="codex-mode-enforce"]').trigger("click");
    await wrapper.get('[data-testid="codex-client-cli-deny"]').trigger("click");
    await wrapper.get('[data-testid="codex-admission-save"]').trigger("click");
    await flushPromises();

    expect(mocks.update).toHaveBeenCalledWith(expect.objectContaining({
      mode: "enforce",
      denied_clients: ["cli"],
      allowed_clients: ["desktop", "vscode", "app_server"],
    }));
    expect(mocks.showSuccess).toHaveBeenCalledTimes(1);
  });

  it("客户端不在允许名单时显示真实拒绝状态", async () => {
    mocks.get.mockResolvedValue({
      ...response(),
      settings: {
        ...response().settings,
        allowed_clients: ["desktop", "vscode", "app_server"],
        denied_clients: [],
      },
    });

    const wrapper = mount(CodexClientAdmissionCard, { global: { stubs: { Toggle: true } } });
    await flushPromises();

    expect(wrapper.get('[data-testid="codex-client-cli-deny"]').classes()).toContain("active");
    expect(wrapper.get('[data-testid="codex-client-cli-allow"]').classes()).not.toContain("active");
  });
});
