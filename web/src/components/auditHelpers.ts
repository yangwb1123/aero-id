import type { JsonRecord } from "../types";

export function auditEventID(record: JsonRecord): string | undefined {
  const value = record.event_id ?? record.id;
  return typeof value === "string" && value ? value : undefined;
}
