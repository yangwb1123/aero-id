import { afterEach, describe, expect, it } from "vitest";
import {
  loadOperationReconcileKey,
  loadSyncJobReconcileKey,
  operationRunIdempotencyKey,
  saveOperationReconcileKey,
  saveSyncJobReconcileKey,
} from "./operationRequestKeys";

afterEach(() => sessionStorage.clear());

describe("operation request idempotency keys", () => {
  it("persists a reconciliation retry key per operation without storing request data", () => {
    const key = "123e4567-e89b-12d3-a456-426614174000";
    saveOperationReconcileKey(sessionStorage, "operation/1", key);

    expect(loadOperationReconcileKey(sessionStorage, "operation/1")).toBe(key);
    expect(loadOperationReconcileKey(sessionStorage, "operation/2")).toBe(
      undefined,
    );
    expect(
      sessionStorage.getItem("aero-id:operation-reconcile:operation%2F1"),
    ).toBe(key);
  });

  it("persists reconciliation keys in a separate sync-job namespace", () => {
    const key = "123e4567-e89b-12d3-a456-426614174000";
    saveSyncJobReconcileKey(sessionStorage, "job/1", key);

    expect(loadSyncJobReconcileKey(sessionStorage, "job/1")).toBe(key);
    expect(loadSyncJobReconcileKey(sessionStorage, "job/2")).toBe(undefined);
    expect(sessionStorage.getItem("aero-id:sync-job-reconcile:job%2F1")).toBe(
      key,
    );
    expect(loadOperationReconcileKey(sessionStorage, "job/1")).toBe(undefined);
  });

  it("clears a settled reconciliation key and derives a stable run key", () => {
    saveOperationReconcileKey(sessionStorage, "operation-1", "bad-key");
    expect(loadOperationReconcileKey(sessionStorage, "operation-1")).toBe(
      undefined,
    );
    saveOperationReconcileKey(sessionStorage, "operation-1", undefined);
    expect(sessionStorage.length).toBe(0);
    expect(operationRunIdempotencyKey("operation-1")).toBe(
      "operation.run:operation-1",
    );
  });
});
