import type { CapabilityReport, Dataset } from "../types";
import { jobDatasetLabels, jobSourceLabels } from "./jobSummaryFields";

export function isProjectionSource(source: unknown): source is string {
  return typeof source === "string" && Object.hasOwn(jobSourceLabels, source);
}

// Navigation narrows the local account dataset scope, never a native resource ACL.
// Connector enabled/freshness are independent: permitted historical reads stay local.
export function sourceProjectionDatasets(
  datasets: readonly Dataset[],
  source: unknown,
  report: CapabilityReport | undefined,
): Dataset[] {
  if (!isProjectionSource(source) || !Array.isArray(datasets) || !report)
    return [];
  const candidates = datasets.filter((item) => item?.source === source);
  if (
    candidates.length > 11 ||
    new Set(candidates.map((item) => item.name)).size !== candidates.length ||
    candidates.some(
      (item) =>
        typeof item.name !== "string" ||
        !Object.hasOwn(jobDatasetLabels, item.name) ||
        !item.name.startsWith(`${source}.`) ||
        typeof item.pii !== "boolean",
    )
  )
    return [];
  return candidates.filter((item) =>
    report.datasets.some(
      (current) =>
        current.name === item.name &&
        current.source === source &&
        current.registered &&
        current.authorization === "allowed",
    ),
  );
}
