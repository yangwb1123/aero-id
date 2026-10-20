import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AeroIdClient } from "../api/aeroId";
import type { JsonRecord } from "../types";
import JobDetails from "./JobDetails.vue";
import OperationDetails from "./OperationDetails.vue";
import {
  commandButton,
  commandTestClient,
  flushCommandUI,
  mountCommandTest,
  testAccountID,
  testReceiptID,
  unmountCommandTests,
} from "./commandTestFixtures";
import {
  saveOperationReconcileKey,
  saveSyncJobReconcileKey,
} from "./operationRequestKeys";

vi.mock("@iris-ui-kit/vue", async () => {
  const { defineComponent, h } = await import("vue");
  return {
    ...(await import("./commandTestFixtures")).irisCommandTestStubs(),
    IrisAlert: defineComponent({
      props: { tone: String, title: String },
      setup:
        (props, { slots }) =>
        () =>
          h(
            "section",
            { "data-tone": props.tone, "data-alert-title": props.title },
            [h("h2", props.title), slots.default?.()],
          ),
    }),
  };
});

beforeEach(() => sessionStorage.clear());
afterEach(unmountCommandTests);
type TaskKind = "sync" | "operation";
const blockTitle = "继续执行授权阻断记录";
function receipt(kind: TaskKind, status = "unknown"): JsonRecord {
  const common = {
    account_id: testAccountID,
    actor_uid: "original-initiator",
    tenant_id: "tenant-a",
    status,
    idempotency_key: "original-create-key",
    result: { original_outcome: "unknown" },
  };
  return kind === "sync"
    ? {
        ...common,
        job_id: testReceiptID,
        job_type: "export",
        request: { datasets: ["aero-vault.usage"] },
      }
    : {
        ...common,
        operation_id: testReceiptID,
        home_region: "local",
        operation_type: "sync",
        steps: [
          {
            step_key: "summary",
            source: "aero-im",
            action: "summary",
            status: "unknown",
            source_operation_id: "original-source-operation",
            dataset_scope: {
              status: "explicit",
              datasets: ["aero-im.workspaces"],
            },
          },
        ],
      };
}
function mountTask(kind: TaskKind, value: JsonRecord) {
  const client = {
    ...commandTestClient(),
    getSyncJob: vi.fn(async (): Promise<JsonRecord> => value),
    getOperation: vi.fn(async (): Promise<JsonRecord> => value),
    listOperationAudit: vi.fn(async () => ({ items: [] })),
    reconcileSyncJob: vi.fn(),
    reconcileOperation: vi.fn(),
    downloadExport: vi.fn(),
  };
  const props =
    kind === "sync"
      ? { client: client as unknown as AeroIdClient, jobId: testReceiptID }
      : {
          client: client as unknown as AeroIdClient,
          operationId: testReceiptID,
        };
  const mounted = mountCommandTest(
    kind === "sync" ? JobDetails : OperationDetails,
    props,
  );
  return { ...mounted, client };
}
function alert(root: HTMLElement, title: string): HTMLElement | undefined {
  return [...root.querySelectorAll<HTMLElement>("[data-alert-title]")].find(
    (item) => item.dataset.alertTitle === title,
  );
}
function expectNoCommands(client: ReturnType<typeof mountTask>["client"]) {
  for (const command of Object.values(client.commands))
    expect(command).not.toHaveBeenCalled();
  expect(client.runOperation).not.toHaveBeenCalled();
  expect(client.reconcileOperation).not.toHaveBeenCalled();
  expect(client.reconcileSyncJob).not.toHaveBeenCalled();
  expect(client.downloadExport).not.toHaveBeenCalled();
}

