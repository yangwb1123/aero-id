import type { JsonRecord } from "../types";

export function syncJobID(record: JsonRecord): string | undefined {
  const value = record.job_id ?? record.id;
  return typeof value === "string" && value ? value : undefined;
}
