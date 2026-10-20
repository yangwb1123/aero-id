import { describe, expect, it } from "vitest";
import type { CommandPlan } from "../api/commandIntent";
import { commandAvailable, sameCapabilityContext } from "./commandEligibility";
import {
  commandTestReport,
  testAccountID,
  testOtherAccountID,
  testScope,
} from "./commandTestFixtures";
import {
  operationReceiptPlan,
  operationActionAvailable,
  jobDownloadAvailable,
  jobReconcileAvailable,
} from "./taskActionEligibility";

const operation: CommandPlan = {
  kind: "operation",
  operation_type: "sync",
  datasets: ["aero-im.workspaces"],
  steps: [
    {
      step_key: "summary",
      source: "aero-im",
      action: "summary",
      request_payload: { datasets: ["aero-im.workspaces"] },
      compensation_action: "erase",
    },
  ],
};
const receipt = () => ({
  operation_id: testOtherAccountID,
  account_id: testAccountID,
  actor_uid: "initiator",
  tenant_id: "tenant-a",
  home_region: "local",
  operation_type: "sync",
  steps: operation.steps!.map((step) => ({
    step_key: step.step_key,
    source: step.source,
    action: step.action,
    compensation_action: step.compensation_action,
    dataset_scope: {
      status: "explicit",
      datasets: step.request_payload!.datasets,
    },
  })),
});
describe("current per-action guidance", () => {
  it("never permits an absent report, mismatched caller, hidden scope or ownership-only worker grant", () => {
    const plan: CommandPlan = {
      kind: "sync",
      datasets: ["aero-im.workspaces"],
    };
    expect(commandAvailable(undefined, plan, testAccountID)).toBe(false);
    expect(
      commandAvailable(
        commandTestReport(),
        { ...plan, datasets: [] },
        testAccountID,
      ),
    ).toBe(false);
    expect(
      sameCapabilityContext(
        { ...commandTestReport(), client_id: "another" },
        testScope,
      ),
    ).toBe(false);
    const report = commandTestReport();
    report.capabilities.find(
      (item) => item.name === "source.sync",
    )!.authorization = "denied";
    expect(commandAvailable(report, plan, testAccountID)).toBe(false);
  });
  it("requires compensation's actual erase grant plus root/action grants, not just operation.manage", () => {
    const report = commandTestReport();
    expect(commandAvailable(report, operation, testAccountID)).toBe(true);
    for (const permission of [
      "operation.manage",
      "source.sync",
      "account.erase",
    ]) {
      const denied = commandTestReport();
      denied.capabilities.find((item) => item.name === permission)!.available =
        false;
      expect(commandAvailable(denied, operation, testAccountID)).toBe(false);
    }
    expect(
      commandAvailable(
        report,
        { ...operation, operation_type: "replay" },
        testAccountID,
      ),
    ).toBe(true);
    report.capabilities.find(
      (item) => item.name === "audit.replay",
    )!.authorization = "unknown";
    expect(
      commandAvailable(
        report,
        { ...operation, operation_type: "replay" },
        testAccountID,
      ),
    ).toBe(false);
  });
  it("does not confuse summary enablement with erase flags or forbid explicit historical exports", () => {
    const report = commandTestReport();
    report.datasets[0].enabled = false;
    expect(
      commandAvailable(
        report,
        { kind: "sync", datasets: ["aero-vault.usage"] },
        testAccountID,
      ),
    ).toBe(false);
    expect(
      commandAvailable(
        report,
        { kind: "export", datasets: ["aero-vault.usage"] },
        testAccountID,
      ),
    ).toBe(true);
    // Per-source erase is checked by server preview: summary=false is not an erase flag.
    expect(
      commandAvailable(
        report,
        { kind: "erase", datasets: ["aero-vault.usage"] },
        testAccountID,
      ),
    ).toBe(true);
  });
  it("validates persisted operation scope/home/tenant before Run or Reconcile", () => {
    expect(operationReceiptPlan(receipt())).toBeDefined();
    expect(
      operationActionAvailable(
        receipt(),
        testOtherAccountID,
        commandTestReport(),
        testScope,
      ),
    ).toBe(true);
    for (const patch of [
      { tenant_id: "other" },
      { home_region: "foreign" },
      { steps: [] },
      { operation_id: testAccountID },
      {
        steps: [
          {
            ...receipt().steps[0],
            dataset_scope: { status: "explicit", datasets: [] },
          },
        ],
      },
    ]) {
      expect(
        operationActionAvailable(
          { ...receipt(), ...patch },
          testOtherAccountID,
          commandTestReport(),
          testScope,
        ),
      ).toBe(false);
    }
  });
  it("gates audit reconciliation with read+export; downloads require global export even for owners", () => {
    const report = commandTestReport();
    const job = {
      job_id: testOtherAccountID,
      account_id: testAccountID,
      actor_uid: "initiator",
      tenant_id: "tenant-a",
      job_type: "audit_export",
      request: { datasets: ["aero-vault.usage"] },
    };
    expect(
      jobReconcileAvailable(job, testOtherAccountID, report, testScope),
    ).toBe(true);
    report.capabilities.find((item) => item.name === "audit.read")!.available =
      false;
    expect(
      jobReconcileAvailable(job, testOtherAccountID, report, testScope),
    ).toBe(false);
    report.capabilities.find(
      (item) => item.name === "account.export",
    )!.available = false;
    expect(
      jobDownloadAvailable(
        { ...job, job_type: "export" },
        testOtherAccountID,
        report,
        testScope,
      ),
    ).toBe(false);
    expect(
      jobDownloadAvailable(
        { ...job, tenant_id: "other" },
        testOtherAccountID,
        report,
        testScope,
      ),
    ).toBe(false);
  });
});
