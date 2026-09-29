const uuidPattern = /^[\da-f]{8}-(?:[\da-f]{4}-){3}[\da-f]{12}$/i;

export function operationRunIdempotencyKey(operationID: string): string {
  return `operation.run:${operationID}`;
}

export function loadOperationReconcileKey(
  storage: Storage,
  operationID: string,
): string | undefined {
  return loadReconcileKey(storage, "operation", operationID);
}

export function saveOperationReconcileKey(
  storage: Storage,
  operationID: string,
  key: string | undefined,
): void {
  saveReconcileKey(storage, "operation", operationID, key);
}

export function loadSyncJobReconcileKey(
  storage: Storage,
  jobID: string,
): string | undefined {
  return loadReconcileKey(storage, "sync-job", jobID);
}

export function saveSyncJobReconcileKey(
  storage: Storage,
  jobID: string,
  key: string | undefined,
): void {
  saveReconcileKey(storage, "sync-job", jobID, key);
}

function loadReconcileKey(
  storage: Storage,
  scope: "operation" | "sync-job",
  id: string,
): string | undefined {
  try {
    const value = storage.getItem(storageKey(scope, id));
    return value && uuidPattern.test(value) ? value : undefined;
  } catch {
    return undefined;
  }
}

function saveReconcileKey(
  storage: Storage,
  scope: "operation" | "sync-job",
  id: string,
  key: string | undefined,
): void {
  try {
    const name = storageKey(scope, id);
    if (key) storage.setItem(name, key);
    else storage.removeItem(name);
  } catch {
    // Reconciliation can still proceed in memory when session storage is blocked.
  }
}

function storageKey(scope: "operation" | "sync-job", id: string): string {
  return `aero-id:${scope}-reconcile:${encodeURIComponent(id)}`;
}
