import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { nextTick } from "vue";
import { AeroIdClient } from "../api/aeroId";
import type { JsonRecord } from "../types";
import JobDetails from "./JobDetails.vue";
import {
  commandButton,
  flushCommandUI,
  mountCommandTest,
  testAccountID,
  testReceiptID,
  unmountCommandTests,
} from "./commandTestFixtures";

vi.mock("@iris-ui-kit/vue", async () => {
  const { defineComponent, h } = await import("vue");
  return {
    ...(await import("./commandTestFixtures")).irisCommandTestStubs(),
    IrisAlert: defineComponent({
      props: { title: String },
      setup:
        (props, { slots }) =>
        () =>
          h("aside", [h("strong", props.title), slots.default?.()]),
    }),
  };
});
beforeEach(() => {
  sessionStorage.clear();
  vi.spyOn(document, "hidden", "get").mockReturnValue(false);
});
afterEach(() => {
  unmountCommandTests();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});
function job(patch: JsonRecord = {}): JsonRecord {
  return {
    job_id: testReceiptID,
    account_id: testAccountID,
    actor_uid: "original-initiator",
    tenant_id: "tenant-a",
    job_type: "sync",
    status: "partially_completed",
    created_at: "2026-10-16T09:00:00Z",
    updated_at: "2026-10-16T09:02:00Z",
    request: { datasets: ["aero-vault.usage", "aero-im.workspaces"] },
    result: {
      mode: "sync",
      sources: {
        "aero-vault": { status: "synced", count: 1, complete: true },
        "aero-im": { status: "unavailable", error_code: "source.http_503" },
      },
    },
    ...patch,
  };
}
function exportResult(patch: JsonRecord = {}): JsonRecord {
  return {
    schema_version: 1,
    type: "export",
    job_id: testReceiptID,
    account_id: testAccountID,
    tenant_id: "tenant-a",
    selected_datasets: ["aero-vault.usage", "aero-im.workspaces"],
    missing_datasets: ["aero-im.workspaces"],
    source_count: 3,
    membership_count: 0,
    dataset_count: 1,
    complete: false,
    coverage_complete: false,
    traversal_complete: true,
    point_in_time: false,
    generated_at: "2026-10-16T09:02:00Z",
    expires_at: new Date(Date.now() + 300_000).toISOString(),
    manifest_digest: "a".repeat(64),
    download_url: "/untrusted-link-must-not-be-followed",
    ...patch,
  };
}
function mountJob(value: JsonRecord) {
  const requests: Array<{ method?: string; path: string; query: string }> = [];
  const fetcher = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input));
      requests.push({
        method: init?.method,
        path: url.pathname,
        query: url.search,
      });
      if (url.pathname.endsWith("/export/download"))
        return new Response('{"schema_version":1}', {
          headers: { "Content-Type": "application/json" },
        });
      return new Response(JSON.stringify({ data: value }));
    },
  );
  const client = new AeroIdClient(
    "https://accounts.example.test/v1",
    () => "component-fixture-marker",
    fetcher as typeof fetch,
  );
  return {
    ...mountCommandTest(JobDetails, { client, jobId: testReceiptID }),
    requests,
  };
}
function summary(root: HTMLElement): HTMLElement {
  const panel = root.querySelector<HTMLElement>("[data-job-summary]");
  expect(
    panel,
    "task results must be readable outside JSON diagnostics",
  ).not.toBeNull();
  return panel!;
}

