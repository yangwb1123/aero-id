import { datasets, resourceFixture } from "./resources-data.mjs";

export const accountDatasets = [
  ...datasets,
  { name: "snaplink.identity", source: "snaplink", pii: true },
];
export function accountSummaryFixture(scenario, selected = []) {
  const view = resourceFixture(scenario.replace(/^account-/, ""));
  view.snapshots.push({
    dataset: "snaplink.identity",
    source: "snaplink",
    source_region: "local",
    status: "fresh",
    version: 42,
    generated_at: "2026-10-16T09:00:00Z",
    last_synced_at: "2026-10-16T09:01:00Z",
    data: { unknown: { token: "UNAPPROVED-FIELD-MUST-NOT-RENDER" } },
  });
  if (selected.length)
    view.snapshots = view.snapshots.filter((item) =>
      selected.includes(item.dataset),
    );
  return view;
}
