export interface StoredEraseIntent {
  idempotencyKey?: string;
  jobID?: string;
}

const keyPattern = /^[\da-f]{8}-(?:[\da-f]{4}-){3}[\da-f]{12}$/i;
const maxJobIDLength = 256;

export function loadEraseIntent(
  storage: Storage,
  accountID: string | undefined,
): StoredEraseIntent {
  const key = storageKey(accountID);
  if (!key) return {};
  try {
    const raw = storage.getItem(key);
    if (!raw) return {};
    const value: unknown = JSON.parse(raw);
    if (!value || typeof value !== "object" || Array.isArray(value)) return {};
    const record = value as Record<string, unknown>;
    const idempotencyKey =
      typeof record.idempotencyKey === "string" &&
      keyPattern.test(record.idempotencyKey)
        ? record.idempotencyKey
        : undefined;
    const jobID =
      typeof record.jobID === "string" &&
      record.jobID.trim().length > 0 &&
      record.jobID.length <= maxJobIDLength
        ? record.jobID
        : undefined;
    return { idempotencyKey, jobID };
  } catch {
    return {};
  }
}

export function saveEraseIntent(
  storage: Storage,
  accountID: string | undefined,
  intent: StoredEraseIntent,
): void {
  const key = storageKey(accountID);
  if (!key) return;
  try {
    if (!intent.idempotencyKey && !intent.jobID) {
      storage.removeItem(key);
      return;
    }
    storage.setItem(key, JSON.stringify(intent));
  } catch {
    // Idempotency remains available in memory when session storage is blocked.
  }
}

function storageKey(accountID: string | undefined): string | undefined {
  const normalized = accountID?.trim();
  return normalized ? `aero-id:erase-intent:${normalized}` : undefined;
}
