import { defineComponent } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AeroIdClient } from "../api/aeroId";
import type { CommandIntent, CommandPlan } from "../api/commandIntent";
import { loadCommandPreviews } from "../components/commandPreviewStorage";
import { saveJobIntents } from "../components/jobIntentStorage";
import {
  abandonedTestIntent,
  commandTestClient,
  commandTestIntent,
  commandTestReport,
  executedTestIntent,
  mountCommandTest,
  testAccountID,
  testOtherAccountID,
  testReceiptID,
  testScope,
  unmountCommandTests,
} from "../components/commandTestFixtures";
import { useCommandPreview } from "./useCommandPreview";

const plan: CommandPlan = { kind: "export", datasets: ["aero-vault.usage"] };
beforeEach(() => sessionStorage.clear());
afterEach(unmountCommandTests);
function mount(options: { provide?: boolean; channel?: string } = {}) {
  const client = commandTestClient();
  const receipt = vi.fn();
  let hook!: ReturnType<typeof useCommandPreview>;
  const mounted = mountCommandTest(
    defineComponent({
      setup() {
        hook = useCommandPreview({
          client: () => client as unknown as AeroIdClient,
          channel: options.channel ?? "account-jobs",
          accountID: () => testAccountID,
          onReceipt: receipt,
        });
        return () => null;
      },
    }),
    {},
    { provide: options.provide },
  );
  return { ...mounted, hook, client, receipt };
}
function originalPrepare(client: ReturnType<typeof commandTestClient>) {
  const original = commandTestIntent(plan);
  client.commands.prepare.mockResolvedValue(original);
  client.commands.get.mockResolvedValue(original);
  return original;
}

