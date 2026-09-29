import { afterEach, describe, expect, it } from "vitest";
import { loadEraseIntent, saveEraseIntent } from "./eraseIntentStorage";

afterEach(() => sessionStorage.clear());

describe("account erase intent storage", () => {
  it("stores only the key and job ID for the matching account", () => {
    saveEraseIntent(sessionStorage, "account-1", {
      idempotencyKey: "123e4567-e89b-12d3-a456-426614174000",
    });
    expect(loadEraseIntent(sessionStorage, "account-1")).toEqual({
      idempotencyKey: "123e4567-e89b-12d3-a456-426614174000",
    });
    expect(loadEraseIntent(sessionStorage, "account-2")).toEqual({});

    saveEraseIntent(sessionStorage, "account-1", { jobID: "job-1" });
    expect(loadEraseIntent(sessionStorage, "account-1")).toEqual({
      jobID: "job-1",
    });
  });

  it("ignores malformed or oversized stored values", () => {
    sessionStorage.setItem(
      "aero-id:erase-intent:account-1",
      JSON.stringify({ idempotencyKey: "not-a-uuid", jobID: "x".repeat(257) }),
    );

    expect(loadEraseIntent(sessionStorage, "account-1")).toEqual({});
  });

  it("removes settled state and tolerates a missing account scope", () => {
    saveEraseIntent(sessionStorage, "account-1", {
      idempotencyKey: "123e4567-e89b-12d3-a456-426614174000",
    });
    saveEraseIntent(sessionStorage, "account-1", {});
    saveEraseIntent(sessionStorage, undefined, { jobID: "job-1" });

    expect(loadEraseIntent(sessionStorage, "account-1")).toEqual({});
    expect(sessionStorage.length).toBe(0);
  });
});
