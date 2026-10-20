import type { JsonRecord } from "../types";
import type { RecordColumn } from "./table";

export type ReferenceKind = "sources" | "memberships";
export const referenceMissing = "来源未提供";
export const referenceInvalid = "字段无效，未采用";
const sourceNames: Record<string, string> = {
  snaplink: "Snaplink",
  "aero-im": "IM",
  "aero-vault": "Vault",
};
const scopeNames: Record<string, string> = {
  tenant: "租户",
  workspace: "工作空间",
  bucket: "桶",
};
const statusNames: Record<string, string> = {
  active: "活跃记录（active）",
  inactive: "非活跃记录（inactive）",
  unavailable: "不可用记录（unavailable）",
  unknown: "未知（unknown）",
};
export function referenceText(value: unknown): string {
  if (value === undefined || value === null || value === "")
    return referenceMissing;
  if (typeof value !== "string" || value.length > 512) return referenceInvalid;
  for (const char of value) {
    const code = char.charCodeAt(0);
    if (code < 32 || (code >= 127 && code <= 159)) return referenceInvalid;
  }
  return value;
}
function named(
  value: unknown,
  names: Record<string, string>,
  fallback: string,
): string {
  const text = referenceText(value);
  return Object.hasOwn(names, text) ? names[text]! : fallback;
}
function version(value: unknown): string {
  if (value === undefined || value === null) return referenceMissing;
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0
    ? value.toLocaleString("en-US")
    : referenceInvalid;
}
function timestamp(value: unknown): string {
  const text = referenceText(value);
  if (text === referenceMissing || text === referenceInvalid) return text;
  return /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(
    text,
  ) && Number.isFinite(Date.parse(text))
    ? text
    : referenceInvalid;
}
// Exact HTTP fields only. No aliases, nested data/ACLs, JSON or source calls.
export function referenceSummary(
  row: JsonRecord,
  kind: ReferenceKind,
): JsonRecord {
  const source = named(row.source, sourceNames, "来源未识别");
  const scope = named(row.scope_type, scopeNames, "作用域类型未识别");
  return {
    id: referenceText(row.id),
    source_label: source,
    source: referenceText(row.source),
    scope_label: `${source} ${scope}`,
    scope_type: referenceText(row.scope_type),
    scope_id: referenceText(row.scope_id),
    native_id: referenceText(
      kind === "sources" ? row.source_account_id : row.source_member_id,
    ),
    role: kind === "memberships" ? referenceText(row.role) : referenceMissing,
    status_label: named(row.status, statusNames, "状态未确认"),
    status: referenceText(row.status),
    source_region: referenceText(row.source_region),
    version: version(row.version),
    generated_at:
      kind === "sources" ? timestamp(row.generated_at) : referenceMissing,
    last_synced_at: timestamp(row.last_synced_at),
    error_code:
      kind === "sources" ? referenceText(row.error_code) : referenceMissing,
  };
}
export function referenceColumns(kind: ReferenceKind): RecordColumn[] {
  return [
    { key: "source_label", label: "来源" },
    { key: "scope_label", label: "原生作用域" },
    { key: "scope_id", label: "原作用域 ID" },
    ...(kind === "memberships" ? [{ key: "role", label: "来源角色原值" }] : []),
    { key: "status_label", label: "记录状态" },
  ];
}