describe("scoped command preview recovery", () => {
  it("recovers an unknown preparation by original-key GET without another POST", async () => {
    const item = mount();
    item.client.commands.prepare.mockRejectedValueOnce(
      new Error("lost response"),
    );
    await item.hook.prepare(plan);
    const key = item.hook.pending.value!.idempotencyKey;
    const original = commandTestIntent(plan);
    item.client.commands.findByKey.mockResolvedValue(original);
    await item.hook.recover();
    expect(item.client.commands.findByKey).toHaveBeenCalledWith(
      key,
      expect.any(AbortSignal),
    );
    expect(item.hook.pending.value?.intentID).toBe(original.intent_id);
    expect(item.client.commands.prepare).toHaveBeenCalledOnce();
  });
  it("NotFound lookup retains an unknown original key and blocks changed scope", async () => {
    const item = mount();
    item.client.commands.prepare.mockRejectedValueOnce(
      new Error("lost response"),
    );
    await item.hook.prepare(plan);
    const key = item.hook.pending.value!.idempotencyKey;
    item.client.commands.findByKey.mockRejectedValue(
      new Error("command.not_found"),
    );
    await item.hook.recover();
    await item.hook.prepare({ ...plan, datasets: ["aero-im.workspaces"] });
    expect(item.hook.pending.value?.idempotencyKey).toBe(key);
    expect(item.client.commands.prepare).toHaveBeenCalledOnce();
  });
  it("retains closure pointers and only prepares a new key after an explicit click and live proof", async () => {
    const item = mount();
    const original = originalPrepare(item.client);
    await item.hook.prepare(plan);
    const key = item.hook.pending.value!.idempotencyKey;
    const closed = abandonedTestIntent(original);
    await item.hook.confirmed(closed);
    expect(item.hook.pending.value).toBeUndefined();
    expect(item.hook.abandoned.value[0].idempotencyKey).toBe(key);
    expect(item.receipt).not.toHaveBeenCalled();
    expect(item.client.commands.prepare).toHaveBeenCalledOnce();
    item.client.commands.get.mockResolvedValue(closed);
    const next = { ...commandTestIntent(plan), intent_id: testOtherAccountID };
    item.client.commands.prepare.mockResolvedValue(next);
    await item.hook.prepare(plan);
    expect(item.client.commands.get).toHaveBeenCalledWith(
      original.intent_id,
      expect.any(AbortSignal),
    );
    expect(item.client.commands.prepare.mock.calls[1][1]).not.toBe(key);
    expect(loadCommandPreviews(sessionStorage, testScope)).toHaveLength(2);
    expect(item.hook.abandoned.value).toHaveLength(1);
  });
  it("does not trust a locally forged closed enum or a missing closure before a new command", async () => {
    const item = mount();
    const original = originalPrepare(item.client);
    await item.hook.prepare(plan);
    const storageKey = `aero-id:command-previews:v2:${encodeURIComponent(testScope)}`;
    const pointers = loadCommandPreviews(sessionStorage, testScope);
    sessionStorage.setItem(
      storageKey,
      JSON.stringify(
        pointers.map((pointer) => ({ ...pointer, abandoned: true })),
      ),
    );
    item.app.unmount();
    const restored = mount();
    restored.client.commands.get.mockResolvedValue(original);
    await restored.hook.prepare(plan);
    expect(restored.client.commands.prepare).not.toHaveBeenCalled();
    expect(restored.hook.error.value?.message).toContain("关闭证明");
    restored.client.commands.get.mockRejectedValue(new Error("not found"));
    await restored.hook.prepare(plan);
    expect(restored.client.commands.prepare).not.toHaveBeenCalled();
    expect(
      loadCommandPreviews(sessionStorage, testScope)[0].idempotencyKey,
    ).toBe(pointers[0].idempotencyKey);
  });
  it("does not let a late prepared GET reopen a stored authoritative closure", async () => {
    const first = mount();
    const original = originalPrepare(first.client);
    await first.hook.prepare(plan);
    const second = mount();
    let finish!: (intent: CommandIntent) => void;
    second.client.commands.get.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    const pendingRead = second.hook.recover();
    await first.hook.confirmed(abandonedTestIntent(original));
    finish(original);
    await pendingRead;
    expect(loadCommandPreviews(sessionStorage, testScope)[0].abandoned).toBe(
      true,
    );
    expect(second.hook.error.value?.message).toContain("迟到回应");
    expect(second.client.commands.prepare).not.toHaveBeenCalled();
  });
  it("cannot turn an executed erase receipt into abandonment or clear its pointer", async () => {
    const item = mount();
    const erase = { kind: "erase" as const, datasets: ["aero-vault.usage"] };
    const original = commandTestIntent(erase);
    item.client.commands.prepare.mockResolvedValue(original);
    await item.hook.prepare(erase);
    await item.hook.confirmed(executedTestIntent(original));
    await item.hook.confirmed(abandonedTestIntent(original));
    expect(item.hook.pending.value?.receiptID).toBe(testReceiptID);
    expect(item.hook.abandoned.value).toHaveLength(0);
  });
  it("fails closed without capabilities or verified browser scope", async () => {
    const item = mount({ provide: false });
    await item.hook.prepare(plan);
    expect(item.client.commands.prepare).not.toHaveBeenCalled();
    const scoped = mount();
    scoped.scope.value = undefined;
    await scoped.hook.prepare(plan);
    expect(scoped.client.commands.prepare).not.toHaveBeenCalled();
  });
  it("rechecks current capability after async plan hashing, before the first POST", async () => {
    const item = mount();
    let finish!: (value: ArrayBuffer) => void;
    const digest = vi.spyOn(crypto.subtle, "digest").mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    const request = item.hook.prepare(plan);
    item.report.value!.capabilities.find(
      (feature) => feature.name === "account.export",
    )!.available = false;
    finish(new ArrayBuffer(32));
    await request;
    digest.mockRestore();
    expect(item.client.commands.prepare).not.toHaveBeenCalled();
    expect(item.hook.error.value?.message).toContain("能力已变化");
  });
  it("persists before prepare, retains the exact key after timeout, and rejects changed scope", async () => {
    const item = mount();
    item.client.commands.prepare.mockImplementation(async () => {
      expect(loadCommandPreviews(sessionStorage, testScope)).toHaveLength(1);
      throw new Error("lost response");
    });
    await item.hook.prepare(plan);
    await item.hook.prepare({ ...plan, datasets: ["aero-im.workspaces"] });
    expect(item.client.commands.prepare).toHaveBeenCalledTimes(1);
    expect(item.hook.error.value?.message).toContain("不能更改范围");
    await item.hook.prepare(plan);
    expect(item.client.commands.prepare.mock.calls[1]).toEqual(
      item.client.commands.prepare.mock.calls[0],
    );
    expect(item.client.commands.execute).not.toHaveBeenCalled();
  });
  it("restores a known ID with GET only after remount; expiry never replaces the original key", async () => {
    const item = mount();
    const original = originalPrepare(item.client);
    original.expires_at = new Date(Date.now() - 1).toISOString();
    original.created_at = new Date(Date.now() - 600000).toISOString();
    await item.hook.prepare(plan);
    const key = item.hook.pending.value?.idempotencyKey;
    item.app.unmount();
    const restored = mount();
    restored.client.commands.get.mockResolvedValue(original);
    await restored.hook.recover();
    expect(restored.client.commands.get).toHaveBeenCalledWith(
      original.intent_id,
      expect.any(AbortSignal),
    );
    expect(restored.client.commands.prepare).not.toHaveBeenCalled();
    expect(restored.hook.preview.value?.expires_at).toBe(original.expires_at);
    expect(restored.hook.pending.value?.idempotencyKey).toBe(key);
  });
  it("recognizes only the original verified receipt and never auto-runs", async () => {
    const item = mount();
    const original = originalPrepare(item.client);
    await item.hook.prepare(plan);
    await item.hook.confirmed({
      ...executedTestIntent(original),
      account_id: testOtherAccountID,
    });
    expect(item.receipt).not.toHaveBeenCalled();
    expect(item.hook.pending.value).toBeDefined();
    await item.hook.confirmed(executedTestIntent(original));
    expect(item.receipt).toHaveBeenCalledWith(executedTestIntent(original));
    expect(item.hook.pending.value).toBeUndefined();
    expect(item.client.runOperation).not.toHaveBeenCalled();
  });
  it("does not let an old view's late receipt clear another intent's recovery key", async () => {
    const first = mount();
    const original = originalPrepare(first.client);
    await first.hook.prepare(plan);
    const second = mount();
    await first.hook.confirmed(executedTestIntent(original));
    const nextPlan: CommandPlan = {
      kind: "export",
      datasets: ["aero-im.workspaces"],
    };
    first.client.commands.prepare.mockResolvedValue({
      ...commandTestIntent(nextPlan),
      intent_id: testOtherAccountID,
    });
    await first.hook.prepare(nextPlan);
    const next = first.hook.pending.value;
    await second.hook.confirmed(executedTestIntent(original));
    expect(
      loadCommandPreviews(sessionStorage, testScope)[0].idempotencyKey,
    ).toBe(next?.idempotencyKey);
    expect(second.receipt).not.toHaveBeenCalled();
    expect(second.hook.error.value?.message).toContain("不会替换或清除其键");
  });
  it("keeps erased receipts to prevent a second erase across refresh", async () => {
    const item = mount();
    const erase: CommandPlan = { kind: "erase", datasets: plan.datasets };
    const original = commandTestIntent(erase);
    item.client.commands.prepare.mockResolvedValue(original);
    await item.hook.prepare(erase);
    await item.hook.confirmed(executedTestIntent(original));
    expect(item.hook.pending.value?.receiptID).toBe(testReceiptID);
    item.app.unmount();
    const restored = mount();
    restored.client.commands.get.mockResolvedValue(
      executedTestIntent(original),
    );
    await restored.hook.recover();
    expect(restored.receipt).toHaveBeenCalledOnce();
    expect(restored.client.commands.prepare).not.toHaveBeenCalled();
  });
  it("rejects a different receipt or prepared regression after an erase receipt was already confirmed", async () => {
    const item = mount();
    const erase: CommandPlan = { kind: "erase", datasets: plan.datasets };
    const original = commandTestIntent(erase);
    item.client.commands.prepare.mockResolvedValue(original);
    await item.hook.prepare(erase);
    await item.hook.confirmed(executedTestIntent(original));
    item.client.commands.get.mockResolvedValue({
      ...executedTestIntent(original),
      receipt_id: testOtherAccountID,
    });
    await item.hook.recover();
    expect(item.receipt).toHaveBeenCalledOnce();
    expect(item.hook.pending.value?.receiptID).toBe(testReceiptID);
    expect(item.hook.error.value?.message).toContain("原身份/目标/意图不一致");
    item.client.commands.get.mockResolvedValue(original);
    await item.hook.recover();
    expect(item.receipt).toHaveBeenCalledOnce();
  });
  it("blocks old direct keys and migrates only the exactly matched existing server-preview key", async () => {
    saveJobIntents(sessionStorage, testScope, {
      old: {
        signature: JSON.stringify({
          jobType: "export",
          datasets: plan.datasets,
        }),
        idempotencyKey: testOtherAccountID,
      },
    });
    const item = mount();
    await item.hook.prepare(plan);
    expect(item.client.commands.prepare).not.toHaveBeenCalled();
    expect(item.hook.error.value?.message).toContain("旧接口");
    saveJobIntents(sessionStorage, testScope, {});
    saveJobIntents(sessionStorage, `${testScope}:server-preview-v1`, {
      old: {
        signature: JSON.stringify({
          jobType: "export",
          datasets: plan.datasets,
        }),
        idempotencyKey: testOtherAccountID,
      },
    });
    const original = originalPrepare(item.client);
    await item.hook.prepare({ ...plan, datasets: ["aero-im.workspaces"] });
    expect(item.client.commands.prepare).not.toHaveBeenCalled();
    await item.hook.prepare(plan);
    expect(item.client.commands.prepare).toHaveBeenCalledWith(
      plan,
      testOtherAccountID,
      testAccountID,
    );
    await item.hook.confirmed(executedTestIntent(original));
    expect(
      sessionStorage.getItem(
        `aero-id:job-intents:v1:${encodeURIComponent(`${testScope}:server-preview-v1`)}`,
      ),
    ).toBeNull();
  });
  it("stores only bounded typed pointers/hashes, not payloads, tokens or confirmation text", async () => {
    const item = mount({ channel: "operation" });
    const operation: CommandPlan = {
      kind: "operation",
      datasets: ["aero-im.workspaces"],
      operation_type: "sync",
      request_payload: { note: "private-content-marker" },
      steps: [
        {
          step_key: "summary",
          source: "aero-im",
          action: "summary",
          request_payload: {
            datasets: ["aero-im.workspaces"],
            note: "private-step-marker",
          },
        },
      ],
    };
    const original = commandTestIntent(operation);
    item.client.commands.prepare.mockResolvedValue(original);
    await item.hook.prepare(operation);
    const raw = [...Array(sessionStorage.length)]
      .map((_, i) => sessionStorage.getItem(sessionStorage.key(i)!))
      .join("");
    expect(raw).not.toMatch(
      /private-content|private-step|request_payload|confirmation_digest|access_token/,
    );
    expect(loadCommandPreviews(sessionStorage, testScope)[0].intentID).toBe(
      original.intent_id,
    );
  });
  it("rejects mutated lifetime/plan under the same original ID, including after refresh", async () => {
    const item = mount();
    const original = originalPrepare(item.client);
    await item.hook.prepare(plan);
    item.app.unmount();
    const restored = mount();
    restored.client.commands.get.mockResolvedValue({
      ...original,
      expires_at: new Date(Date.now() + 600000).toISOString(),
    });
    await restored.hook.recover();
    expect(restored.hook.preview.value).toBeUndefined();
    expect(restored.hook.pending.value?.intentID).toBe(original.intent_id);
    expect(restored.hook.error.value?.message).toContain("有效期发生变化");
  });
  it("does not emit stale write results or erase old-scope recovery keys on identity change", async () => {
    const item = mount();
    let finish!: (intent: CommandIntent) => void;
    item.client.commands.prepare.mockImplementation(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    const request = item.hook.prepare(plan);
    await vi.waitFor(() =>
      expect(item.client.commands.prepare).toHaveBeenCalledOnce(),
    );
    item.scope.value = JSON.stringify([
      "https://other.example/v1",
      "issuer-b",
      "client-b",
      "tenant-b",
      testOtherAccountID,
    ]);
    finish(executedTestIntent(commandTestIntent(plan)));
    await request;
    expect(item.receipt).not.toHaveBeenCalled();
    expect(item.hook.preview.value).toBeUndefined();
    expect(loadCommandPreviews(sessionStorage, testScope)).toHaveLength(1);
  });
  it("aborts original GET on unmount; changing a report never auto-posts", async () => {
    const item = mount();
    originalPrepare(item.client);
    await item.hook.prepare(plan);
    let finish!: (intent: CommandIntent) => void;
    item.client.commands.get.mockImplementation((_id, signal) => {
      expect(signal?.aborted).toBe(false);
      return new Promise((resolve) => {
        finish = resolve;
      });
    });
    const read = item.hook.recover();
    item.app.unmount();
    expect(item.client.commands.get.mock.calls[0][1]?.aborted).toBe(true);
    finish(executedTestIntent(commandTestIntent(plan)));
    await read;
    item.report.value = commandTestReport();
    expect(item.receipt).not.toHaveBeenCalled();
    expect(item.client.commands.prepare).toHaveBeenCalledOnce();
  });
  it("does not treat corrupt durable browser state as absence and issue a new key", async () => {
    sessionStorage.setItem(
      `aero-id:command-previews:v2:${encodeURIComponent(testScope)}`,
      "not-json",
    );
    const item = mount();
    await item.hook.prepare(plan);
    expect(item.client.commands.prepare).not.toHaveBeenCalled();
    expect(item.hook.error.value).toBeDefined();
  });
});
