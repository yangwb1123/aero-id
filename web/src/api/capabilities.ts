import { AeroIdError } from "./transport";
import type { CapabilityReport } from "../types";

const access = new Set(["allowed", "denied", "unknown"]);
const statuses = new Set(["fresh", "stale", "unavailable", "unknown"]);
const record = (value: unknown): value is Record<string, unknown> =>
  Boolean(value) && typeof value === "object" && !Array.isArray(value);
const text = (value: unknown): value is string =>
  typeof value === "string" && value.length > 0 && value.length <= 512;

export function parseCapabilities(value: unknown): CapabilityReport {
  if (!validReport(value))
    throw new AeroIdError(
      0,
      "response.invalid",
      "能力报告格式无效；不会推断权限",
    );
  return value as unknown as CapabilityReport;
}

function validReport(value: unknown): boolean {
  if (
    !record(value) ||
    value.schema_version !== 1 ||
    value.authorization_version !== "unsupported"
  )
    return false;
  if (
    ![
      value.account_id,
      value.tenant_id,
      value.client_id,
      value.issuer,
      value.home_region,
      value.local_region,
      value.generated_at,
    ].every(text)
  )
    return false;
  if (
    !Number.isFinite(Date.parse(String(value.generated_at))) ||
    typeof value.partial !== "boolean" ||
    !Array.isArray(value.capabilities) ||
    !Array.isArray(value.datasets)
  )
    return false;
  if (value.capabilities.length > 32 || value.datasets.length > 32)
    return false;
  if (
    !value.capabilities.every(validFeature) ||
    !value.datasets.every(validDataset)
  )
    return false;
  return (
    new Set(value.capabilities.map((item) => item.name)).size ===
      value.capabilities.length &&
    new Set(value.datasets.map((item) => item.name)).size ===
      value.datasets.length
  );
}

function validFeature(value: unknown): boolean {
  if (!record(value) || !text(value.name)) return false;
  if (
    ![value.registered, value.enabled, value.available].every(
      (item) => typeof item === "boolean",
    )
  )
    return false;
  if (
    !access.has(String(value.authorization)) ||
    !["self", "global"].includes(String(value.authorization_scope))
  )
    return false;
  // Server eligibility cannot manufacture a grant from unavailable/unknown.
  return (
    !value.available ||
    (value.registered === true &&
      value.enabled === true &&
      value.authorization === "allowed")
  );
}

function validDataset(value: unknown): boolean {
  if (
    !record(value) ||
    !text(value.name) ||
    !["snaplink", "aero-im", "aero-vault"].includes(String(value.source))
  )
    return false;
  if (
    ![value.pii, value.registered, value.enabled, value.fresh].every(
      (item) => typeof item === "boolean",
    )
  )
    return false;
  return (
    access.has(String(value.authorization)) &&
    value.resource_authorization === "unknown" &&
    statuses.has(String(value.status)) &&
    typeof value.version === "number" &&
    Number.isSafeInteger(value.version) &&
    value.version >= 0
  );
}

export function capabilityAvailable(
  report: CapabilityReport | undefined,
  names: string[],
): boolean {
  return Boolean(
    report?.capabilities.some(
      (item) =>
        names.includes(item.name) &&
        item.available &&
        item.registered &&
        item.enabled &&
        item.authorization === "allowed",
    ),
  );
}
