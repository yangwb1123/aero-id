import type { JsonRecord, PageData } from "../types";

export interface RecordColumn {
  key: string;
  label: string;
  aliases?: string[];
  kind?: "text" | "status" | "date";
}

export function mergePage<T>(
  current: PageData<T>,
  next: PageData<T>,
  requestedCursor: string,
): PageData<T> {
  if (next.nextCursor === requestedCursor) {
    throw new Error("分页游标没有前进，请刷新后重试");
  }
  return {
    items: [...current.items, ...next.items],
    nextCursor: next.nextCursor,
  };
}

export function valueOf(record: JsonRecord, column: RecordColumn): unknown {
  for (const key of [column.key, ...(column.aliases ?? [])]) {
    if (record[key] !== undefined && record[key] !== null) return record[key];
  }
  return undefined;
}

export function statusTone(
  value: string,
): "success" | "warning" | "danger" | "neutral" | "primary" {
  if (
    [
      "fresh",
      "active",
      "completed",
      "delivered",
      "succeeded",
      "ready",
    ].includes(value)
  )
    return "success";
  if (
    ["stale", "pending", "running", "unknown", "partially_completed"].includes(
      value,
    )
  )
    return "warning";
  if (["failed", "unavailable", "blocked", "dead_letter"].includes(value))
    return "danger";
  return "neutral";
}
