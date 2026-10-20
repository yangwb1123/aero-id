import assert from "node:assert/strict";
import { capabilityFixture } from "./resources-data.mjs";
import {
  accountDatasets,
  accountSummaryFixture,
} from "./account-summary-data.mjs";

const source = {
  id: "01800000-0000-7000-8000-000000000031",
  source: "aero-vault",
  source_region: "local",
  source_account_id: "vault-native-account",
  scope_type: "tenant",
  scope_id: "same-opaque-reference",
  status: "active",
  version: 17,
  generated_at: "2026-10-16T00:00:00Z",
  last_synced_at: "2026-10-16T00:00:01Z",
  data: {
    token: "UNAPPROVED-FIELD-MUST-NOT-RENDER",
    acl: "UNAPPROVED-FIELD-MUST-NOT-RENDER",
  },
};
const membership = {
  id: "01800000-0000-7000-8000-000000000021",
  source: "aero-im",
  source_region: "local",
  source_member_id: "im-native-member",
  scope_type: "workspace",
  scope_id: "same-opaque-reference",
  role: "owner",
  status: "active",
  version: 11,
  last_synced_at: "2026-10-16T00:00:02Z",
  data: { body: "UNAPPROVED-FIELD-MUST-NOT-RENDER" },
};
export function connectionsAPI(url, scenario) {
  if (url.pathname === "/v1/me/capabilities") {
    const report = capabilityFixture("healthy");
    const state =
      scenario === "connections-denied"
        ? "denied"
        : scenario === "connections-unknown"
          ? "unknown"
          : "allowed";
    report.capabilities = [
      {
        name: "account.self.read",
        registered: true,
        enabled: true,
        authorization: state,
        authorization_scope: "self",
        available: state === "allowed",
      },
    ];
    if (
      [
        "connections-projection-denied-dataset",
        "connections-projection-unknown-dataset",
      ].includes(scenario)
    ) {
      for (const dataset of report.datasets)
        if (dataset.source === "aero-vault")
          dataset.authorization = scenario.includes("denied")
            ? "denied"
            : "unknown";
    }
    return { data: report };
  }
  if (url.pathname === "/v1/me")
    return {
      data: {
        account: accountSummaryFixture("healthy").account,
        sources: [],
        memberships: [],
      },
    };
  if (url.pathname === "/v1/account-datasets") {
    if (scenario === "connections-projection-catalog-error")
      return {
        status: 503,
        error: { code: "datasets.unavailable", message: "受控目录读取故障" },
      };
    return { data: { datasets: accountDatasets } };
  }
  if (url.pathname === "/v1/me/data") {
    const selected = url.searchParams.getAll("dataset");
    const source =
      scenario === "connections-projection-im" ? "aero-im" : "aero-vault";
    assert(
      selected.length > 0 &&
        selected.every((name) =>
          accountDatasets.some(
            (item) => item.name === name && item.source === source,
          ),
        ),
    );
    assert.equal(url.searchParams.get("consistency"), "eventual");
    assert(
      [...url.searchParams.keys()].every((key) =>
        ["dataset", "consistency"].includes(key),
      ),
    );
    if (scenario === "connections-projection-read-error")
      return {
        status: 503,
        error: { code: "aggregation.unavailable", message: "受控投影读取故障" },
      };
    return {
      data: accountSummaryFixture(
        scenario === "connections-projection-partial" ? "partial" : "healthy",
        selected,
      ),
    };
  }
  if (["/v1/me/resources", "/v1/me/overview"].includes(url.pathname))
    return { data: accountSummaryFixture("healthy") };
  assert(["/v1/me/sources", "/v1/me/memberships"].includes(url.pathname));
  assert.equal(url.searchParams.get("limit"), "50");
  assert(
    [...url.searchParams.keys()].every((key) =>
      ["limit", "cursor"].includes(key),
    ),
  );
  const isSource = url.pathname.endsWith("/sources");
  const field = isSource ? "sources" : "memberships";
  if (scenario === "connections-source-error" && isSource)
    return {
      status: 503,
      error: {
        code: "account.sources_failed",
        message: "受控来源引用读取故障",
      },
    };
  if (scenario === "connections-page-error" && url.searchParams.has("cursor"))
    return {
      status: 503,
      error: {
        code: "account.memberships_failed",
        message: "受控后续页读取故障",
      },
    };
  if (scenario === "connections-malformed" && isSource)
    return { data: { sources: null, next_cursor: "" } };
  if (scenario === "connections-empty")
    return { data: { [field]: [], next_cursor: "" } };
  const row = structuredClone(isSource ? source : membership);
  if (url.searchParams.has("cursor")) {
    assert.equal(url.searchParams.get("cursor"), membership.id);
    assert(!isSource);
    return {
      data: {
        memberships: [
          {
            ...row,
            id: "01800000-0000-7000-8000-000000000020",
            scope_type: "bucket",
            source: "aero-vault",
            source_member_id: "vault-native-member",
            role: "reader",
            scope_id: "bucket-native-scope",
          },
        ],
        next_cursor: "",
      },
    };
  }
  if (scenario === "connections-missing") {
    for (const key of [
      "version",
      "last_synced_at",
      "generated_at",
      "source_region",
      "source_account_id",
      "source_member_id",
      "scope_type",
      "scope_id",
      "role",
    ])
      delete row[key];
    row.status = "unrecognized-state";
  }
  if (scenario === "connections-unavailable") row.status = "unavailable";
  if (isSource && scenario === "connections-projection-im") {
    row.source = "aero-im";
    row.source_account_id = "im-native-account";
    row.scope_type = "workspace";
  }
  return {
    data: {
      [field]: [row],
      next_cursor:
        !isSource &&
        [
          "connections-healthy",
          "connections-page-error",
          "connections-page-recovered",
        ].includes(scenario)
          ? membership.id
          : "",
    },
  };
}
