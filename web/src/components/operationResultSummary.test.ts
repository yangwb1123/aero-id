import { describe, expect, it } from "vitest";
import {
  compensationState,
  operationAction,
  operationCode,
  operationResult,
  operationSource,
  operationState,
  operationSteps,
  operationStepState,
} from "./operationResultSummary";
import { operationAuditItem } from "./operationAuditSummary";

describe("bounded explicit Operation result mapping", () => {
  it.each([undefined, null, false, "1", -1, 1.5, Number.MAX_SAFE_INTEGER + 1])(
    "does not coerce invalid/missing count %j into zero",
    (count) => {
      const mapped = operationResult({
        action: "summary",
        source: "aero-vault",
        result: { count },
      });
      expect(
        mapped.fields.find((field) => field.label === "摘要投影条数")?.value,
      ).toBe(count == null ? "来源未提供" : "字段无效（未展示）");
    },
  );
  it("honors reported zero, incomplete status, stale application and only approved same-source skipped scope", () => {
    const mapped = operationResult({
      action: "summary",
      source: "aero-vault",
      result: {
        status: "partially_completed",
        count: 0,
        complete: false,
        application_outcome: "stale_ignored",
        skipped_datasets: ["aero-vault.buckets"],
        arbitrary: "UNAPPROVED",
      },
    });
    expect(mapped.incomplete).toBe(true);
    expect(mapped.fields).toContainEqual({ label: "摘要投影条数", value: "0" });
    expect(mapped.fields.map((field) => field.value).join(" ")).toContain(
      "旧版本摘要已忽略",
    );
    expect(mapped.fields.map((field) => field.value).join(" ")).toContain(
      "Vault 桶引用",
    );
    expect(JSON.stringify(mapped)).not.toContain("UNAPPROVED");
  });
  it.each(
    [
      ["aero-im.workspaces"],
      ["unknown.private"],
      Array.from({ length: 12 }, () => "aero-vault.usage"),
      ["aero-vault.usage", "aero-vault.usage"],
      "bad",
    ].map((value) => ({ value })),
  )(
    "rejects unsupported skipped scope $value without revealing it",
    ({ value: skipped_datasets }) => {
      const mapped = operationResult({
        action: "summary",
        source: "aero-vault",
        result: { skipped_datasets },
      });
      expect(mapped.fields).toContainEqual({
        label: "来源报告跳过范围",
        value: "跳过范围字段无效（未展示）",
      });
    },
  );
  it("keeps source marker distinct from durable step state and maps no arbitrary command result", () => {
    const step = {
      status: "unknown",
      action: "erase",
      result: { status: "completed", count: 999, content: "UNAPPROVED" },
    };
    expect(operationStepState(step.status).title).toBe("步骤结果未知");
    expect(operationResult(step).fields).toEqual([
      { label: "来源结果标记", value: "来源报告完成" },
    ]);
    expect(operationState("unknown").title).toBe("结果未知");
    expect(
      operationResult({ action: "erase", result: { complete: false } })
        .incomplete,
    ).toBe(false);
  });
  it("separates forward and compensation results and IDs without fallback", () => {
    const step = {
      action: "summary",
      result: { count: 2 },
      compensation_action: "summary",
      compensation_status: "succeeded",
      compensation_result: { count: 0, complete: true },
    };
    expect(operationResult(step, true).fields).toContainEqual({
      label: "摘要投影条数",
      value: "0",
    });
    expect(compensationState(step).title).toBe("补偿 · 步骤成功");
    expect(compensationState(step).detail).toContain("不代表整体回滚");
  });
  it.each(
    [[], [null], Array.from({ length: 33 }, () => ({}))].map((value) => ({
      value,
    })),
  )("distinguishes empty and bad/beyond-budget steps $value", ({ value }) => {
    const steps = operationSteps(value);
    expect(steps.issue === undefined).toBe(value.length === 0);
    expect(steps.items).toHaveLength(0);
  });
  it("preserves at most the 32 returned records in order without inventing chronology", () => {
    const value = Array.from({ length: 32 }, (_, index) => ({
      step_key: String(index),
    }));
    expect(operationSteps(value).items).toEqual(value);
  });
  it.each([null, ["summary"], {}, 1, "unapproved-action"])(
    "does not stringify unknown action/source %j",
    (value) => {
      expect(operationAction(value)).not.toContain("unapproved-action");
      expect(operationSource(value)).toBe("来源未提供或无效");
      expect(operationStepState(value).tone).toBe("warning");
    },
  );
  it("does not render arbitrary error text as a code", () => {
    expect(operationCode("source.operation_pending")).toBe(
      "source.operation_pending",
    );
    expect(operationCode("https://private.invalid/secret")).toBeUndefined();
    expect(operationCode({ password: "never" })).toBeUndefined();
  });
});

describe("readable local audit facts are not live grants or a merged step history", () => {
  it("uses explicit forward/compensation attempt slots and source ID only when that fact contains them", () => {
    const item = operationAuditItem(
      {
        action: "operation.compensation.reconciled",
        occurred_at: "2026-10-16T09:00:00Z",
        payload: {
          step_key: "original-step",
          status: "unknown",
          compensation_attempt: 2,
          message_body: "UNAPPROVED",
        },
      },
      1,
    );
    expect(item.description).toContain("补偿对账结果已记录");
    expect(item.description).toContain("补偿尝试：2");
    expect(item.description).not.toContain("正向尝试");
    expect(item.description).not.toContain("来源操作 ID");
    expect(item.description).not.toContain("UNAPPROVED");
    expect(item.variant).toBe("warning");
  });
  it("uses the repository expiry fact's attempt as a compensation counter, not a forward attempt", () => {
    const item = operationAuditItem(
      {
        action: "operation.compensation.expired",
        payload: { step_id: "original-step-id", status: "unknown", attempt: 3 },
      },
      0,
    );
    expect(item.description).toContain("补偿尝试：3");
    expect(item.description).toContain("步骤 ID：original-step-id");
    expect(item.description).not.toContain("正向尝试");
  });
  it("can show recorded creation scope without lending it to current action admission", () => {
    const item = operationAuditItem(
      {
        action: "operation.requested",
        payload: {
          steps: [
            {
              step_key: "summary",
              source: "aero-vault",
              action: "summary",
              datasets: ["aero-vault.usage"],
            },
          ],
        },
      },
      0,
    );
    expect(item.description).toContain("Vault 用量");
    expect(item.title).toBe("operation.requested");
  });
  it("does not infer success from the word finished or unknown actions", () => {
    expect(
      operationAuditItem(
        { action: "operation.step.finished", payload: { status: "failed" } },
        0,
      ).variant,
    ).toBe("danger");
    expect(
      operationAuditItem(
        {
          action: "unmapped.completed",
          payload: { status: "completed", arbitrary: "UNAPPROVED" },
        },
        0,
      ).variant,
    ).toBe("info");
    expect(
      operationAuditItem(
        { action: "unmapped.completed", payload: { arbitrary: "UNAPPROVED" } },
        0,
      ).description,
    ).not.toContain("UNAPPROVED");
  });
});
