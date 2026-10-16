import { describe, expect, it } from "vitest";
import {
  createReasoningEffortMappingRow,
  normalizeReasoningEffortForPlatform,
  reasoningEffortMappingsToAPI,
  reasoningEffortMappingsToRows,
  reasoningEffortOptionsForPlatform,
  validateReasoningEffortMappings,
} from "../groupsReasoningEffort";

describe("groupsReasoningEffort", () => {
  it("exposes fixed OpenAI choices for OpenAI and composite groups", () => {
    expect(reasoningEffortOptionsForPlatform("openai").map((option) => option.value)).toEqual([
      "minimal", "low", "medium", "high", "xhigh", "max",
    ]);
    expect(reasoningEffortOptionsForPlatform("composite").map((option) => option.value)).toEqual([
      "minimal", "low", "medium", "high", "xhigh", "max",
    ]);
    expect(reasoningEffortOptionsForPlatform("grok")).toEqual([]);
  });

  it("hydrates supported rows and drops stale custom values", () => {
    const rows = reasoningEffortMappingsToRows([
      { from: " max ", to: " xhigh " },
      { from: "ultra", to: "high" },
    ]);
    expect(reasoningEffortMappingsToAPI(rows)).toEqual([{ from: "max", to: "xhigh" }]);
    expect(normalizeReasoningEffortForPlatform("grok", "max")).toBe("");
  });

  it("rejects incomplete and duplicate source rows", () => {
    const incomplete = createReasoningEffortMappingRow({ to: "low" });
    const first = createReasoningEffortMappingRow({ from: "max", to: "xhigh" });
    const second = createReasoningEffortMappingRow({ from: "max", to: "high" });
    expect(validateReasoningEffortMappings([incomplete])[incomplete.id]?.from).toBe("fromRequired");
    expect(validateReasoningEffortMappings([first, second])[first.id]?.from).toBe("duplicateFrom");
  });
});
