import { beforeEach, describe, expect, it } from "vitest";
import {
  commandMetadataSignature,
  commandPlanSignature,
  loadCommandPreviews,
  saveCommandPreviews,
  type StoredCommandPreview,
} from "./commandPreviewStorage";
import {
  commandTestIntent,
  executedTestIntent,
  testAccountID,
  testOtherAccountID,
  testScope,
} from "./commandTestFixtures";

const item = (): StoredCommandPreview => ({
  channel: "account-jobs",
  accountID: testAccountID,
  kind: "export",
  signature: `v1:${"a".repeat(64)}`,
  idempotencyKey: testOtherAccountID,
});
beforeEach(() => sessionStorage.clear());
describe("metadata-only preview pointers", () => {
  it("isolates deployment/principal scopes and does not restore another client", () => {
    saveCommandPreviews(sessionStorage, testScope, [item()]);
    expect(loadCommandPreviews(sessionStorage, testScope)).toEqual([item()]);
    expect(loadCommandPreviews(sessionStorage, "different-client")).toEqual([]);
    expect(loadCommandPreviews(sessionStorage, undefined)).toEqual([]);
  });
  it("rejects corrupt, payload-bearing, unbounded or unverified pointers instead of treating them as absent", () => {
    const key = `aero-id:command-previews:v2:${encodeURIComponent(testScope)}`;
    for (const raw of [
      "bad-json",
      JSON.stringify([{ ...item(), payload: "secret" }]),
      JSON.stringify([{ ...item(), acknowledged: true }]),
      JSON.stringify([
        { ...item(), accountID: "01800000-0000-7000-8000-0000000000AB" },
      ]),
      JSON.stringify([item(), item()]),
      JSON.stringify([{ ...item(), intentID: testAccountID }]),
      JSON.stringify(Array.from({ length: 17 }, item)),
      " ".repeat(16385),
    ]) {
      sessionStorage.setItem(key, raw);
      expect(() => loadCommandPreviews(sessionStorage, testScope)).toThrow();
    }
  });
  it("permits callback receipt acknowledgment only for a fully bound queued pointer and counts it in the cap", () => {
    const receipt: StoredCommandPreview = {
      ...item(),
      kind: "callback_replay",
      channel: "callback-replay",
      intentID: testAccountID,
      metadataSignature: `v1:${"b".repeat(64)}`,
      receiptID: testAccountID,
      acknowledged: true,
    };
    saveCommandPreviews(sessionStorage, testScope, [
      receipt,
      {
        ...receipt,
        acknowledged: undefined,
        intentID: testOtherAccountID,
        receiptID: testOtherAccountID,
        idempotencyKey: testAccountID,
      },
    ]);
    expect(loadCommandPreviews(sessionStorage, testScope)).toHaveLength(2);
    for (const invalid of [
      { ...receipt, kind: "erase" },
      { ...receipt, receiptID: testOtherAccountID },
      { ...receipt, abandoned: true },
    ])
      expect(() =>
        saveCommandPreviews(sessionStorage, testScope, [
          invalid as StoredCommandPreview,
        ]),
      ).toThrow();
    expect(() =>
      saveCommandPreviews(
        sessionStorage,
        testScope,
        Array.from({ length: 17 }, (_, i) => ({
          ...receipt,
          idempotencyKey: `01800000-0000-7000-8000-${String(i + 1).padStart(12, "0")}`,
        })),
      ),
    ).toThrow();
  });
  it("hashes authoritative lifetime at microsecond precision, not raw formatting or rounded JS milliseconds", async () => {
    const created = {
      ...commandTestIntent(),
      created_at: "2030-01-01T12:00:00.123456789Z",
      expires_at: "2030-01-01T12:05:00.123456789Z",
    };
    const loaded = {
      ...created,
      created_at: "2030-01-01T12:00:00.123456Z",
      expires_at: "2030-01-01T12:05:00.123456Z",
    };
    expect(await commandMetadataSignature(created)).toBe(
      await commandMetadataSignature(loaded),
    );
    expect(
      await commandMetadataSignature({
        ...loaded,
        expires_at: "2030-01-01T12:05:00.123457Z",
      }),
    ).not.toBe(await commandMetadataSignature(created));
  });
  it("hashes the immutable server metadata while permitting only receipt/version transitions", async () => {
    const intent = commandTestIntent();
    expect(await commandMetadataSignature(intent)).toBe(
      await commandMetadataSignature(executedTestIntent(intent)),
    );
    expect(
      await commandMetadataSignature({
        ...intent,
        preconditions: { ...intent.preconditions, writer_epoch: 4 },
      }),
    ).not.toBe(await commandMetadataSignature(intent));
    expect(await commandPlanSignature(testAccountID, intent.plan)).not.toBe(
      await commandPlanSignature(testOtherAccountID, intent.plan),
    );
  });
});
