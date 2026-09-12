import type { LegacyCutoverStatus } from "../api/migration";
import type { JsonRecord } from "../types";

export const statusLabels: Record<LegacyCutoverStatus, string> = {
  legacy: "Legacy serving",
  shadow: "Shadow comparison",
  cut_over: "Cut over",
  rolled_back: "Rolled back",
};

export function cutoverTransitions(
  status: LegacyCutoverStatus,
): LegacyCutoverStatus[] {
  if (status === "legacy") return ["shadow"];
  if (status === "shadow") return ["cut_over"];
  if (status === "cut_over") return ["rolled_back"];
  return ["legacy", "shadow"];
}

export function cutoverStatus(value: unknown): LegacyCutoverStatus | undefined {
  return value === "legacy" ||
    value === "shadow" ||
    value === "cut_over" ||
    value === "rolled_back"
    ? value
    : undefined;
}

export function records(value: unknown): JsonRecord[] {
  return Array.isArray(value)
    ? value.filter(
        (item): item is JsonRecord =>
          Boolean(item) && typeof item === "object" && !Array.isArray(item),
      )
    : [];
}

export function statusTone(
  status: LegacyCutoverStatus,
): "neutral" | "primary" | "warning" | "danger" {
  if (status === "shadow") return "primary";
  if (status === "cut_over") return "warning";
  if (status === "rolled_back") return "danger";
  return "neutral";
}
