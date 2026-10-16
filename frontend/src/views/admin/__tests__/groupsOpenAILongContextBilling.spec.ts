import { describe, expect, it } from "vitest";

import type { AdminGroup } from "@/types";
import { summarizeOpenAILongContextBillingAccounts } from "../groupsOpenAILongContextBilling";

const group = (
  id: number,
  longContextAccountCount: number,
  standardAccountCount: number,
): AdminGroup =>
  ({
    id,
    openai_long_context_billing_account_count: longContextAccountCount,
    openai_standard_billing_account_count: standardAccountCount,
  }) as AdminGroup;

describe("summarizeOpenAILongContextBillingAccounts", () => {
  it("uses selected source groups while creating or replacing bindings", () => {
    expect(
      summarizeOpenAILongContextBillingAccounts(
        [group(1, 2, 1), group(2, 0, 3), group(3, 5, 5)],
        [1, 2],
      ),
    ).toEqual({
      longContextAccountCount: 2,
      standardAccountCount: 4,
      canEnable: true,
      usesCopiedGroups: true,
    });
  });

  it("uses the current group when edit does not replace account bindings", () => {
    expect(
      summarizeOpenAILongContextBillingAccounts(
        [group(1, 9, 9)],
        [],
        group(7, 0, 2),
      ),
    ).toEqual({
      longContextAccountCount: 0,
      standardAccountCount: 2,
      canEnable: false,
      usesCopiedGroups: false,
    });
  });

  it("treats missing and invalid counts as zero", () => {
    const source = group(1, Number.NaN, -3);
    expect(
      summarizeOpenAILongContextBillingAccounts([source], [1]),
    ).toEqual({
      longContextAccountCount: 0,
      standardAccountCount: 0,
      canEnable: false,
      usesCopiedGroups: true,
    });
  });
});
