import { describe, expect, it } from "vitest";
import type { Dataset } from "../types";
import { commandTestReport, testDatasets } from "./commandTestFixtures";
import {
  isProjectionSource,
  sourceProjectionDatasets,
} from "./sourceProjectionScope";

const read = (
  datasets: Dataset[] = testDatasets,
  source: unknown = "aero-vault",
) => sourceProjectionDatasets(datasets, source, commandTestReport());

describe("source-local projection selection", () => {
  it.each(["snaplink", "aero-im", "aero-vault"])(
    "recognizes only the exact public source %s",
    (source) => {
      expect(isProjectionSource(source)).toBe(true);
      expect(read(testDatasets, source).map((item) => item.name)).toEqual(
        testDatasets
          .filter((item) => item.source === source)
          .map((item) => item.name),
      );
    },
  );
  it.each([
    undefined,
    null,
    "",
    "AERO-VAULT",
    "aero-vault ",
    "__proto__",
    ["aero-vault"],
    { source: "aero-vault" },
  ])(
    "rejects an unrecognized source %j without an all-source fallback",
    (source) => {
      expect(isProjectionSource(source)).toBe(false);
      expect(
        sourceProjectionDatasets(testDatasets, source, commandTestReport()),
      ).toEqual([]);
    },
  );
  it.each(
    [
      [{ name: "aero-im.workspaces", source: "aero-vault", pii: false }],
      [{ name: "aero-vault.raw_path", source: "aero-vault", pii: false }],
      [testDatasets[0]!, testDatasets[0]!],
      [{ name: "__proto__", source: "aero-vault", pii: false }],
      [{ name: "aero-vault.usage", source: "aero-vault", pii: "false" }],
    ].map((datasets) => ({ datasets })),
  )(
    "rejects a mismatched, unknown, duplicate or malformed source directory",
    ({ datasets }) => {
      expect(read(datasets as Dataset[])).toEqual([]);
    },
  );
  it("requires exact current source, registration and allowed local read qualification, not enabled/freshness or ACL", () => {
    const report = commandTestReport();
    for (const authorization of ["denied", "unknown"] as const) {
      report.datasets[0]!.authorization = authorization;
      expect(
        sourceProjectionDatasets(testDatasets, "aero-vault", report),
      ).toEqual([]);
    }
    report.datasets[0]!.authorization = "allowed";
    report.datasets[0]!.registered = false;
    expect(
      sourceProjectionDatasets(testDatasets, "aero-vault", report),
    ).toEqual([]);
    report.datasets[0]!.registered = true;
    report.datasets[0]!.source = "aero-im";
    expect(
      sourceProjectionDatasets(testDatasets, "aero-vault", report),
    ).toEqual([]);
    report.datasets[0]!.source = "aero-vault";
    report.datasets[0]!.enabled = false;
    expect(
      sourceProjectionDatasets(testDatasets, "aero-vault", report),
    ).toEqual([testDatasets[0]]);
    expect(report.datasets[0]!.resource_authorization).toBe("unknown");
    expect(
      sourceProjectionDatasets(testDatasets, "aero-vault", undefined),
    ).toEqual([]);
    expect(
      sourceProjectionDatasets(
        undefined as unknown as Dataset[],
        "aero-vault",
        report,
      ),
    ).toEqual([]);
    expect(read(null as unknown as Dataset[])).toEqual([]);
  });
});
