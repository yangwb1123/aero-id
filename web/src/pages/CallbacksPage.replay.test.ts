import { ref } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import CallbacksPage from "./CallbacksPage.vue";
import type { CommandIntent } from "../api/commandIntent";
import {
  callbackTestClient,
  callbackTestDelivery,
  callbackTestReport,
} from "../components/callbackTestFixtures";
import { callbackReplayPlan } from "../components/callbackReplayPlan";
import { loadCommandPreviews } from "../components/commandPreviewStorage";
import {
  abandonedTestIntent,
  commandButton,
  commandTestIntent,
  executedTestIntent,
  flushCommandUI,
  mountCommandTest,
  testAccountID,
  testIntentID,
  testOtherAccountID,
  testScope,
  unmountCommandTests,
} from "../components/commandTestFixtures";
vi.mock("@iris-ui-kit/vue", async () =>
  (await import("../components/commandTestFixtures")).irisCommandTestStubs(),
);
beforeEach(() => sessionStorage.clear());
afterEach(() => {
  unmountCommandTests();
  vi.restoreAllMocks();
});
function field(root: HTMLElement, index: number, value: string) {
  const input = root.querySelectorAll<HTMLInputElement>(
    'input[autocomplete="off"]',
  )[index];
  input.value = value;
  input.dispatchEvent(new Event("input"));
}
async function prepare(root: HTMLElement) {
  commandButton(root, "查看此订阅投递").click();
  await flushCommandUI();
  commandButton(root, "查看投递详情").click();
  await flushCommandUI();
  commandButton(root, "确认此失败投递的重放意图").click();
}
async function prepared(root: HTMLElement) {
  await prepare(root);
  await vi.waitFor(() =>
    expect(root.querySelectorAll('input[autocomplete="off"]')).toHaveLength(3),
  );
}
async function confirm(root: HTMLElement) {
  const delivery = callbackTestDelivery();
  field(root, 0, delivery.id);
  field(root, 1, delivery.event_id);
  await flushCommandUI();
  commandButton(root, "确认原投递 CAS 排队").click();
}
function mount() {
  const fixture = callbackTestClient();
  const mounted = mountCommandTest(
    CallbacksPage,
    { client: fixture.client },
    { report: ref(callbackTestReport()) },
  );
  return { ...fixture, ...mounted };
}
describe("callback page durable preview and recovery", () => {
  it("requires BOTH exact original Delivery/Event IDs, not account ID or a generic confirmation", async () => {
    const item = mount();
    await flushCommandUI();
    await prepared(item.root);
    field(item.root, 0, testAccountID);
    field(item.root, 1, callbackTestDelivery().event_id);
    await flushCommandUI();
    expect(commandButton(item.root, "确认原投递 CAS 排队").disabled).toBe(true);
    field(item.root, 0, callbackTestDelivery().id);
    field(item.root, 1, "确认");
    await flushCommandUI();
    expect(commandButton(item.root, "确认原投递 CAS 排队").disabled).toBe(true);
    expect(item.commands.execute).not.toHaveBeenCalled();
    expect(item.root.textContent).toContain("本地 CAS 版本 / Claim 代数");
    await confirm(item.root);
    await vi.waitFor(() =>
      expect(item.root.textContent).toContain("本地 CAS 已排队"),
    );
    const pointer = loadCommandPreviews(sessionStorage, testScope)[0];
    expect(pointer.receiptID).toBe(testIntentID);
    expect(pointer.acknowledged).toBeUndefined();
    expect(item.callbacks.replayDelivery).not.toHaveBeenCalled();
  });
  it("restores a prepared original with GET only, without metadata lookup or another prepare on refresh", async () => {
    const first = mount();
    await flushCommandUI();
    await prepared(first.root);
    const original = (await first.commands.prepare.mock.results[0].value)!;
    first.app.unmount();
    const restored = mount();
    restored.commands.get.mockResolvedValue(original);
    await flushCommandUI();
    expect(restored.commands.prepare).not.toHaveBeenCalled();
    commandButton(restored.root, "仅查询原重放意图 / 回执").click();
    await vi.waitFor(() =>
      expect(
        restored.root.querySelectorAll('input[autocomplete="off"]'),
      ).toHaveLength(3),
    );
    expect(restored.commands.get).toHaveBeenCalledWith(
      original.intent_id,
      expect.any(AbortSignal),
    );
    expect(restored.commands.prepare).not.toHaveBeenCalled();
    expect(restored.commands.execute).not.toHaveBeenCalled();
    expect(restored.callbacks.getDelivery).not.toHaveBeenCalled();
    expect(restored.callbacks.replayDelivery).not.toHaveBeenCalled();
  });
  it("keeps ambiguous prepare key across refresh; NotFound never enables another tuple or replacement key", async () => {
    const first = mount();
    first.commands.prepare.mockRejectedValueOnce(new Error("unknown prepare"));
    await flushCommandUI();
    await prepare(first.root);
    await vi.waitFor(() =>
      expect(first.root.textContent).toContain("unknown prepare"),
    );
    const pointer = loadCommandPreviews(sessionStorage, testScope)[0];
    first.app.unmount();
    const restored = mount();
    restored.commands.findByKey.mockRejectedValueOnce(
      new Error("command.not_found"),
    );
    await flushCommandUI();
    commandButton(restored.root, "仅查询原重放意图 / 回执").click();
    await flushCommandUI();
    expect(restored.commands.findByKey).toHaveBeenCalledWith(
      pointer.idempotencyKey,
      expect.any(AbortSignal),
    );
    expect(
      loadCommandPreviews(sessionStorage, testScope)[0].idempotencyKey,
    ).toBe(pointer.idempotencyKey);
    expect(restored.commands.prepare).not.toHaveBeenCalled();
    expect(restored.root.textContent).not.toContain("显式重取原参数");
    expect(restored.callbacks.replayDelivery).not.toHaveBeenCalled();
  });
  it("recovers an unknown Execute as an immutable queue receipt even with unavailable subscription reads", async () => {
    const first = mount();
    first.commands.execute.mockRejectedValueOnce(new Error("unknown execute"));
    await flushCommandUI();
    await prepared(first.root);
    await confirm(first.root);
    await flushCommandUI();
    const original = first.commands.execute.mock.calls[0][0];
    const key = loadCommandPreviews(sessionStorage, testScope)[0]
      .idempotencyKey;
    first.app.unmount();
    const restored = mount();
    restored.callbacks.list.mockRejectedValueOnce(
      new Error("parent deleted or reads denied"),
    );
    restored.commands.get.mockResolvedValue(executedTestIntent(original));
    restored.report.value = undefined;
    await flushCommandUI();
    commandButton(restored.root, "仅查询原重放意图 / 回执").click();
    await vi.waitFor(() =>
      expect(restored.root.textContent).toContain("本地 CAS 已排队"),
    );
    expect(loadCommandPreviews(sessionStorage, testScope)[0]).toMatchObject({
      idempotencyKey: key,
      receiptID: original.intent_id,
    });
    expect(restored.commands.execute).not.toHaveBeenCalled();
    expect(restored.commands.prepare).not.toHaveBeenCalled();
    expect(restored.callbacks.replayDelivery).not.toHaveBeenCalled();
  });
  it("requires exact intent ID plus live receipt proof for acknowledgment; no POST or replacement on unknown proof", async () => {
    const item = mount();
    await flushCommandUI();
    await prepared(item.root);
    await confirm(item.root);
    await vi.waitFor(() =>
      expect(item.root.textContent).toContain("本地 CAS 已排队"),
    );
    field(item.root, 0, testOtherAccountID);
    await flushCommandUI();
    expect(
      commandButton(item.root, "核实原排队回执（不发送命令）").disabled,
    ).toBe(true);
    field(item.root, 0, testIntentID);
    await flushCommandUI();
    item.commands.get.mockRejectedValueOnce(
      new Error("unknown original receipt"),
    );
    commandButton(item.root, "核实原排队回执（不发送命令）").click();
    await flushCommandUI();
    expect(
      loadCommandPreviews(sessionStorage, testScope)[0].acknowledged,
    ).toBeUndefined();
    item.commands.get.mockResolvedValue(
      executedTestIntent(item.commands.execute.mock.calls[0][0]),
    );
    commandButton(item.root, "核实原排队回执（不发送命令）").click();
    await vi.waitFor(() =>
      expect(
        loadCommandPreviews(sessionStorage, testScope)[0].acknowledged,
      ).toBe(true),
    );
    expect(item.commands.prepare).toHaveBeenCalledOnce();
    expect(item.commands.execute).toHaveBeenCalledOnce();
    expect(item.root.textContent).toContain("不证明接收端完成");
    expect(item.callbacks.replayDelivery).not.toHaveBeenCalled();
  });
  it("abandon uses original intent version/digest, not deletion/replay, and cannot cancel an execute-won receipt", async () => {
    for (const executeWon of [false, true]) {
      sessionStorage.clear();
      const item = mount();
      await flushCommandUI();
      await prepared(item.root);
      const original = await item.commands.prepare.mock.results[0].value;
      item.report.value = undefined;
      item.commands.abandon.mockResolvedValueOnce(
        executeWon
          ? executedTestIntent(original)
          : abandonedTestIntent(original),
      );
      field(item.root, 2, original.intent_id);
      await flushCommandUI();
      commandButton(item.root, "放弃未执行预览").click();
      await vi.waitFor(() =>
        expect(
          loadCommandPreviews(sessionStorage, testScope)[0][
            executeWon ? "receiptID" : "abandoned"
          ],
        ).toBe(executeWon ? original.intent_id : true),
      );
      expect(item.commands.abandon).toHaveBeenCalledWith(original);
      expect(item.commands.execute).not.toHaveBeenCalled();
      expect(item.callbacks.deleteSubscription).not.toHaveBeenCalled();
      expect(item.callbacks.replayDelivery).not.toHaveBeenCalled();
      item.app.unmount();
    }
  });
  it("rejects stale plan/receipt, disables confirmation and never silently renews after metadata revisions change", async () => {
    const item = mount();
    await flushCommandUI();
    await prepared(item.root);
    item.commands.execute.mockRejectedValueOnce(
      Object.assign(new Error("original tuple stale"), {
        code: "command.stale",
      }),
    );
    await confirm(item.root);
    await flushCommandUI();
    expect(commandButton(item.root, "确认原投递 CAS 排队").disabled).toBe(true);
    const original = item.commands.execute.mock.calls[0][0];
    item.commands.get.mockResolvedValue({
      ...original,
      plan: {
        ...original.plan,
        callback: { ...original.plan.callback!, revision: 99 },
      },
    });
    commandButton(item.root, "仅查询原意图 / 回执").click();
    await flushCommandUI();
    expect(item.root.textContent).toContain("原意图计划/身份/有效期不一致");
    expect(loadCommandPreviews(sessionStorage, testScope)[0].intentID).toBe(
      original.intent_id,
    );
    expect(item.commands.prepare).toHaveBeenCalledOnce();
    expect(item.commands.execute).toHaveBeenCalledOnce();
    expect(item.callbacks.replayDelivery).not.toHaveBeenCalled();
  });
  it("does not adopt late Execute after identity change, nor persist token/plan/confirmation/payload content", async () => {
    const item = mount();
    await flushCommandUI();
    await prepared(item.root);
    let finish!: (value: CommandIntent) => void;
    item.commands.execute.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    await confirm(item.root);
    await flushCommandUI();
    const original = item.commands.execute.mock.calls[0][0];
    item.scope.value = JSON.stringify([
      "https://id.example/v1",
      "another-issuer",
      "another-client",
      "tenant-b",
      testOtherAccountID,
    ]);
    await flushCommandUI();
    finish(executedTestIntent(original));
    await flushCommandUI();
    expect(item.root.textContent).not.toContain("本地 CAS 已排队");
    const raw = sessionStorage.getItem(
      `aero-id:command-previews:v2:${encodeURIComponent(testScope)}`,
    )!;
    expect(raw).not.toMatch(
      /"plan"|event_id|delivery_id|confirmation_digest|access_token|endpoint/,
    );
    // The kind/channel contain callback labels; no tuple/plan fields or input IDs.
    expect(
      loadCommandPreviews(sessionStorage, testScope)[0].receiptID,
    ).toBeUndefined();
    expect(item.commands.execute).toHaveBeenCalledOnce();
    expect(item.callbacks.replayDelivery).not.toHaveBeenCalled();
  });
  it("keeps an expired prepared original pointer but prohibits confirmation and alternate commands", async () => {
    const item = mount();
    const original = commandTestIntent(
      callbackReplayPlan(callbackTestDelivery()),
    );
    original.created_at = new Date(Date.now() - 600000).toISOString();
    original.expires_at = new Date(Date.now() - 1).toISOString();
    item.commands.prepare.mockResolvedValue(original);
    await flushCommandUI();
    await prepared(item.root);
    field(item.root, 0, callbackTestDelivery().id);
    field(item.root, 1, callbackTestDelivery().event_id);
    await flushCommandUI();
    expect(commandButton(item.root, "确认原投递 CAS 排队").disabled).toBe(true);
    expect(item.commands.execute).not.toHaveBeenCalled();
    expect(item.commands.prepare).toHaveBeenCalledOnce();
    expect(loadCommandPreviews(sessionStorage, testScope)[0].intentID).toBe(
      original.intent_id,
    );
  });
});
