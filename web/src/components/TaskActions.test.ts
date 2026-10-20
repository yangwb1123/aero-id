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
vi.mock("@iris-ui-kit/vue", async () =>
  (await import("./commandTestFixtures")).irisCommandTestStubs(),
);
beforeEach(() => sessionStorage.clear());
afterEach(unmountCommandTests);
function operation() {
  return {
    operation_id: testReceiptID,
    account_id: testAccountID,
    actor_uid: "initiator",
    tenant_id: "tenant-a",
    operation_type: "sync",
    home_region: "local",
    status: "requested",
    steps: [
      {
        step_key: "summary",
        source: "aero-im",
        action: "summary",
        dataset_scope: { status: "explicit", datasets: ["aero-im.workspaces"] },
        compensation_action: "erase",
      },
    ],
  };
}
function operationMount() {
  const value = operation();
  const client = {
    ...commandTestClient(),
    getOperation: vi.fn(async () => value),
    listOperationAudit: vi.fn(async () => ({ items: [] })),
    runOperation: vi.fn(
      async (_id: string, _key: string): Promise<JsonRecord> => ({
        ...value,
        status: "completed",
      }),
    ),
    reconcileOperation: vi.fn(
      async (_id: string, _key: string): Promise<JsonRecord> => ({
        ...value,
        status: "unknown",
      }),
    ),
  };
  return {
    client,
    ...mountCommandTest(OperationDetails, {
      client: client as unknown as AeroIdClient,
      operationId: testReceiptID,
    }),
  };
}
function jobMount(type = "export", status = "unknown") {
  const job = {
    job_id: testReceiptID,
    account_id: testAccountID,
    actor_uid: "initiator",
    tenant_id: "tenant-a",
    job_type: type,
    status,
    request: { datasets: ["aero-vault.usage"] },
  };
  const client = {
    ...commandTestClient(),
    getSyncJob: vi.fn(async (): Promise<JsonRecord> => job),
    reconcileSyncJob: vi.fn(
      async (_id: string, _key: string): Promise<JsonRecord> => ({
        ...job,
        status: "unknown",
      }),
    ),
    downloadExport: vi.fn(
      async () => new Blob(["{}"], { type: "application/json" }),
    ),
  };
  return {
    client,
    ...mountCommandTest(JobDetails, {
      client: client as unknown as AeroIdClient,
      jobId: testReceiptID,
    }),
  };
}
describe("existing task actions remain current-authorized, not new previews", () => {
  it("confirms destructive compensation before Run and rechecks current grants at click time", async () => {
    const item = operationMount();
    await flushCommandUI();
    commandButton(item.root, "准备运行危险 Operation").click();
    await flushCommandUI();
    const input = item.root.querySelector("input")!;
    input.value = "CONFIRM OPERATION RUN";
    input.dispatchEvent(new Event("input"));
    await flushCommandUI();
    item.report.value!.capabilities.find(
      (feature) => feature.name === "account.erase",
    )!.available = false;
    await flushCommandUI();
    expect(commandButton(item.root, "确认运行").disabled).toBe(true);
    expect(item.client.runOperation).not.toHaveBeenCalled();
    item.report.value!.capabilities.find(
      (feature) => feature.name === "account.erase",
    )!.available = true;
    await flushCommandUI();
    commandButton(item.root, "确认运行").click();
    await flushCommandUI();
    expect(item.client.runOperation).toHaveBeenCalledWith(
      testReceiptID,
      `operation.run:${testReceiptID}`,
    );
    expect(item.client.commands.prepare).not.toHaveBeenCalled();
  });
  it("does not clear a reconciliation key on a wrong-target result and only manually retries that same key", async () => {
    const item = jobMount();
    item.client.reconcileSyncJob.mockImplementation(async () => ({
      job_id: testReceiptID,
      account_id: "wrong-account",
    }));
    await flushCommandUI();
    commandButton(item.root, "对账未知结果").click();
    await flushCommandUI();
    expect(item.root.textContent).toContain("与原任务不一致");
    commandButton(item.root, "对账未知结果").click();
    await flushCommandUI();
    expect(item.client.reconcileSyncJob.mock.calls[1]).toEqual(
      item.client.reconcileSyncJob.mock.calls[0],
    );
    expect(item.client.commands.prepare).not.toHaveBeenCalled();
  });
  it("never borrows an owner/menu grant to reconcile; audit jobs also need audit.read", async () => {
    const item = jobMount("audit_export");
    await flushCommandUI();
    item.report.value!.capabilities.find(
      (feature) => feature.name === "audit.read",
    )!.available = false;
    await flushCommandUI();
    expect(commandButton(item.root, "对账未知结果").disabled).toBe(true);
    expect(item.client.reconcileSyncJob).not.toHaveBeenCalled();
  });
  it.each(["audit.export", "audit.read"])(
    "requires current %s again at audit download confirmation, without an automatic request",
    async (permission) => {
      const item = jobMount("audit_export", "completed");
      await flushCommandUI();
      commandButton(item.root, "一次性下载").click();
      await flushCommandUI();
      expect(commandButton(item.root, "确认并下载").disabled).toBe(false);
      item.report.value!.capabilities.find(
        (feature) => feature.name === permission,
      )!.authorization = "denied";
      await flushCommandUI();
      expect(commandButton(item.root, "确认并下载").disabled).toBe(true);
      commandButton(item.root, "确认并下载").click();
      await flushCommandUI();
      expect(item.client.downloadExport).not.toHaveBeenCalled();
      expect(item.client.commands.prepare).not.toHaveBeenCalled();
    },
  );
  it("disables one-time download when export permission is unknown even with an owned completed receipt", async () => {
    const item = jobMount("export", "completed");
    await flushCommandUI();
    item.report.value!.capabilities.find(
      (feature) => feature.name === "account.export",
    )!.authorization = "unknown";
    await flushCommandUI();
    expect(commandButton(item.root, "一次性下载").disabled).toBe(true);
    expect(item.client.downloadExport).not.toHaveBeenCalled();
  });
});
