import type { JsonRecord } from "../types";

export function operationID(record: JsonRecord): string | undefined {
  const value = record.operation_id ?? record.id;
  return typeof value === "string" && value ? value : undefined;
}
