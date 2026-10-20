import { defineComponent, ref } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AeroIdClient } from "../api/aeroId";
import type { CommandIntent } from "../api/commandIntent";
import { useCommandPreview } from "./useCommandPreview";
import { callbackReplayPlan } from "../components/callbackReplayPlan";
import {
  callbackTestClient,
  callbackTestDelivery,
  callbackTestReport,
} from "../components/callbackTestFixtures";
import {
  loadCommandPreviews,
  saveCommandPreviews,
} from "../components/commandPreviewStorage";
import {
  abandonedTestIntent,
  commandTestIntent,
  executedTestIntent,
  mountCommandTest,
  testAccountID,
  testOtherAccountID,
  testScope,
  unmountCommandTests,
} from "../components/commandTestFixtures";
const plan = () => callbackReplayPlan(callbackTestDelivery());
beforeEach(() => sessionStorage.clear());
afterEach(() => {
  unmountCommandTests();
  vi.restoreAllMocks();
});
function mount() {
  const fixture = callbackTestClient();
  const receipt = vi.fn();
  let hook!: ReturnType<typeof useCommandPreview>;
  const mounted = mountCommandTest(
    defineComponent({
      setup() {
        hook = useCommandPreview({
          client: () => fixture.client as AeroIdClient,
          channel: "callback-replay",
          accountID: () => testAccountID,
          legacyAccountCommands: false,
          onReceipt: receipt,
        });
        return () => null;
      },
    }),
    {},
    { report: ref(callbackTestReport()) },
  );
  return { ...mounted, ...fixture, hook, receipt };
}
describe("durable callback command pointer recovery", () => {
  it("recovers an unknown prepare by original-key GET, checking exact tuple despite JSON property order", async () => {
    const item = mount();
    item.commands.prepare.mockRejectedValueOnce(new Error("unknown prepare"));
    await item.hook.prepare(plan());
    const pointer = item.hook.pending.value!;
    const expected = commandTestIntent(plan());
    const t = expected.plan.callback!;
    item.commands.findByKey.mockResolvedValue({
      ...expected,
      plan: {
        kind: "callback_replay",
        datasets: [],
        callback: {
          lease_generation: t.lease_generation,
          revision: t.revision,
          event_id: t.event_id,
          delivery_id: t.delivery_id,
          subscription_id: t.subscription_id,
        },
      },
    });
    await item.hook.recover();
    expect(item.hook.preview.value?.plan.callback).toEqual(t);
    expect(item.commands.findByKey).toHaveBeenCalledWith(
      pointer.idempotencyKey,
      expect.any(AbortSignal),
    );
    expect(item.commands.prepare).toHaveBeenCalledOnce();
    expect(item.commands.execute).not.toHaveBeenCalled();
  });
  it("rejects a wrong original-key callback target or revision rather than binding a new tuple", async () => {
    for (const change of [
      { revision: 8 },
      { lease_generation: 9 },
      { event_id: testOtherAccountID },
      { delivery_id: testOtherAccountID },
    ]) {
      sessionStorage.clear();
      const item = mount();
      item.commands.prepare.mockRejectedValueOnce(new Error("unknown prepare"));
      await item.hook.prepare(plan());
      const key = item.hook.pending.value!.idempotencyKey;
      const intent = commandTestIntent(plan());
      item.commands.findByKey.mockResolvedValue({
        ...intent,
        plan: {
          ...intent.plan,
          callback: { ...intent.plan.callback!, ...change },
        },
      });
      await item.hook.recover();
      expect(item.hook.preview.value).toBeUndefined();
      expect(item.hook.pending.value?.idempotencyKey).toBe(key);
      expect(item.hook.pending.value?.intentID).toBeUndefined();
      expect(item.hook.error.value?.message).toContain("原意图签名不一致");
      expect(item.commands.prepare).toHaveBeenCalledOnce();
      item.app.unmount();
    }
  });
  it("retains the original queued receipt through refresh and only acknowledges it with live GET proof", async () => {
    const first = mount();
    const intent = commandTestIntent(plan());
    first.commands.prepare.mockResolvedValue(intent);
    await first.hook.prepare(plan());
    const originalKey = first.hook.pending.value!.idempotencyKey;
    await first.hook.confirmed(executedTestIntent(intent));
    expect(first.hook.pending.value?.receiptID).toBe(intent.intent_id);
    first.app.unmount();
    const restored = mount();
    restored.commands.get.mockResolvedValue(executedTestIntent(intent));
    await restored.hook.recover();
    expect(restored.receipt).toHaveBeenCalledWith(executedTestIntent(intent));
    expect(restored.hook.pending.value?.idempotencyKey).toBe(originalKey);
    expect(restored.commands.prepare).not.toHaveBeenCalled();
    expect(restored.commands.execute).not.toHaveBeenCalled();
    await restored.hook.acknowledgeReceipt();
    expect(restored.commands.get).toHaveBeenCalledTimes(2);
    expect(restored.hook.pending.value).toBeUndefined();
    expect(restored.hook.acknowledged.value[0].receiptID).toBe(
      intent.intent_id,
    );
    const next = {
      ...commandTestIntent(plan()),
      intent_id: testOtherAccountID,
    };
    restored.commands.prepare.mockResolvedValue(next);
    await restored.hook.prepare(plan());
    expect(restored.commands.get).toHaveBeenCalledTimes(3);
    expect(restored.commands.prepare.mock.calls[0][1]).not.toBe(originalKey);
    expect(loadCommandPreviews(sessionStorage, testScope)).toHaveLength(2);
  });
  it("local acknowledgment, NotFound or prepared are not proof for a replacement key", async () => {
    const first = mount();
    const intent = commandTestIntent(plan());
    first.commands.prepare.mockResolvedValue(intent);
    await first.hook.prepare(plan());
    await first.hook.confirmed(executedTestIntent(intent));
    const pointers = loadCommandPreviews(sessionStorage, testScope);
    saveCommandPreviews(sessionStorage, testScope, [
      { ...pointers[0], acknowledged: true },
    ]);
    first.app.unmount();
    const restored = mount();
    restored.commands.get.mockResolvedValue(intent);
    await restored.hook.prepare(plan());
    expect(restored.commands.prepare).not.toHaveBeenCalled();
    restored.commands.get.mockRejectedValueOnce(new Error("command.not_found"));
    await restored.hook.prepare(plan());
    expect(restored.commands.prepare).not.toHaveBeenCalled();
    expect(
      loadCommandPreviews(sessionStorage, testScope)[0].idempotencyKey,
    ).toBe(pointers[0].idempotencyKey);
  });
  it("abandon returns only local closure; execute-won never becomes abandonment or clears its receipt", async () => {
    const item = mount();
    const intent = commandTestIntent(plan());
    item.commands.prepare.mockResolvedValue(intent);
    await item.hook.prepare(plan());
    await item.hook.confirmed(executedTestIntent(intent));
    await item.hook.confirmed(abandonedTestIntent(intent));
    expect(item.hook.pending.value?.receiptID).toBe(intent.intent_id);
    expect(item.hook.abandoned.value).toHaveLength(0);
  });
  it("refuses a late prepared GET to regress a queue receipt acknowledged by another view", async () => {
    const first = mount();
    const original = commandTestIntent(plan());
    first.commands.prepare.mockResolvedValue(original);
    await first.hook.prepare(plan());
    const second = mount();
    let finish!: (value: CommandIntent) => void;
    second.commands.get.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    const read = second.hook.recover();
    await first.hook.confirmed(executedTestIntent(original));
    first.commands.get.mockResolvedValue(executedTestIntent(original));
    await first.hook.acknowledgeReceipt();
    finish(original);
    await read;
    expect(loadCommandPreviews(sessionStorage, testScope)[0].acknowledged).toBe(
      true,
    );
    expect(second.hook.preview.value).toBeUndefined();
    expect(second.receipt).not.toHaveBeenCalled();
    expect(second.hook.error.value?.message).toContain("迟到回应");
  });
  it("caps original receipt proof at one total 15-second GET budget and retains unknown pointers", async () => {
    const item = mount();
    const original = commandTestIntent(plan());
    item.commands.prepare.mockResolvedValue(original);
    await item.hook.prepare(plan());
    await item.hook.confirmed(executedTestIntent(original));
    let finish!: (value: CommandIntent) => void;
    item.commands.get.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    vi.useFakeTimers();
    try {
      const checking = item.hook.acknowledgeReceipt();
      await vi.advanceTimersByTimeAsync(15001);
      expect(item.commands.get.mock.calls[0][1]?.aborted).toBe(true);
      finish(executedTestIntent(original));
      await checking;
      expect(item.hook.pending.value?.receiptID).toBe(original.intent_id);
      expect(item.hook.acknowledged.value).toHaveLength(0);
      expect(item.commands.prepare).toHaveBeenCalledOnce();
    } finally {
      vi.useRealTimers();
    }
  });
  it("rechecks guidance after closure GET before a replacement-key POST", async () => {
    const item = mount();
    const original = commandTestIntent(plan());
    item.commands.prepare.mockResolvedValue(original);
    await item.hook.prepare(plan());
    await item.hook.confirmed(abandonedTestIntent(original));
    item.commands.get.mockImplementationOnce(async () => {
      item.report.value!.capabilities.find(
        (cap) => cap.name === "callback.manage",
      )!.available = false;
      return abandonedTestIntent(original);
    });
    await item.hook.prepare(plan());
    expect(item.commands.prepare).toHaveBeenCalledOnce();
    expect(item.hook.error.value?.message).toContain("能力已变化");
  });
  it("does not borrow Sync legacy keys or write permissions after hashing or identity changes", async () => {
    sessionStorage.setItem(
      `aero-id:job-intents:v1:${encodeURIComponent(testScope)}`,
      "corrupt unrelated sync keys",
    );
    const item = mount();
    let finish!: (value: ArrayBuffer) => void;
    vi.spyOn(crypto.subtle, "digest").mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    const request = item.hook.prepare(plan());
    item.report.value!.capabilities.find(
      (cap) => cap.name === "callback.manage",
    )!.available = false;
    finish(new ArrayBuffer(32));
    await request;
    expect(item.commands.prepare).not.toHaveBeenCalled();
    expect(item.hook.error.value?.message).toContain("能力已变化");
    expect(item.callbacks.replayDelivery).not.toHaveBeenCalled();
  });
});
