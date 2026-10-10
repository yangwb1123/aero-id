import { afterEach, describe, expect, it } from "vitest";
import { routeDetails, routeFromHash, taskHref } from "./router";
const id = "019f0123-4567-7000-8000-0123456789ab";
afterEach(() => window.history.replaceState(null, "", "/"));

describe("safe task routes", () => {
  it("keeps legacy hash pages and decodes only valid detail links", () => {
    for (const hash of ["#jobs", "#/jobs"]) {
      window.history.replaceState(null, "", hash);
      expect(routeFromHash("overview")).toBe("jobs");
    }
    window.history.replaceState(null, "", `#/jobs/${id}`);
    expect(routeDetails(routeFromHash("overview"))).toEqual({
      page: "jobs",
      jobID: id,
    });
    expect(taskHref("operations", id)).toBe(`#/operations/${id}`);
    expect(routeDetails(`operations/${id}`)).toEqual({
      page: "operations",
      operationID: id,
    });
  });

  it("does not crash or use arbitrary paths, tokens or malformed IDs as resources", () => {
    window.history.replaceState(null, "", "#/jobs/%not-encoded");
    expect(routeFromHash("overview")).toBe("overview");
    for (const route of [
      "jobs/token",
      `jobs/${id}/extra`,
      `unknown/${id}`,
      `jobs/${id}?token=secret`,
    ]) {
      expect(routeDetails(route)).toEqual({ page: "overview" });
    }
    expect(taskHref("jobs", "https://example.test/token")).toBeUndefined();
  });
});
