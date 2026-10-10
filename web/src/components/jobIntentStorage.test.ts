import { beforeEach, describe, expect, it } from "vitest";
import { loadJobIntents, saveJobIntents } from "./jobIntentStorage";
import { nextJobCreateIntent } from "./jobHelpers";
const id = "019f0123-4567-7000-8000-0123456789ab";
beforeEach(() => window.sessionStorage.clear());

describe("scoped job intent storage", () => {
  it("preserves request keys across reload without sharing identity/client scopes", () => {
    const intent = nextJobCreateIntent(
      { jobType: "export", datasets: ["aero-vault.usage"] },
      undefined,
      () => id,
    );
    saveJobIntents(window.sessionStorage, "account-a:tenant-a:client-a", {
      [intent.signature]: intent,
    });
    expect(
      loadJobIntents(window.sessionStorage, "account-a:tenant-a:client-a")[
        intent.signature
      ],
    ).toEqual(intent);
    expect(
      loadJobIntents(window.sessionStorage, "account-a:tenant-b:client-a"),
    ).toEqual({});
    expect(
      loadJobIntents(window.sessionStorage, "account-a:tenant-a:client-b"),
    ).toEqual({});
    saveJobIntents(window.sessionStorage, "account-a:tenant-a:client-a", {});
    expect(
      loadJobIntents(window.sessionStorage, "account-a:tenant-a:client-a"),
    ).toEqual({});
  });

  it("does not restore arbitrary bodies, credentials, corrupt data or oversized histories", () => {
    const key = "aero-id:job-intents:v1:scope";
    for (const value of [
      "{",
      JSON.stringify(Array(17).fill({})),
      JSON.stringify([
        {
          idempotencyKey: id,
          signature: JSON.stringify({
            jobType: "export",
            datasets: ["aero-vault.usage"],
            token: "secret",
          }),
        },
        {
          idempotencyKey: "not-a-key",
          signature: JSON.stringify({
            jobType: "sync",
            datasets: ["aero-vault.usage"],
          }),
        },
      ]),
    ]) {
      window.sessionStorage.setItem(key, value);
      expect(loadJobIntents(window.sessionStorage, "scope")).toEqual({});
    }
    expect(loadJobIntents(window.sessionStorage, undefined)).toEqual({});
  });
});