describe.each<TaskKind>(["sync", "operation"])(
  "%s durable continuation diagnostics",
  (kind) => {
    it.each(["denied", "unavailable"])(
      "shows authorization_%s as a historical warning, not execution failure or a grant",
      async (reason) => {
        saveOperationReconcileKey(
          sessionStorage,
          testReceiptID,
          "original-op-key",
        );
        saveSyncJobReconcileKey(
          sessionStorage,
          testReceiptID,
          "original-job-key",
        );
        const stored = { ...sessionStorage };
        const code = `${kind}.authorization_${reason}`;
        const value = { ...receipt(kind), error_code: code };
        const original = structuredClone(value);
        const item = mountTask(kind, value);
        await flushCommandUI();
        const notice = alert(item.root, blockTitle);
        expect(notice?.dataset.tone).toBe("warning");
        expect(notice?.textContent).toContain(code);
        expect(notice?.textContent).toContain(
          reason === "denied" ? "权限被拒绝" : "未能核实授权或目标",
        );
        expect(notice?.textContent).toContain("原发起者");
        expect(notice?.textContent).toContain("不代表当前授权");
        expect(notice?.textContent).toContain("不等于任务执行失败");
        expect(notice?.textContent).toContain("不能证明远端未执行");
        expect(notice?.textContent).toContain("原回执和原键");
        expect(alert(item.root, code)).toBeUndefined();
        expect(value).toEqual(original);
        expect({ ...sessionStorage }).toEqual(stored);
        expectNoCommands(item.client);
        expect(commandButton(item.root, "对账未知结果").disabled).toBe(false);
        for (const capability of item.report.value!.capabilities)
          capability.available = false;
        await flushCommandUI();
        expect(commandButton(item.root, "对账未知结果").disabled).toBe(true);
        expect(alert(item.root, blockTitle)).toBeDefined();
        expectNoCommands(item.client);
      },
    );
    it.each(
      kind === "sync"
        ? ["pending", "running", "unknown"]
        : ["requested", "running", "waiting", "compensating", "unknown"],
    )(
      "keeps %s nonterminal without inferring no prior effects",
      async (status) => {
        const value: JsonRecord = {
          ...receipt(kind, status),
          error_code: `${kind}.authorization_unavailable`,
          // Recognized diagnostics use fixed copy rather than arbitrary messages.
          error_message:
            "untrusted diagnostic must not become recovery guidance",
          completed_at: null,
        };
        const item = mountTask(kind, value);
        await flushCommandUI();
        expect(alert(item.root, blockTitle)?.dataset.tone).toBe("warning");
        expect(item.root.textContent).not.toContain(value.error_message);
        expect(value.status).toBe(status);
        expectNoCommands(item.client);
      },
    );
    const fallbackCases: [string, JsonRecord][] = [
      ...["completed", "partially_completed", "failed", "canceled"].map(
        (status): [string, JsonRecord] => [status, { status }],
      ),
      ["unknown future status", { status: "awaiting_repair" }],
      [
        "wrong status family",
        { status: kind === "sync" ? "requested" : "pending" },
      ],
      ["missing status", { status: undefined }],
      ["non-string status", { status: ["unknown"] }],
      ["completion evidence", { completed_at: "2026-10-14T00:00:00Z" }],
      ["malformed completion evidence", { completed_at: "" }],
      [
        "other resource family",
        {
          error_code: `${kind === "sync" ? "operation" : "sync"}.authorization_denied`,
        },
      ],
      [
        "future authorization code",
        { error_code: `${kind}.authorization_expired` },
      ],
      [
        "prefix lookalike",
        { error_code: `${kind}.authorization_denied.extra` },
      ],
      ["non-string code", { error_code: [`${kind}.authorization_denied`] }],
      ...[
        "execution_lease_lost",
        "execution_scope_invalid",
        "execution_defer_unknown",
      ].map((code): [string, JsonRecord] => [
        code,
        { error_code: `${kind}.${code}` },
      ]),
      ["erased account", { error_code: "account.erased" }],
      ["home region fence", { error_code: "account.home_region_required" }],
    ];
    it.each(fallbackCases)(
      "does not classify %s as an authorization recovery warning",
      async (_name, changes) => {
        const value = {
          ...receipt(kind),
          error_code: `${kind}.authorization_denied`,
          error_message: "original diagnostic retained",
          ...changes,
        };
        const item = mountTask(kind, value);
        await flushCommandUI();
        expect(alert(item.root, blockTitle)).toBeUndefined();
        const diagnostic = alert(item.root, String(value.error_code));
        expect(diagnostic?.dataset.tone).toBe("danger");
        expect(diagnostic?.textContent).toContain(value.error_message);
        expectNoCommands(item.client);
      },
    );
    it("updates only after original-task GET and never treats cleared diagnostics as new-command authority", async () => {
      const value: JsonRecord = {
        ...receipt(kind),
        error_code: `${kind}.authorization_denied`,
      };
      const item = mountTask(kind, value);
      await flushCommandUI();
      expect(alert(item.root, blockTitle)).toBeDefined();
      const cleared = { ...value, error_code: "", status: "completed" };
      item.client.getSyncJob.mockResolvedValue(cleared);
      item.client.getOperation.mockResolvedValue(cleared);
      commandButton(item.root, "刷新状态").click();
      await flushCommandUI();
      expect(alert(item.root, blockTitle)).toBeUndefined();
      const getter =
        kind === "sync" ? item.client.getSyncJob : item.client.getOperation;
      expect(getter).toHaveBeenCalledTimes(2);
      expect(getter).toHaveBeenLastCalledWith(
        testReceiptID,
        expect.any(AbortSignal),
      );
      expect(value.status).toBe("unknown");
      expectNoCommands(item.client);
    });
    it("does not promote nested step/result diagnostics without a persisted root error code", async () => {
      const value = receipt(kind);
      const code = `${kind}.authorization_denied`;
      value.result = { error_code: code };
      if (kind === "operation") {
        for (const step of value.steps as JsonRecord[]) {
          step.error_code = code;
          step.compensation_error_code = code;
        }
      }
      const item = mountTask(kind, value);
      await flushCommandUI();
      expect(alert(item.root, blockTitle)).toBeUndefined();
      expect(item.root.querySelector('[data-tone="danger"]')).toBeNull();
      expectNoCommands(item.client);
    });
  },
);
