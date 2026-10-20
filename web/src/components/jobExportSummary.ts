import type { JsonRecord } from "../types";
import {
  jobDatasets,
  jobRecord,
  jobScope,
  jobTime,
  jobTimeValue,
} from "./jobSummaryFields";

function boundToJob(result: JsonRecord, job: JsonRecord): boolean {
  return (
    ["job_id", "account_id", "tenant_id"].every(
      (field) => result[field] === undefined || result[field] === job[field],
    ) &&
    (result.type === undefined || result.type === job.job_type)
  );
}
function sameDatasets(left: string[], right: string[]): boolean {
  return (
    left.length === right.length && left.every((name) => right.includes(name))
  );
}
function accountScopeIssue(
  result: JsonRecord,
  job: JsonRecord,
): string | undefined {
  const requested = jobScope(job);
  if (
    result.complete === true &&
    (result.coverage_complete === false ||
      (Array.isArray(result.missing_datasets) &&
        result.missing_datasets.length > 0))
  )
    return "清单完整性字段互相矛盾；请核实原任务，不推断全部范围完成。";
  if (result.selected_datasets !== undefined) {
    const selected = jobDatasets(result.selected_datasets);
    if (
      requested.issue ||
      !selected ||
      !sameDatasets(selected, requested.datasets)
    )
      return "清单范围与原请求不能确认一致；不采用这份结果元数据。";
  }
  if (result.missing_datasets !== undefined) {
    const missing = jobDatasets(result.missing_datasets);
    if (
      requested.issue ||
      !missing ||
      missing.some((name) => !requested.datasets.includes(name))
    )
      return "缺失数据集字段无效；不采用这份结果元数据。";
  }
  return undefined;
}
export function jobExportMetadata(job: JsonRecord) {
  const result = jobRecord(job.result);
  let issue: string | undefined;
  if (job.result !== undefined && job.result !== null && !result)
    issue = "结果元数据字段无效；请核实原任务。";
  else if (result && !boundToJob(result, job))
    issue = "结果元数据与原任务不一致；请核实原回执。";
  else if (result?.schema_version !== undefined && result.schema_version !== 1)
    issue = "结果元数据版本不支持；请核实原任务。";
  else if (result && job.job_type === "export")
    issue = accountScopeIssue(result, job);
  return { result: issue ? {} : (result ?? {}), issue };
}
export interface JobDownloadState {
  blocked: boolean;
  title: string;
  detail: string;
}
function stopped(title: string, detail: string): JobDownloadState {
  return { blocked: true, title, detail };
}
function statusConstraint(status: unknown): JobDownloadState | undefined {
  if (status === "completed") return undefined;
  if (status === "unknown")
    return stopped(
      "制品结果未知，暂不能下载",
      "只核对原制品，不重新生成或延长有效期；暂时不存在也不授权生成第二份导出。",
    );
  if (status === "partially_completed")
    return stopped(
      "部分完成，尚无可下载完成证明",
      "查看下方完整性与错误信息；已有计数不是制品已发布的证明。",
    );
  return stopped(
    "任务尚未报告可下载完成",
    "先查询原任务；不自动重试、创建新导出或延长有效期。",
  );
}
function artifactConstraint(
  result: JsonRecord,
  now: number,
): JobDownloadState | undefined {
  if (
    result.artifact_deleted_at !== undefined &&
    result.artifact_deleted_at !== null
  )
    return stopped(
      "制品已消费或清理",
      "原指针已处置；这不保证此前响应已保存，也不授权重生成或延长有效期。",
    );
  if (result.expires_at === undefined || result.expires_at === null)
    return undefined;
  const expiry = jobTimeValue(result.expires_at);
  if (expiry === undefined)
    return stopped(
      "制品有效期字段无效",
      "不能确认下载条件；请只刷新原任务核实，不创建替代导出。",
    );
  if (now >= expiry)
    return stopped(
      "制品已到期",
      "本地时钟已到所报告期限，暂停下载；服务端时钟与原 TTL 才是最终依据。刷新不会续期。",
    );
  return undefined;
}
export function jobDownloadState(
  job: JsonRecord,
  now: number,
  received = false,
): JobDownloadState {
  if (received)
    return stopped(
      "本页已接收一次性下载",
      "请检查浏览器下载记录与保存位置；本页不再次消费，不保证文件已保存到磁盘。",
    );
  const status = statusConstraint(job.status);
  if (status) return status;
  const { result, issue } = jobExportMetadata(job);
  if (issue) return stopped("制品元数据不能确认", issue);
  const artifact = artifactConstraint(result, now);
  if (artifact) return artifact;
  if (result.expires_at === undefined || result.expires_at === null)
    return {
      blocked: false,
      title: "制品有效期未提供",
      detail:
        "不能确认原制品仍可用；历史任务可显式请求一次，服务端仍须核对原指针、有效期与当前读取权限。",
    };
  return {
    blocked: false,
    title: "可请求一次性下载，仍须服务端核验",
    detail: `所报告到期时间：${jobTime(result.expires_at)}；未到期不等于制品存在或当前有权读取。`,
  };
}
