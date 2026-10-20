import { describe, expect, it } from "vitest";
import { jobSourceResults, jobOutcome } from "./jobResultSummary";
import { jobDownloadState, jobExportMetadata } from "./jobExportSummary";
import {
  jobCount,
  jobDatasets,
  jobScope,
  jobTimeValue,
} from "./jobSummaryFields";
import type { JsonRecord } from "../types";
const now = Date.parse("2026-10-16T10:00:00Z");
const base: JsonRecord = {
  job_id: "01800000-0000-7000-8000-000000000003",
  account_id: "01800000-0000-7000-8000-000000000002",
  tenant_id: "tenant-demo",
  job_type: "export",
  status: "completed",
  request: { datasets: ["aero-vault.usage"] },
};
describe("bounded approved task result mapping", () => {
  it.each([
    undefined,
    null,
    {},
    "aero-vault.usage",
    [],
    ["__proto__"],
    ["unknown.body"],
    ["aero-vault.usage", "aero-vault.usage"],
    Array(12).fill("aero-vault.usage"),
  ])("does not turn an unconfirmed scope into all datasets: %j", (datasets) => {
    expect(jobScope({ request: { datasets } }).issue).toBeTruthy();
  });
  it("only accepts unique names from the current registry", () => {
    expect(jobDatasets(["aero-im.workspaces", "aero-vault.usage"])).toEqual([
      "aero-im.workspaces",
      "aero-vault.usage",
    ]);
  });
  it.each([
    [0, "0"],
    [3, "3"],
    [undefined, "来源未提供"],
    [null, "来源未提供"],
    ["0", "字段无效（未展示）"],
    [-1, "字段无效（未展示）"],
    [false, "字段无效（未展示）"],
    [Number.MAX_SAFE_INTEGER + 1, "字段无效（未展示）"],
  ])("does not fabricate or coerce counts: %j", (input, output) =>
    expect(jobCount(input)).toBe(output),
  );
  it.each([
    "",
    "2026-02-30T00:00:00Z",
    "2026-10-16",
    "2026-10-16T24:00:00Z",
    "2026-10-16T09:00:00",
    123,
  ])("rejects unconfirmed calendar or zoned time: %j", (value) =>
    expect(jobTimeValue(value)).toBeUndefined(),
  );
  it("maps only selected sources and known source summary fields", () => {
    const rows = jobSourceResults({
      ...base,
      job_type: "sync",
      result: {
        sources: {
          "aero-vault": {
            status: "partial",
            count: 0,
            complete: false,
            skipped_datasets: ["aero-vault.usage"],
            result: { body: "MUST-NOT-RENDER" },
          },
          "aero-im": { status: "synced", count: 20 },
          arbitrary: { status: "synced", count: 20 },
        },
      },
    });
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({
      source: "aero-vault",
      title: "已应用部分摘要",
      count: "0",
      complete: "来源报告摘要不完整",
      skipped: "Vault 用量",
    });
    expect(JSON.stringify(rows)).not.toContain("MUST-NOT-RENDER");
  });
  it("does not let inherited property names or an erase-only status claim summary success", () => {
    for (const status of ["toString", "__proto__", ["synced"], "succeeded"]) {
      expect(
        jobSourceResults({
          ...base,
          job_type: "sync",
          result: { sources: { "aero-vault": { status } } },
        })[0].title,
      ).toBe("未识别的来源状态");
    }
    expect(
      jobOutcome({ status: "toString", job_type: "__proto__" }).status.title,
    ).toBe("未识别状态");
  });
  it("does not imply a missing source result is a failed or unexecuted source", () => {
    expect(jobSourceResults({ ...base, job_type: "sync" })[0]).toMatchObject({
      missing: true,
      title: "未报告来源结果",
    });
  });
  it("keeps reconciliation counts as projection counts and preserves the original source operation ID for erase", () => {
    const sources = {
      "aero-vault": {
        status: "reconciled",
        outcome: "stale_ignored",
        drift_count: 2,
        repaired_count: 0,
      },
    };
    expect(
      jobSourceResults({
        ...base,
        job_type: "reconcile",
        result: { sources },
      })[0],
    ).toMatchObject({ drift: "2", repaired: "0", outcome: "旧版本已忽略" });
    sources["aero-vault"] = {
      status: "synced",
      outcome: "",
      drift_count: 0,
      repaired_count: 0,
    };
    expect(
      jobSourceResults({
        ...base,
        job_type: "erase",
        result: {
          sources: {
            "aero-vault": {
              status: "unknown",
              source_operation_id: "opaque-original-operation",
            },
          },
        },
      })[0],
    ).toMatchObject({
      title: "来源结果未知",
      operationID: "opaque-original-operation",
    });
  });
});
describe("original export metadata and download guidance", () => {
  it.each([
    { job_id: "another-job" },
    { account_id: "another-account" },
    { tenant_id: "another-tenant" },
    { type: "audit_export" },
    { schema_version: 2 },
    { selected_datasets: ["aero-im.workspaces"] },
    { missing_datasets: ["aero-im.workspaces"] },
    { complete: true, coverage_complete: false },
  ])(
    "does not adopt mismatched or contradictory export metadata: %j",
    (result) => {
      expect(jobExportMetadata({ ...base, result }).issue).toBeTruthy();
      expect(jobExportMetadata({ ...base, result }).result).toEqual({});
      expect(jobDownloadState({ ...base, result }, now).blocked).toBe(true);
    },
  );
  it.each([
    "2026-10-16T09:00:00Z",
    "2026-10-16T10:00:00Z",
    "2026-10-16T12:00:00+02:00",
  ])(
    "blocks at or after the original expiry without renewing it: %s",
    (expires_at) => {
      const job = { ...base, result: { expires_at } };
      expect(jobDownloadState(job, now)).toMatchObject({
        blocked: true,
        title: "制品已到期",
      });
      expect(job.result.expires_at).toBe(expires_at);
    },
  );
  it("separates missing historical expiry guidance from an invalid expiry and a disposed pointer", () => {
    expect(jobDownloadState(base, now)).toMatchObject({
      blocked: false,
      title: "制品有效期未提供",
    });
    expect(
      jobDownloadState({ ...base, result: { expires_at: "tomorrow" } }, now)
        .blocked,
    ).toBe(true);
    expect(
      jobDownloadState(
        { ...base, result: { artifact_deleted_at: "2026-10-16T09:00:00Z" } },
        now,
      ).title,
    ).toBe("制品已消费或清理");
    expect(jobDownloadState(base, now, true).title).toBe(
      "本页已接收一次性下载",
    );
  });
  it.each(["pending", "running", "unknown", "partially_completed", "failed"])(
    "never treats %s as downloadable completion",
    (status) => {
      expect(
        jobDownloadState(
          { ...base, status, result: { expires_at: "2026-10-17T09:00:00Z" } },
          now,
        ).blocked,
      ).toBe(true);
    },
  );
});
