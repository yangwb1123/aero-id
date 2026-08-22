import { IrisBadge, IrisEmptyState } from "@iris-ui-kit/react";
import type { JsonRecord } from "../types";

export interface RecordColumn {
  key: string;
  label: string;
  aliases?: string[];
  kind?: "text" | "status" | "date";
}

function valueOf(record: JsonRecord, column: RecordColumn): unknown {
  for (const key of [column.key, ...(column.aliases ?? [])]) {
    if (record[key] !== undefined && record[key] !== null) return record[key];
  }
  return undefined;
}

function statusTone(
  value: string,
): "success" | "warning" | "danger" | "neutral" | "primary" {
  if (
    ["fresh", "active", "completed", "delivered", "succeeded"].includes(value)
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

function display(value: unknown, kind: RecordColumn["kind"]): React.ReactNode {
  if (value === undefined || value === null || value === "")
    return <span className="muted">—</span>;
  if (kind === "status") {
    const text = String(value);
    return <IrisBadge tone={statusTone(text)}>{text}</IrisBadge>;
  }
  if (kind === "date") {
    const date = new Date(String(value));
    return Number.isNaN(date.getTime()) ? String(value) : date.toLocaleString();
  }
  if (typeof value === "object") return JSON.stringify(value);
  return String(value);
}

export function RecordTable({
  records,
  columns,
  empty = "暂无数据",
}: {
  records: JsonRecord[];
  columns: RecordColumn[];
  empty?: string;
}): React.ReactElement {
  if (records.length === 0)
    return (
      <IrisEmptyState title={empty} description="当前筛选条件没有记录。" />
    );
  return (
    <div className="table-scroll">
      <table className="platform-table">
        <thead>
          <tr>
            {columns.map((column) => (
              <th key={column.key}>{column.label}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {records.map((record, index) => (
            <tr
              key={String(
                record.id ?? record.event_id ?? record.account_id ?? index,
              )}
            >
              {columns.map((column) => (
                <td key={column.key}>
                  {display(valueOf(record, column), column.kind)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
