// Synthetic, content-free reference/usage fixtures; not a deployed source contract.
export const datasets = [
  { name: "aero-vault.usage", source: "aero-vault", pii: false },
  { name: "aero-vault.buckets", source: "aero-vault", pii: false },
  { name: "aero-im.workspaces", source: "aero-im", pii: false },
];
const account = {
  account_id: "01800000-0000-7000-8000-000000000002",
  tenant_id: "tenant-demo",
  home_region: "local",
};
function snapshot(dataset, data) {
  return {
    dataset,
    source: dataset.startsWith("aero-vault.") ? "aero-vault" : "aero-im",
    source_region: "local",
    status: "fresh",
    version: 42,
    generated_at: "2026-10-16T09:00:00Z",
    last_synced_at: "2026-10-16T09:01:00Z",
    data,
  };
}
export function resourceFixture(scenario) {
  const result = {
    account,
    consistency: "eventual",
    partial: false,
    source_errors: {},
    stale_datasets: [],
    snapshots: [
      snapshot("aero-vault.usage", {
        tenant_id: "tenant-demo",
        used_bytes: 1048576,
        used_objects: 12,
        max_bytes: 2097152,
        max_objects: 100,
        token: "UNAPPROVED-FIELD-MUST-NOT-RENDER",
      }),
      snapshot("aero-vault.buckets", {
        items: ["design-assets", "project-backups"],
      }),
      snapshot("aero-im.workspaces", {
        items: [
          {
            id: "workspace-demo",
            name: "设计协作",
            slug: "design",
            message_body: "UNAPPROVED-FIELD-MUST-NOT-RENDER",
          },
        ],
      }),
    ],
  };
  if (scenario === "empty") {
    result.snapshots[0].data = { tenant_id: "tenant-demo", used_bytes: 0 };
    result.snapshots[1].data = { items: [] };
    result.snapshots[2].data = { items: [] };
  } else if (scenario === "missing") {
    result.snapshots = [];
    result.partial = true;
    result.source_errors = {
      "aero-vault": "source.no_projection",
      "aero-im": "source.not_configured",
    };
  } else if (scenario === "partial") {
    result.partial = true;
    result.stale_datasets = ["aero-vault.usage"];
    result.snapshots[1].data = {
      truncated: true,
      items: Array.from(
        { length: 51 },
        (_, index) => `bucket-${String(index).padStart(2, "0")}`,
      ),
    };
    result.snapshots[2].data = { status: "not_found" };
  } else if (scenario === "malformed") {
    result.snapshots[0].data = {
      used_bytes: "not-a-byte-count",
      max_bytes: Number.MAX_SAFE_INTEGER + 1,
    };
    result.snapshots[1].data = { items: [{ id: "not-a-bucket-name" }] };
    result.snapshots[2].source = "aero-vault";
  } else if (scenario === "fault") {
    result.snapshots = [];
    result.partial = true;
    result.source_errors = {
      "aero-vault": "source.unavailable",
      "aero-im": "source.http_403",
    };
  }
  return result;
}
export function capabilityFixture(scenario) {
  return {
    schema_version: 1,
    ...account,
    client_id: "browser-fixture",
    issuer: "https://fixture.invalid",
    local_region: "local",
    generated_at: new Date().toISOString(),
    authorization_version: "unsupported",
    partial: false,
    capabilities: [],
    datasets: datasets.map((dataset) => ({
      ...dataset,
      registered: true,
      enabled: scenario !== "missing" || dataset.source !== "aero-im",
      authorization:
        scenario === "denied"
          ? "denied"
          : scenario === "unknown"
            ? "unknown"
            : "allowed",
      resource_authorization: "unknown",
      status: "fresh",
      fresh: true,
      version: 42,
    })),
  };
}
