import { capabilityAvailable } from "./api/capabilities";
import type { CapabilityReport } from "./types";

const requirements: Record<string, string[]> = {
  "admin-accounts": ["account.admin.read"],
  "admin-governance": ["audit.read", "system.config", "user.ban"],
  migration: ["account.admin.manage"],
  audit: ["audit.read"],
  compliance: ["audit.read", "audit.export", "system.config"],
  callbacks: ["callback.manage"],
  replays: ["audit.replay"],
};

export function routeEligibility(
  route: string,
  report: CapabilityReport | undefined,
): { available: boolean; reason: string } {
  if (route === "services") return { available: true, reason: "" };
  const names = requirements[route] ?? ["account.self.read"];
  if (capabilityAvailable(report, names))
    return { available: true, reason: "" };
  const features = report?.capabilities.filter((item) =>
    names.includes(item.name),
  );
  if (!features?.length) return { available: false, reason: "能力尚未确认" };
  if (features.every((item) => !item.enabled))
    return { available: false, reason: "未配置" };
  if (features.some((item) => item.authorization === "unknown"))
    return { available: false, reason: "权限未知" };
  if (
    features.some((item) => item.reason_code === "account.home_region_required")
  )
    return { available: false, reason: "需要主区域" };
  return { available: false, reason: "无权限或不可用" };
}
