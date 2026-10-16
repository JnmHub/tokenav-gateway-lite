import { mount } from "@vue/test-utils";
import { describe, expect, it, vi } from "vitest";

import GroupPeakRatePolicyFields from "../GroupPeakRatePolicyFields.vue";

vi.mock("vue-i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

const mountPolicy = (overrides: Record<string, unknown> = {}) =>
  mount(GroupPeakRatePolicyFields, {
    props: {
      enabled: false,
      start: "18:00",
      end: "22:00",
      multiplier: 1,
      timezone: "Asia/Shanghai",
      ...overrides,
    },
  });

describe("GroupPeakRatePolicyFields", () => {
  it("defaults to a collapsed, disabled policy and only emits the switch change", async () => {
    const wrapper = mountPolicy();

    expect(wrapper.find('[data-testid="peak-rate-fields"]').exists()).toBe(false);
    await wrapper.get('[data-testid="peak-rate-toggle"]').trigger("click");

    expect(wrapper.emitted("update:enabled")).toEqual([[true]]);
  });

  it("uses clickable timezone and multiplier presets", async () => {
    const wrapper = mountPolicy({ enabled: true });

    await wrapper.get('[data-testid="peak-timezone-Asia/Tokyo"]').trigger("click");
    await wrapper.get('[data-testid="peak-multiplier-1.5"]').trigger("click");

    expect(wrapper.emitted("update:timezone")).toEqual([["Asia/Tokyo"]]);
    expect(wrapper.emitted("update:multiplier")).toEqual([[1.5]]);
  });

  it("emits native time values and explains a cross-midnight window", async () => {
    const wrapper = mountPolicy({ enabled: true, start: "22:00", end: "06:00" });

    expect(wrapper.find('[data-testid="peak-rate-cross-midnight-hint"]').exists()).toBe(true);
    await wrapper.get('[data-testid="peak-rate-start"]').setValue("23:30");
    await wrapper.get('[data-testid="peak-rate-end"]').setValue("07:15");

    expect(wrapper.emitted("update:start")).toEqual([["23:30"]]);
    expect(wrapper.emitted("update:end")).toEqual([["07:15"]]);
  });
});