describe("readable task and export outcomes via the original JobDetails GET", () => {
  it("explains the selected scope and individual sync outcomes without pretending partial is success", async () => {
    const item = mountJob(job());
    await flushCommandUI();
    const text = summary(item.root).textContent;
    expect(text).toContain("部分完成");
    expect(text).toContain("Vault 用量");
    expect(text).toContain("IM 工作空间");
    expect(text).toContain("已应用摘要");
    expect(text).toContain("摘要投影条数：1");
    expect(text).toContain("来源不可用");
    expect(text).toContain("source.http_503");
    expect(text).not.toMatch(/100%|全部成功|1 个对象/);
    expect(item.requests).toEqual([
      { method: "GET", path: `/v1/sync/jobs/${testReceiptID}`, query: "" },
    ]);
  });
  it("distinguishes a missing source result from a claim that nothing executed", async () => {
    const item = mountJob(job({ result: { sources: {} } }));
    await flushCommandUI();
    expect(summary(item.root).textContent).toContain("未报告来源结果");
    expect(summary(item.root).textContent).toContain("不能据此判断未执行");
  });
  it("reads export reference counts, missing scope and completeness independently from completed status", async () => {
    const item = mountJob(
      job({ job_type: "export", status: "completed", result: exportResult() }),
    );
    await flushCommandUI();
    const text = summary(item.root).textContent;
    expect(text).toContain("来源引用数");
    expect(text).toContain("成员关系引用数");
    expect(text).toContain("所选投影存在缺失或不可用");
    expect(text).toContain("缺失的数据集");
    expect(text).toContain("IM 工作空间");
    expect(text).toContain("引用分页遍历完成");
    expect(text).toContain("不是跨源同一时点");
    expect(text).toContain("制品到期时间");
    expect(text).toContain("未提供逐来源区域、版本与新鲜度");
    expect(item.requests).toHaveLength(1);
  });
  it("keeps unknown export outcomes explicit and never downloads or regenerates on its own", async () => {
    const item = mountJob(
      job({ job_type: "export", status: "unknown", result: exportResult() }),
    );
    await flushCommandUI();
    const text = summary(item.root).textContent;
    expect(text).toContain("结果未知");
    expect(text).toContain("只核对原制品");
    expect(text).toContain("不重新生成或延长有效期");
    expect(
      [...item.root.querySelectorAll("button")].some(
        (button) => button.textContent?.trim() === "一次性下载",
      ),
    ).toBe(false);
    expect(item.requests).toHaveLength(1);
  });
  it("presents an audit export window, including a reported zero and truncation, not account-dataset coverage", async () => {
    const item = mountJob(
      job({
        job_type: "audit_export",
        status: "completed",
        result: {
          type: "audit_export",
          event_count: 0,
          truncated: true,
          continuation_job_id: testReceiptID,
        },
      }),
    );
    await flushCommandUI();
    const text = summary(item.root).textContent;
    expect(text).toContain("审计事实数");
    expect(text).toContain("0");
    expect(text).toContain("本次窗口已截断");
    expect(text).toContain("不是完整审计历史");
    expect(text).not.toContain("缺失的数据集");
    expect(text).not.toContain("Vault 用量");
  });
  it.each([
    ["expired", { expires_at: "2026-01-01T00:00:00Z" }, "制品已到期"],
    [
      "disposed",
      { artifact_deleted_at: "2026-10-16T09:03:00Z" },
      "制品已消费或清理",
    ],
  ])(
    "explains and disables a known %s artifact without another request",
    async (_, patch, message) => {
      const item = mountJob(
        job({
          job_type: "export",
          status: "completed",
          result: exportResult(patch),
        }),
      );
      await flushCommandUI();
      expect(summary(item.root).textContent).toContain(message);
      expect(commandButton(item.root, "一次性下载").disabled).toBe(true);
      expect(item.requests).toHaveLength(1);
    },
  );
  it("does not coerce missing or malformed export counters into zero", async () => {
    const item = mountJob(
      job({
        job_type: "export",
        status: "completed",
        result: exportResult({
          source_count: "3",
          membership_count: undefined,
          dataset_count: Number.MAX_SAFE_INTEGER + 1,
        }),
      }),
    );
    await flushCommandUI();
    const text = summary(item.root).textContent;
    expect(text).toContain("字段无效（未展示）");
    expect(text).toContain("来源未提供");
    expect(text).not.toContain("9,007,199");
  });
  it("keeps technical request/result JSON collapsed while the business summary remains visible", async () => {
    const item = mountJob(job());
    await flushCommandUI();
    expect(summary(item.root)).not.toBeNull();
    const diagnostics = item.root.querySelector(
      "details[data-job-diagnostics]",
    );
    expect(diagnostics).not.toBeNull();
    expect(diagnostics?.hasAttribute("open")).toBe(false);
    expect(diagnostics?.textContent).toContain("结果元数据");
  });
  it("saves only the original one-time response after explicit confirmation, then explains local consumption", async () => {
    vi.useFakeTimers();
    const settle = async () => {
      await vi.advanceTimersByTimeAsync(0);
      await nextTick();
    };
    vi.stubGlobal(
      "URL",
      class extends URL {
        static createObjectURL = vi.fn(() => "blob:owned-job-fixture");
        static revokeObjectURL = vi.fn();
      },
    );
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    const item = mountJob(
      job({ job_type: "export", status: "completed", result: exportResult() }),
    );
    await settle();
    commandButton(item.root, "一次性下载").click();
    await settle();
    expect(item.requests).toHaveLength(1);
    commandButton(item.root, "确认并下载").click();
    await settle();
    expect(item.requests.at(-1)).toEqual({
      method: "GET",
      path: `/v1/sync/jobs/${testReceiptID}/export/download`,
      query: "?one_time=true",
    });
    expect(URL.createObjectURL).toHaveBeenCalledTimes(1);
    expect(summary(item.root).textContent).toContain("本页已接收一次性下载");
    expect(commandButton(item.root, "一次性下载").disabled).toBe(true);
    expect(item.requests).toHaveLength(2);
    await vi.advanceTimersByTimeAsync(1000);
    expect(URL.revokeObjectURL).toHaveBeenCalledExactlyOnceWith(
      "blob:owned-job-fixture",
    );
  });
  it("updates expiry guidance while confirmation is open without GET, renewing TTL or consuming", async () => {
    vi.useFakeTimers();
    const expiry = new Date(Date.now() + 1000).toISOString();
    const item = mountJob(
      job({
        job_type: "export",
        status: "completed",
        result: exportResult({ expires_at: expiry }),
      }),
    );
    await vi.advanceTimersByTimeAsync(0);
    await nextTick();
    commandButton(item.root, "一次性下载").click();
    await nextTick();
    expect(commandButton(item.root, "确认并下载").disabled).toBe(false);
    await vi.advanceTimersByTimeAsync(1000);
    await nextTick();
    expect(commandButton(item.root, "确认并下载").disabled).toBe(true);
    expect(summary(item.root).textContent).toContain("制品已到期");
    expect(item.requests).toHaveLength(1);
    unmountCommandTests();
    expect(vi.getTimerCount()).toBe(0);
  });
});
