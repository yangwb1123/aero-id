import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "../api/aeroId";
import type { JsonRecord } from "../types";
import OperationDetails from "./OperationDetails.vue";
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
    IrisTimeline: defineComponent({
      props: ["items"],
      setup: (props) => () =>
        h(
          "ol",
          props.items.map(
            (item: { key: string; title: string; description: string }) =>
              h("li", { key: item.key }, [item.title, item.description]),
          ),
        ),
    }),
  };
});
beforeEach(() => sessionStorage.clear());
afterEach(() => {
  unmountCommandTests();
  vi.restoreAllMocks();
});
// Legacy/missing-scope HTTP receipt: no raw step request_payload or timestamps.
function operation(patch: JsonRecord = {}): JsonRecord {
  return {
    operation_id: testReceiptID,
    account_id: testAccountID,
    tenant_id: "tenant-a",
    actor_uid: "original-initiator",
    home_region: "local",
    operation_type: "sync",
    status: "partially_completed",
    request_id: "fixture-operation-request",
    created_at: "2026-10-16T09:00:00Z",
    updated_at: "2026-10-16T09:02:00Z",
    request_payload: {},
    result: {},
    steps: [
      {
        step_key: "vault-summary",
        source: "aero-vault",
        action: "summary",
        status: "succeeded",
        attempt: 1,
        result: {
          status: "partially_completed",
          count: 1,
          complete: false,
          application_outcome: "applied",
          skipped_datasets: ["aero-vault.buckets"],
          message_body: "UNAPPROVED-FIELD-MUST-NOT-RENDER",
        },
      },
      {
        step_key: "im-summary",
        source: "aero-im",
        action: "summary",
        status: "failed",
        attempt: 1,
        error_code: "operation.step_failed",
        source_operation_id: "im-original-id",
      },
      {
        step_key: "later-step",
        source: "aero-im",
        action: "summary",
        status: "skipped",
        attempt: 0,
      },
    ],
    ...patch,
  };
}
function mountOperation(value: JsonRecord, events: JsonRecord[] = []) {
  let read = value;
  const requests: Array<{ method?: string; path: string }> = [];
  const fetcher = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input)).pathname;
      requests.push({ method: init?.method, path });
      const data = path.startsWith("/v1/audit/")
        ? { operation_id: testReceiptID, events }
        : read;
      return new Response(JSON.stringify({ data }));
    },
  );
  const client = new AeroIdClient(
    "https://accounts.example.test/v1",
    () => "component-fixture-marker",
    fetcher as typeof fetch,
  );
  return {
    ...mountCommandTest(OperationDetails, {
      client,
      operationId: testReceiptID,
    }),
    requests,
    setRead: (next: JsonRecord) => {
      read = next;
    },
  };
}
function panel(root: HTMLElement): HTMLElement {
  const value = root.querySelector<HTMLElement>("[data-operation-summary]");
  expect(
    value,
    "Operation must be understandable outside JSON/tables",
  ).not.toBeNull();
  return value!;
}

