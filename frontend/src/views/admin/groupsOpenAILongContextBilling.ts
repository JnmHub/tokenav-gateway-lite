import type { AdminGroup } from "@/types";

export interface OpenAILongContextBillingAccountSummary {
  longContextAccountCount: number;
  standardAccountCount: number;
  canEnable: boolean;
  usesCopiedGroups: boolean;
}

const nonNegativeCount = (value: number | null | undefined): number => {
  if (!Number.isFinite(value)) return 0;
  return Math.max(0, Number(value));
};

// 创建和“替换账号来源”场景只能根据来源分组的统计做前端预判；
// 保存时后端会再按目标分组的 OAuth、隐私等规则检查真实账号，不能只信任该估算。
export const summarizeOpenAILongContextBillingAccounts = (
  groups: AdminGroup[],
  copiedGroupIDs: number[],
  currentGroup?: AdminGroup | null,
): OpenAILongContextBillingAccountSummary => {
  const selectedIDs = new Set(
    copiedGroupIDs.filter((id) => Number.isInteger(id) && id > 0),
  );
  const usesCopiedGroups = selectedIDs.size > 0;
  const sources = usesCopiedGroups
    ? groups.filter((group) => selectedIDs.has(group.id))
    : currentGroup
      ? [currentGroup]
      : [];

  const longContextAccountCount = sources.reduce(
    (total, group) =>
      total + nonNegativeCount(group.openai_long_context_billing_account_count),
    0,
  );
  const standardAccountCount = sources.reduce(
    (total, group) =>
      total + nonNegativeCount(group.openai_standard_billing_account_count),
    0,
  );

  return {
    longContextAccountCount,
    standardAccountCount,
    canEnable: longContextAccountCount > 0,
    usesCopiedGroups,
  };
};
