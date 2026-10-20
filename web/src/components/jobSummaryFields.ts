import type { JsonRecord } from "../types";

// Public registry in internal/domain/aggregation.go; display-only, never a grant.
export const jobDatasetLabels: Record<string, string> = {
  "snaplink.identity": "Snaplink 身份",
  "snaplink.tenants": "Snaplink 租户",
  "snaplink.permissions": "Snaplink 权限",
  "snaplink.security": "Snaplink 安全摘要",
  "aero-im.profile": "IM 资料",
  "aero-im.workspaces": "IM 工作空间",
  "aero-im.activity_summary": "IM 活动摘要",
  "aero-im.notifications": "IM 通知摘要",
  "aero-vault.tenants": "Vault 租户",
  "aero-vault.buckets": "Vault 桶引用",
  "aero-vault.usage": "Vault 用量",
};
export const jobSourceLabels: Record<string, string> = {
  snaplink: "Snaplink",
  "aero-im": "Aero IM",
  "aero-vault": "Aero Vault",
};
export function jobRecord(value: unknown): JsonRecord | undefined {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as JsonRecord)
    : undefined;
}
export function jobText(value: unknown): string {
  return typeof value === "string" && value.length > 0 && value.length <= 512
    ? value
    : "来源未提供";
}
export function jobCount(value: unknown): string {
  if (value === undefined || value === null) return "来源未提供";
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0
    ? value.toLocaleString("en-US")
    : "字段无效（未展示）";
}
export function jobFlag(value: unknown, yes: string, no: string): string {
  if (value === undefined || value === null) return "来源未提供";
  if (typeof value !== "boolean") return "字段无效（未展示）";
  return value ? yes : no;
}
function validCalendar(value: string): boolean {
  const [year, month, day, hour, minute, second] = value
    .slice(0, 19)
    .split(/[-T:]/)
    .map(Number);
  const days = new Date(Date.UTC(year, month, 0)).getUTCDate();
  return (
    month >= 1 &&
    month <= 12 &&
    day >= 1 &&
    day <= days &&
    hour <= 23 &&
    minute <= 59 &&
    second <= 59
  );
}
export function jobTimeValue(value: unknown): number | undefined {
  if (
    typeof value !== "string" ||
    value.length > 64 ||
    !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/.test(
      value,
    ) ||
    !validCalendar(value)
  )
    return undefined;
  const time = Date.parse(value);
  return Number.isFinite(time) ? time : undefined;
}
export function jobTime(value: unknown): string {
  if (value === undefined || value === null) return "来源未提供";
  const time = jobTimeValue(value);
  return time === undefined
    ? "字段无效（未展示）"
    : new Date(time)
        .toISOString()
        .replace("T", " ")
        .replace(/\.\d{3}Z$/, " UTC");
}
export function jobDatasets(value: unknown): string[] | undefined {
  if (!Array.isArray(value) || value.length > 11) return undefined;
  if (
    !value.every(
      (name) =>
        typeof name === "string" && Object.hasOwn(jobDatasetLabels, name),
    )
  )
    return undefined;
  return new Set(value).size === value.length ? value : undefined;
}
export function jobScope(job: JsonRecord) {
  const datasets = jobDatasets(jobRecord(job.request)?.datasets);
  return {
    datasets: datasets ?? [],
    issue: !datasets?.length
      ? "原任务未提供可确认的数据集范围；不推断为全部数据集。"
      : undefined,
  };
}