describe("Operation business results on the original detail GET", () => {
  it("shows ordered source actions, reported partial summary and failed/skipped steps, not a success percentage", async () => {
    const item = mountOperation(operation());
    await flushCommandUI();
    const text = panel(item.root).textContent;
    expect(text).toContain("来源同步 · 部分完成");
    expect(text).toContain("摘要投影条数");
    expect(text).toContain("所请求摘要不完整");
    expect(text).toContain("Vault 桶引用");
    expect(text).toContain("步骤失败");
    expect(text).toContain("已跳过");
    expect(text).toContain("im-original-id");
    expect(text).not.toContain("UNAPPROVED-FIELD-MUST-NOT-RENDER");
    expect(text).not.toMatch(/100%|全部成功|已回滚/);
    expect(
      [...panel(item.root).querySelectorAll("[data-operation-step]")].map(
        (node) => node.getAttribute("data-operation-step"),
      ),
    ).toEqual(["vault-summary", "im-summary", "later-step"]);
    expect(item.requests.every((request) => request.method === "GET")).toBe(
      true,
    );
  });
  it("separates unknown forward and compensation outcomes without offering a second command", async () => {
    const item = mountOperation(
      operation({
        status: "unknown",
        steps: [
          {
            step_key: "original-erase",
            source: "aero-im",
            action: "erase",
            status: "succeeded",
            attempt: 1,
            source_operation_id: "forward-original",
            compensation_action: "summary",
            compensation_status: "unknown",
            compensation_attempt: 2,
            compensation_source_operation_id: "compensation-original",
            compensation_error_code: "operation.compensation_unknown_outcome",
          },
        ],
      }),
    );
    await flushCommandUI();
    const text = panel(item.root).textContent;
    expect(text).toContain("结果未知");
    expect(text).toContain("补偿结果未知");
    expect(text).toContain("forward-original");
    expect(text).toContain("compensation-original");
    expect(text).toContain("不代表整体回滚");
    expect(text).toContain("保留原 Operation");
    expect(item.requests).toHaveLength(2);
  });
  it("does not call an unreported compensation state or omitted result unexecuted", async () => {
    const item = mountOperation(
      operation({
        status: "completed",
        steps: [
          {
            step_key: "summary",
            source: "aero-vault",
            action: "summary",
            status: "succeeded",
            compensation_action: "erase",
          },
        ],
      }),
    );
    await flushCommandUI();
    const text = panel(item.root).textContent;
    expect(text).toContain("未报告补偿状态");
    expect(text).toContain("计划不代表已执行");
    expect(text).toContain("未报告结果摘要");
    expect(text).toContain("来源未提供");
    expect(text).not.toContain("未执行补偿");
  });
  it("keeps root completion distinct from partial source results and projection freshness", async () => {
    const item = mountOperation(operation({ status: "completed" }));
    await flushCommandUI();
    const text = panel(item.root).textContent;
    expect(text).toContain("来源同步 · 已完成");
    expect(text).toContain("所请求摘要不完整");
    expect(text).toContain("不证明当前投影新鲜");
  });
  it.each(
    [
      undefined,
      null,
      "bad",
      [null],
      Array.from({ length: 33 }, () => ({})),
    ].map((steps) => ({ steps })),
  )(
    "does not turn malformed or missing steps $steps into an empty success",
    async ({ steps }) => {
      const item = mountOperation(operation({ steps }));
      await flushCommandUI();
      expect(panel(item.root).textContent).toContain("步骤记录无法安全展示");
      expect(panel(item.root).textContent).toContain("不能据此判断未执行");
      expect(
        panel(item.root).querySelectorAll("[data-operation-step]"),
      ).toHaveLength(0);
    },
  );
  it("folds raw diagnostics, keeps the original Request ID and offers existing navigation", async () => {
    const item = mountOperation(operation());
    await flushCommandUI();
    const diagnostics = item.root.querySelector(
      "details[data-operation-diagnostics]",
    );
    expect(diagnostics).not.toBeNull();
    expect(diagnostics?.hasAttribute("open")).toBe(false);
    expect(panel(item.root).textContent).toContain("fixture-operation-request");
    expect(panel(item.root).querySelector('a[href="#/audit"]')).not.toBeNull();
    expect(
      panel(item.root).querySelector('a[href="#/resources"]'),
    ).not.toBeNull();
  });
  it("uses audit payload facts to explain step/status/attempt, keeping them separate from the current receipt", async () => {
    const item = mountOperation(operation(), [
      {
        event_id: "fixture-fact",
        source: "aero-id",
        action: "operation.step.finished",
        occurred_at: "2026-10-16T09:01:00Z",
        payload: {
          step_key: "vault-summary",
          status: "succeeded",
          attempt: 1,
          source: "aero-vault",
          datasets: ["aero-vault.usage"],
          source_operation_id: "original-fact-id",
          message_body: "UNAPPROVED-FIELD-MUST-NOT-RENDER",
        },
      },
    ]);
    await flushCommandUI();
    expect(item.root.textContent).toContain("步骤结果已记录");
    expect(item.root.textContent).toContain("Vault 用量");
    expect(item.root.textContent).toContain("original-fact-id");
    expect(item.root.textContent).not.toContain(
      "UNAPPROVED-FIELD-MUST-NOT-RENDER",
    );
  });
  it("explains legacy missing per-step scope and never loosens existing Run eligibility", async () => {
    const item = mountOperation(operation({ status: "requested" }));
    await flushCommandUI();
    expect(panel(item.root).textContent).toContain(
      "本详情未提供逐步骤数据集范围",
    );
    expect(commandButton(item.root, "运行 Operation").disabled).toBe(true);
    commandButton(item.root, "运行 Operation").dispatchEvent(
      new MouseEvent("click", { bubbles: true }),
    );
    await flushCommandUI();
    expect(item.requests).toHaveLength(2);
    item.setRead(operation({ status: "completed" }));
    commandButton(item.root, "刷新状态").click();
    await flushCommandUI();
    expect(panel(item.root).textContent).toContain("来源同步 · 已完成");
    expect(item.requests.every((request) => request.method === "GET")).toBe(
      true,
    );
  });
});
