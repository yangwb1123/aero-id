import { describe, expect, it } from "vitest";
import type { JsonRecord } from "../types";
import { matchesOriginalTask } from "./taskActionEligibility";
import { testAccountID, testReceiptID } from "./commandTestFixtures";

const cases = [
  {
    field: "job_id",
    other: "operation_id",
    type: { job_type: "export" },
    statuses: [
      "pending",
      "running",
      "completed",
      "partially_completed",
      "failed",
      "unknown",
    ],
    otherStatuses: ["requested", "waiting", "compensating"],
  },
  {
    field: "operation_id",
    other: "job_id",
    type: { operation_type: "sync" },
    statuses: [
      "requested",
      "running",
      "waiting",
      "unknown",
      "partially_completed",
      "completed",
      "failed",
      "compensating",
    ],
    otherStatuses: ["pending"],
  },
] as const;
describe.each(cases)(
  "$field action receipt correlation",
  ({ field, other, type, statuses, otherStatuses }) => {
    function original(): JsonRecord {
      return {
        [field]: testReceiptID,
        ...type,
        account_id: testAccountID,
        tenant_id: "tenant-a",
        actor_uid: "original-initiator",
        status: "unknown",
      };
    }
    it("uses the exact canonical root ID, not an unrelated alias", () => {
      const value = original();
      expect(
        matchesOriginalTask(
          { ...value, status: "completed", id: "unrelated-alias" },
          value,
          testReceiptID,
          field,
        ),
      ).toBe(true);
    });
    it.each(["id", "other family", "wrong ID", "missing"])(
      "rejects an original task identified only by %s",
      (shape) => {
        const value = original();
        const result = { ...value, status: "completed" };
        delete value[field];
        if (shape === "id") value.id = testReceiptID;
        if (shape === "other family") value[other] = testReceiptID;
        if (shape === "wrong ID") value[field] = testAccountID;
        expect(matchesOriginalTask(result, value, testReceiptID, field)).toBe(
          false,
        );
      },
    );
    it.each(["account_id", "tenant_id", "actor_uid", Object.keys(type)[0]])(
      "still rejects a changed %s even when the canonical ID matches",
      (key) => {
        const value = original();
        expect(
          matchesOriginalTask(
            { ...value, [key]: "other" },
            value,
            testReceiptID,
            field,
          ),
        ).toBe(false);
      },
    );
    it.each(statuses)("accepts the exact family status %s", (status) => {
      const value = original();
      expect(
        matchesOriginalTask({ ...value, status }, value, testReceiptID, field),
      ).toBe(true);
    });
    it.each(statuses)("does not coerce an array containing %s", (status) => {
      const value = original();
      expect(
        matchesOriginalTask(
          { ...value, status: [status] },
          value,
          testReceiptID,
          field,
        ),
      ).toBe(false);
    });
    it.each(otherStatuses)("rejects the other family's %s status", (status) => {
      const value = original();
      expect(
        matchesOriginalTask({ ...value, status }, value, testReceiptID, field),
      ).toBe(false);
    });
    it.each([
      { label: "missing", status: undefined },
      { label: "null", status: null },
      { label: "number", status: 1 },
      { label: "boolean", status: true },
      { label: "empty array", status: [] },
      { label: "nested array", status: [["completed"]] },
      { label: "object", status: { status: "completed" } },
      { label: "empty string", status: "" },
      { label: "padded string", status: " completed " },
      { label: "uppercase string", status: "COMPLETED" },
      { label: "future status", status: "future-status" },
      { label: "unsupported cancellation", status: "canceled" },
    ])("rejects $label without a fallback to nested metadata", ({ status }) => {
      const value = original();
      const result = {
        ...value,
        status,
        result: { status: "completed" },
        steps: [{ status: "completed" }],
      };
      const snapshot = JSON.stringify({ value, result });
      expect(matchesOriginalTask(result, value, testReceiptID, field)).toBe(
        false,
      );
      expect(JSON.stringify({ value, result })).toBe(snapshot);
    });
    it("does not infer the requested family or an empty ID from an otherwise matching receipt", () => {
      const value = original();
      expect(matchesOriginalTask(value, undefined, testReceiptID, field)).toBe(
        false,
      );
      expect(matchesOriginalTask(value, value, "", field)).toBe(false);
      expect(matchesOriginalTask(value, value, testReceiptID, other)).toBe(
        false,
      );
    });
  },
);
