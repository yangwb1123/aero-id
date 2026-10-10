import type { JobCreateIntent } from "./jobHelpers";

export const maxStoredJobIntents = 16;
const keyPattern = /^[\da-f]{8}-(?:[\da-f]{4}-){3}[\da-f]{12}$/i;

export function loadJobIntents(
  storage: Storage,
  scope: string | undefined,
): Record<string, JobCreateIntent> {
  if (!scope) return {};
  try {
    const raw = storage.getItem(storageKey(scope));
    if (!raw || raw.length > 70_000) return {};
    const values: unknown = JSON.parse(raw);
    if (!Array.isArray(values) || values.length > maxStoredJobIntents)
      return {};
    const result: Record<string, JobCreateIntent> = {};
    for (const value of values) {
      const intent = parseStoredIntent(value);
      if (intent) result[intent.signature] = intent;
    }
    return result;
  } catch {
    return {};
  }
}

export function saveJobIntents(
  storage: Storage,
  scope: string | undefined,
  intents: Record<string, JobCreateIntent>,
): void {
  if (!scope) return;
  try {
    const values = Object.values(intents);
    if (values.length === 0) storage.removeItem(storageKey(scope));
    else storage.setItem(storageKey(scope), JSON.stringify(values));
  } catch {
    // Memory retains the key when sessionStorage is blocked. Never replay a
    // write automatically, and never store tokens or source/export content.
  }
}

function parseStoredIntent(value: unknown): JobCreateIntent | undefined {
  if (!value || typeof value !== "object") return undefined;
  const { signature, idempotencyKey } = value as Record<string, unknown>;
  if (
    typeof signature !== "string" ||
    signature.length > 4096 ||
    typeof idempotencyKey !== "string" ||
    !keyPattern.test(idempotencyKey)
  )
    return undefined;
  return validSignature(signature) ? { signature, idempotencyKey } : undefined;
}

function validSignature(signature: string): boolean {
  try {
    // Only typed metadata is restored. A corrupt neighbor must not discard a
    // valid pending key and turn an ambiguous response into a new command.
    const command = JSON.parse(signature) as Record<string, unknown>;
    if (
      !command ||
      Object.keys(command).some(
        (key) => !["jobType", "datasets"].includes(key),
      ) ||
      !["sync", "export"].includes(String(command.jobType)) ||
      !Array.isArray(command.datasets) ||
      command.datasets.length > 32
    )
      return false;
    return command.datasets.every(
      (item) => typeof item === "string" && /^[a-z0-9_.-]{1,80}$/.test(item),
    );
  } catch {
    return false;
  }
}

function storageKey(scope: string): string {
  return `aero-id:job-intents:v1:${encodeURIComponent(scope)}`;
}
