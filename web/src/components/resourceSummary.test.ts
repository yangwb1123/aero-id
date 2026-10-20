import { describe, expect, it } from "vitest";
import { resourceSummaries } from "./resourceSummary";
import type { AggregateView, JsonRecord } from "../types";
function view(data: unknown = {}, extra: JsonRecord = {}): AggregateView {
  return {
    partial: false,
    source_errors: {},
    snapshots: [
      {
        dataset: "aero-vault.usage",
        source: "aero-vault",
        source_region: "local",
        status: "fresh",
        version: 3,
        data,
        ...extra,
      },
    ],
  };
}
function usage(data: unknown, extra: JsonRecord = {}) {
  return resourceSummaries(view(data, extra))[0]!;
}
describe("explicit bounded resource field mapping", () => {
  it.each([undefined, null])(
    "keeps omitted usage %s unknown instead of zero",
    (value) => {
      const summary = usage({ used_bytes: value, used_objects: value });
      expect(summary.fields[1]!.value).toBe("来源未提供");
      expect(summary.fields[2]!.value).toBe("来源未提供");
    },
  );
  it.each([-1, 1.5, "12", true, [], {}, Infinity, Number.MAX_SAFE_INTEGER + 1])(
    "does not coerce invalid counter %j",
    (value) => {
      const summary = usage({ used_bytes: value });
      expect(summary.fields[1]!.value).toBe("字段无效（未展示）");
      expect(summary.warnings).toContain(
        "部分字段无效或超出安全显示范围，未展示原值；不会补零。",
      );
    },
  );
  it("does not interpret an invalid truncation flag as a complete empty list", () => {
    const value = view({ items: [], truncated: "true" });
    value.snapshots![0]!.dataset = "aero-vault.buckets";
    const summary = resourceSummaries(value)[1]!;
    expect(summary.state).toBe("摘要字段无效");
    expect(summary.listMessage).toBe("");
  });
  it("preserves reported zeros and safe integer values without a quota percentage", () => {
    const summary = usage({
      used_bytes: 0,
      used_objects: 0,
      max_bytes: Number.MAX_SAFE_INTEGER,
    });
    expect(summary.fields[1]!.value).toBe("0 B");
    expect(summary.fields[2]!.value).toBe("0 个对象");
    expect(summary.fields[3]!.value).toBe("9,007,199,254,740,991 B");
  });
  it.each(["unavailable", "unknown"])(
    "does not show retained resource values as current when %s",
    (status) => {
      const summary = usage(
        { used_bytes: 999 },
        { status, expires_at: "2026-01-01T00:00:00Z" },
      );
      expect(summary.fields).toEqual([]);
      expect(summary.state).toBe(
        status === "unavailable" ? "投影不可用" : "投影状态未知",
      );
    },
  );
  it("labels an expired fresh projection stale independently of capability freshness", () => {
    expect(
      usage({ used_bytes: 10 }, { expires_at: "2020-01-01T00:00:00Z" }).state,
    ).toBe("陈旧投影");
  });
  it("does not display a rounded source version or invalid metadata", () => {
    const summary = usage(
      {},
      {
        version: Number.MAX_SAFE_INTEGER + 1,
        generated_at: { secret: "not-shown" },
      },
    );
    expect(summary.metadata[1]!.value).toBe("字段无效（未展示）");
    expect(summary.metadata[2]!.value).toBe("字段无效（未展示）");
    expect(JSON.stringify(summary)).not.toContain("not-shown");
  });
  it("rejects duplicate snapshots rather than choosing an arbitrary source version", () => {
    const value = view({ used_bytes: 10 });
    value.snapshots!.push({
      ...value.snapshots![0]!,
      data: { used_bytes: 20 },
    });
    const summary = resourceSummaries(value)[0]!;
    expect(summary.state).toBe("摘要字段无效");
    expect(summary.fields).toEqual([]);
  });
  it("retains redaction-only truncation as unknown fields, not an empty inventory", () => {
    const value = view({ truncated: true, sha256: "not-a-visible-field" });
    value.snapshots![0]!.dataset = "aero-vault.buckets";
    const summary = resourceSummaries(value)[1]!;
    expect(summary.listMessage).toBe("引用列表：来源未提供");
    expect(summary.warnings[0]).toContain("来源投影已截断");
    expect(JSON.stringify(summary)).not.toContain("not-a-visible-field");
  });
  it.each([
    "source.not_configured",
    "source.no_projection",
    "source.http_401",
    "source.http_403",
    "source.unavailable",
    "source.summary_incomplete",
  ])("separates missing projection reason %s", (code) => {
    const value = view();
    value.snapshots = [];
    value.source_errors = { "aero-vault": code };
    const states: Record<string, string> = {
      "source.not_configured": "未接通",
      "source.no_projection": "待同步",
      "source.http_401": "读取被拒绝",
      "source.http_403": "读取被拒绝",
      "source.unavailable": "来源故障",
      "source.summary_incomplete": "来源结果不完整",
    };
    expect(resourceSummaries(value)[0]!.state).toBe(states[code]);
  });
});
