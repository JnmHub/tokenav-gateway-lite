import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import { describe, expect, it } from "vitest";

const currentDir = dirname(fileURLToPath(import.meta.url));
const source = readFileSync(resolve(currentDir, "../GroupsView.vue"), "utf8");

describe("GroupsView composite group runtime gate", () => {
  it("disables the composite option when the runtime switch is off", () => {
    expect(source).toContain("disabled: !compositeGroupsEnabled.value");
    expect(source).toContain(
      "runtimeSummary.composite_groups_enabled === true",
    );
  });

  it("blocks create and update before sending an unavailable composite group", () => {
    expect(
      source.match(
        /platform === "composite" && !compositeGroupsEnabled\.value/g,
      ),
    ).toHaveLength(2);
    expect(source).toContain("admin.groups.compositeDisabledSaveHint");
  });
});
