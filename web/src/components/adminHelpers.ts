import type { JsonRecord } from "../types";

export function adminAccountID(record: JsonRecord): string | undefined {
  const value = record.account_id ?? record.id;
  return typeof value === "string" && value ? value : undefined;
}

export function records(value: unknown): JsonRecord[] {
  return Array.isArray(value)
    ? value.filter(
        (item): item is JsonRecord =>
          Boolean(item) && typeof item === "object" && !Array.isArray(item),
      )
    : [];
}
