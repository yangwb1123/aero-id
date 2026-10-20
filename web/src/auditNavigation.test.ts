import { afterEach, describe, expect, it } from "vitest";
import { parseAuditNavigation, taskAuditHref } from "./auditNavigation";
import { routeDetails, routeFromHash } from "./router";
const id = "01800000-0000-7000-8000-000000000003";
afterEach(() => window.history.replaceState(null, "", "/"));
it("round-trips an opaque original Request ID without splitting or double-decoding query data", () => {
  const value = "request:original/part?x=1&marker=%2F+中文";
  const href = taskAuditHref("jobs", id, "request", value)!;
  window.history.replaceState(null, "", href);
  const route = routeFromHash("overview");
  expect(routeDetails(route)).toEqual({
    page: "audit",
    auditNavigation: { taskPage: "jobs", taskID: id, filter: "request", value },
  });
  expect(href).not.toContain(value);
});
it("keeps Operation and Request filters separate and binds only an allowed original task return", () => {
  expect(
    parseAuditNavigation(
      taskAuditHref("operations", id, "operation", id)!.slice(2),
    ),
  ).toEqual({
    taskPage: "operations",
    taskID: id,
    filter: "operation",
    value: id,
  });
  expect(taskAuditHref("jobs", id, "operation", id)).toBeUndefined();
  expect(
    taskAuditHref(
      "operations",
      id,
      "operation",
      "01800000-0000-7000-8000-000000000004",
    ),
  ).toBeUndefined();
});
describe("untrusted audit navigation is not an arbitrary query or return URL", () => {
  it.each([
    `audit?operation_id=${id}&request_id=request&from=operations&task_id=${id}`,
    `audit?request_id=one&request_id=two&from=jobs&task_id=${id}`,
    `audit?request_id=one&from=https%3A%2F%2Foutside.example&task_id=${id}`,
    `audit?request_id=one&from=jobs&task_id=not-a-uuid`,
    `audit?request_id=one&from=jobs&task_id=00000000-0000-0000-0000-000000000000`,
    `audit?request_id=one&from=jobs&task_id=${id}&tenant_id=another-tenant`,
    `audit?job_id=${id}&from=jobs&task_id=${id}`,
    `audit?request_id=&from=jobs&task_id=${id}`,
    `audit?request_id=%0Aunsafe&from=jobs&task_id=${id}`,
  ])("rejects %s instead of issuing an unfiltered audit query", (route) => {
    expect(parseAuditNavigation(route)).toBeUndefined();
    expect(routeDetails(route)).toEqual({ page: "overview" });
  });
});
it("does not manufacture a Request ID from missing, non-string or over-budget metadata", () => {
  for (const value of [undefined, null, 42, [], {}, "", " ", "x".repeat(513)])
    expect(taskAuditHref("jobs", id, "request", value)).toBeUndefined();
  expect(
    taskAuditHref("jobs", "not-a-uuid", "request", "original"),
  ).toBeUndefined();
});
