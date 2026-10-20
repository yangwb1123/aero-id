import type { AggregateView, CapabilityReport, JsonRecord } from "../types";

export const resourceDescriptors = [
  {
    dataset: "aero-vault.usage",
    source: "aero-vault",
    title: "Vault 租户用量",
    kind: "usage",
  },
  {
    dataset: "aero-vault.buckets",
    source: "aero-vault",
    title: "Vault 桶引用",
    kind: "buckets",
  },
  {
    dataset: "aero-im.workspaces",
    source: "aero-im",
    title: "IM 工作空间",
    kind: "workspaces",
  },
] as const;
export type ResourceKind = (typeof resourceDescriptors)[number]["kind"];
interface SummaryField {
  label: string;
  value: string;
}
export interface ResourceReference {
  id: string;
  name: string;
  slug?: string;
}
export interface ResourceSummary {
  dataset: string;
  source: string;
  title: string;
  kind: ResourceKind;
  state: string;
  detail: string;
  tone: "neutral" | "success" | "warning" | "danger";
  guidance: string;
  metadata: SummaryField[];
  fields: SummaryField[];
  references: ResourceReference[];
  listMessage: string;
  warnings: string[];
}
const missing = "来源未提供";
const invalid = "字段无效（未展示）";
const maxVisibleReferences = 50;
const maxSnapshots = 64;
export function summaryRecord(value: unknown): value is JsonRecord {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}
export function summaryText(
  value: unknown,
  maxLength = 512,
): string | undefined {
  return typeof value === "string" &&
    value.length > 0 &&
    value.length <= maxLength &&
    !/[\p{Cc}\p{Cf}]/u.test(value)
    ? value
    : undefined;
}
function textField(value: unknown): string {
  return value === undefined || value === null
    ? missing
    : (summaryText(value) ?? invalid);
}
function countField(value: unknown, unit: string): string {
  if (value === undefined || value === null) return missing;
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0
    ? `${value.toLocaleString("en-US")} ${unit}`
    : invalid;
}
function timeField(value: unknown): string {
  if (value === undefined || value === null) return missing;
  return typeof value === "string" &&
    value.length <= 40 &&
    /^\d{4}-\d{2}-\d{2}T.*(?:Z|[+-]\d{2}:\d{2})$/.test(value) &&
    Number.isFinite(Date.parse(value)) &&
    !value.startsWith("0001-")
    ? value
    : invalid;
}
function metadataOf(snapshot: JsonRecord): SummaryField[] {
  return [
    { label: "来源区域", value: textField(snapshot.source_region) },
    {
      label: "来源版本",
      value:
        snapshot.version === 0
          ? invalid
          : countField(snapshot.version, "（仅该来源）"),
    },
    { label: "来源生成时间", value: timeField(snapshot.generated_at) },
    { label: "最后同步时间", value: timeField(snapshot.last_synced_at) },
    { label: "投影到期时间", value: timeField(snapshot.expires_at) },
  ];
}
function guidanceFor(dataset: string, report?: CapabilityReport): string {
  const item = report?.datasets.find((entry) => entry.name === dataset);
  const eligibility =
    item?.authorization === "allowed"
      ? "本地读取资格：允许"
      : item?.authorization === "denied"
        ? "本地读取资格：拒绝"
        : "本地读取资格：未知";
  const connection = item
    ? item.enabled
      ? "摘要连接已启用"
      : "摘要连接未启用（历史投影仍可能存在）"
    : "连接配置未知";
  return `${connection} · ${eligibility}；不代表来源资源授权或健康。`;
}
function missingState(
  summary: ResourceSummary,
  code: unknown,
  report?: CapabilityReport,
): void {
  const item = report?.datasets.find((entry) => entry.name === summary.dataset);
  if (
    item?.authorization === "denied" ||
    code === "source.http_401" ||
    code === "source.http_403" ||
    code === "source.forbidden"
  ) {
    summary.state = "读取被拒绝";
    summary.detail = "当前不能读取此摘要；不会把拒绝解释为空资源。";
    summary.tone = "danger";
  } else if (code === "source.not_configured" || item?.enabled === false) {
    summary.state = "未接通";
    summary.detail = "摘要连接未启用；登记数据集不等于已接通来源。";
  } else if (code === "source.no_projection") {
    summary.state = "待同步";
    summary.detail = "本地尚无该数据集投影；可在同步与导出页显式选择范围。";
  } else if (code) {
    summary.state =
      code === "source.summary_incomplete" ? "来源结果不完整" : "来源故障";
    summary.detail = "本次没有可用摘要；缺失不表示资源数量为零。";
    summary.tone = "warning";
  } else {
    summary.state = "可用性未知";
    summary.detail =
      "尚未返回投影或明确来源状态；不推断已接通、已同步或空结果。";
  }
}
function invalidate(summary: ResourceSummary): void {
  summary.state = "摘要字段无效";
  summary.tone = "danger";
  summary.detail = "摘要形状或来源不匹配，未展示业务内容；请显式重读本地摘要。";
}
function usageFields(data: JsonRecord): SummaryField[] {
  return [
    { label: "用量所属租户", value: textField(data.tenant_id) },
    { label: "已报告存储用量", value: countField(data.used_bytes, "B") },
    { label: "已报告对象用量", value: countField(data.used_objects, "个对象") },
    {
      label: "存储配额上限（来源原值）",
      value: countField(data.max_bytes, "B"),
    },
    {
      label: "对象配额上限（来源原值）",
      value: countField(data.max_objects, "个对象"),
    },
  ];
}
function referenceOf(
  value: unknown,
  kind: ResourceKind,
): ResourceReference | undefined {
  if (kind === "buckets") {
    const name = summaryText(value);
    return name ? { id: name, name } : undefined;
  }
  if (!summaryRecord(value)) return undefined;
  const id = summaryText(value.id);
  if (!id) return undefined;
  return { id, name: textField(value.name), slug: textField(value.slug) };
}
function applyReferences(summary: ResourceSummary, data: JsonRecord): void {
  const label = summary.kind === "buckets" ? "桶引用" : "工作空间引用";
  if (data.items === undefined || data.items === null) {
    summary.listMessage = `引用列表：${missing}`;
    return;
  }
  if (!Array.isArray(data.items)) {
    summary.listMessage = "引用列表字段无效，未展示";
    summary.warnings.push("引用列表类型不符合已确认契约，不能当作空结果。");
    return;
  }
  // Validate only this bounded visible prefix; unshown source JSON is never expanded.
  const references = data.items
    .slice(0, maxVisibleReferences)
    .map((value) => referenceOf(value, summary.kind));
  const ids = new Set(references.map((item) => item?.id));
  if (references.some((item) => !item) || ids.size !== references.length) {
    summary.listMessage = "引用列表字段无效，未展示";
    summary.warnings.push("可见引用存在无效或重复标识，不能当作空结果。");
    return;
  }
  summary.references = references as ResourceReference[];
  summary.listMessage = references.length
    ? `本页显示 ${references.length} 条${label}（不是来源总数）`
    : data.truncated === true
      ? "投影已截断，不能确定引用是否为空"
      : `本次投影未列出${label}；不证明远端没有资源。`;
  if (data.items.length > maxVisibleReferences)
    summary.warnings.push(
      `页面仅显示前 ${maxVisibleReferences} 条引用，其余未验证或展示；不能据此计算总数。`,
    );
}
function applyFreshness(
  summary: ResourceSummary,
  snapshot: JsonRecord,
  view: AggregateView,
  now: number,
): boolean {
  if (snapshot.status === "unavailable" || snapshot.status === "unknown") {
    summary.state =
      snapshot.status === "unavailable" ? "投影不可用" : "投影状态未知";
    summary.detail =
      "投影当前不可用或状态未知；不把保留值当作当前资源，也不推断空结果。";
    return false;
  }
  const expired =
    typeof snapshot.expires_at === "string" &&
    Date.parse(snapshot.expires_at) <= now;
  const stale =
    snapshot.status === "stale" ||
    expired ||
    view.stale_datasets?.includes(summary.dataset);
  summary.state = stale ? "陈旧投影" : "本地投影可读";
  summary.tone = stale ? "warning" : "success";
  summary.detail = stale
    ? "下面是已保存的历史摘要，不代表来源当前状态。"
    : "本地区域投影，不是跨来源实时快照。";
  if (summary.metadata.some((field) => field.value === invalid)) {
    summary.state = "新鲜度待核实";
    summary.tone = "warning";
    summary.warnings.push(
      "部分来源元数据无效；下面仅展示已保存的摘要，无法据此确认新鲜度。",
    );
  }
  return true;
}
function applyContent(summary: ResourceSummary, data: JsonRecord): void {
  if (data.truncated !== undefined && typeof data.truncated !== "boolean") {
    invalidate(summary);
    return;
  }
  if (summary.kind === "workspaces" && data.status === "not_found") {
    if (summary.state === "陈旧投影")
      summary.warnings.push("未关联状态来自陈旧投影，不代表来源当前账户状态。");
    summary.state = "未关联 IM 账户";
    summary.detail =
      "来源报告尚无关联的 IM 账户，不等于工作空间为空；只读摘要不会创建来源账户。";
    summary.tone = "neutral";
    return;
  }
  if (data.truncated === true) {
    if (summary.state === "本地投影可读") summary.state = "部分投影（已截断）";
    summary.tone = "warning";
    summary.warnings.push(
      "来源投影已截断，未提供的字段或引用不能按零或空结果处理。",
    );
  }
  if (summary.kind === "usage") summary.fields = usageFields(data);
  else applyReferences(summary, data);
  if (
    summary.fields.some((field) => field.value === invalid) ||
    summary.references.some(
      (item) => item.name === invalid || item.slug === invalid,
    )
  )
    summary.warnings.push(
      "部分字段无效或超出安全显示范围，未展示原值；不会补零。",
    );
}
function applySnapshot(
  summary: ResourceSummary,
  snapshot: JsonRecord,
  view: AggregateView,
  now: number,
): void {
  summary.metadata = metadataOf(snapshot);
  if (
    snapshot.source !== summary.source ||
    !summaryRecord(snapshot.data) ||
    typeof snapshot.status !== "string" ||
    !["fresh", "stale", "unavailable", "unknown"].includes(snapshot.status)
  ) {
    invalidate(summary);
    return;
  }
  if (applyFreshness(summary, snapshot, view, now))
    applyContent(summary, snapshot.data);
}
export function resourceSummaries(
  view: AggregateView,
  report?: CapabilityReport,
  now = Date.now(),
  datasets?: readonly string[],
): ResourceSummary[] {
  const descriptors =
    datasets === undefined
      ? resourceDescriptors
      : resourceDescriptors.filter((item) => datasets.includes(item.dataset));
  return descriptors.map((descriptor) => {
    const summary: ResourceSummary = {
      ...descriptor,
      state: "可用性未知",
      detail: "",
      tone: "neutral",
      guidance: guidanceFor(descriptor.dataset, report),
      metadata: [],
      fields: [],
      references: [],
      listMessage: "",
      warnings: [],
    };
    if (
      !Array.isArray(view.snapshots) ||
      view.snapshots.length > maxSnapshots
    ) {
      invalidate(summary);
      return summary;
    }
    const matches = view.snapshots.filter(
      (snapshot) =>
        summaryRecord(snapshot) && snapshot.dataset === descriptor.dataset,
    );
    if (matches.length > 1) invalidate(summary);
    else if (!matches.length)
      missingState(summary, view.source_errors?.[descriptor.source], report);
    else if (
      report?.datasets.find((item) => item.name === descriptor.dataset)
        ?.authorization === "denied"
    )
      missingState(summary, "source.forbidden", report);
    else applySnapshot(summary, matches[0]!, view, now);
    if (view.source_errors?.[descriptor.source] && matches.length === 1) {
      const code = view.source_errors[descriptor.source];
      summary.warnings.push(
        code === "source.http_401" || code === "source.http_403"
          ? "该来源报告读取被拒绝；已有投影不证明本次来源读取成功或当前资源授权。"
          : "该来源还报告了连接、读取或覆盖问题；已有投影不证明本次来源读取成功。",
      );
    }
    return summary;
  });
}
