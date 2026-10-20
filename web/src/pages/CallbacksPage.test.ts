import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ref } from "vue";
import CallbacksPage from "./CallbacksPage.vue";
import type { CallbackDelivery } from "../api/callbackDelivery";
import type { CapabilityReport } from "../types";
import {
  callbackTestClient,
  callbackTestDelivery,
  callbackTestReport,
  callbackSubscriptionID,
  callbackDeliveryID,
  callbackOtherSubscriptionID,
} from "../components/callbackTestFixtures";
import {
  commandButton,
  flushCommandUI,
  mountCommandTest,
  testScope,
  executedTestIntent,
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

async function selectDelivery(root: HTMLElement) {
  commandButton(root, "查看此订阅投递").click();
  await flushCommandUI();
  commandButton(root, "查看投递详情").click();
  await flushCommandUI();
}
async function showReplay(root: HTMLElement) {
  await selectDelivery(root);
  commandButton(root, "确认此失败投递的重放意图").click();
  await vi.waitFor(() =>
    expect(root.querySelectorAll('input[autocomplete="off"]')).toHaveLength(3),
  );
  await flushCommandUI();
}
function submitRegistration(root: HTMLElement) {
  const input =
    root.querySelector<HTMLInputElement>('input[type="url"]') ??
    root.querySelector<HTMLInputElement>("input")!;
  input.value = "https://callback.example/events";
  input.dispatchEvent(new Event("input"));
  const text = root.querySelector<HTMLTextAreaElement>("textarea")!;
  text.value = "account.provisioned";
  text.dispatchEvent(new Event("input"));
  root
    .querySelector("form")!
    .dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
}

describe("callback delivery governance", () => {
  it("fails closed without current providers rather than using page admission for actions", async () => {
    const { client, callbacks } = callbackTestClient();
    const { root } = mountCommandTest(
      CallbacksPage,
      { client },
      { provide: false },
    );
    await flushCommandUI();
    expect(root.textContent).toContain("当前 Callback 读取能力尚未确认");
    expect(callbacks.list).not.toHaveBeenCalled();
    expect(callbacks.create).not.toHaveBeenCalled();
    expect(callbacks.replayDelivery).not.toHaveBeenCalled();
  });
  it("discovers original delivery IDs with GETs and never automatically replays", async () => {
    const { client, callbacks, commands } = callbackTestClient();
    const { root } = mountCommandTest(
      CallbacksPage,
      { client },
      { report: ref(callbackTestReport()) },
    );
    await flushCommandUI();
    await selectDelivery(root);
    expect(commands.prepare).not.toHaveBeenCalled();
    expect(callbacks.listDeliveries.mock.calls[0]?.[0]).toBe(
      callbackSubscriptionID,
    );
    expect(callbacks.getDelivery.mock.calls[0]?.slice(0, 2)).toEqual([
      callbackSubscriptionID,
      callbackDeliveryID,
    ]);
    expect(callbacks.replayDelivery).not.toHaveBeenCalled();
    expect(root.textContent).toContain("不是完整尝试时间线");
    commandButton(root, "确认此失败投递的重放意图").click();
    await flushCommandUI();
    expect(root.textContent).toContain(callbackTestDelivery().event_id);
    expect(callbacks.replayDelivery).not.toHaveBeenCalled();
    expect(commands.prepare).toHaveBeenCalledTimes(1);
    expect(commands.prepare.mock.calls[0][0].callback).toEqual({
      subscription_id: callbackSubscriptionID,
      delivery_id: callbackDeliveryID,
      event_id: callbackTestDelivery().event_id,
      revision: 7,
      lease_generation: 8,
    });
    await vi.waitFor(() =>
      expect(root.querySelectorAll('input[autocomplete="off"]')).toHaveLength(
        3,
      ),
    );
    const inputs = root.querySelectorAll<HTMLInputElement>(
      'input[autocomplete="off"]',
    );
    inputs[0].value = callbackDeliveryID;
    inputs[0].dispatchEvent(new Event("input"));
    inputs[1].value = callbackTestDelivery().event_id;
    inputs[1].dispatchEvent(new Event("input"));
    await flushCommandUI();
    commandButton(root, "确认原投递 CAS 排队").click();
    await flushCommandUI();
    expect(commands.execute).toHaveBeenCalledTimes(1);
    expect(callbacks.replayDelivery).not.toHaveBeenCalled();
    expect(root.textContent).toContain("不代表已投递或接收端业务完成");
    expect(callbacks.create).not.toHaveBeenCalled();
  });
  it("keeps the original durable intent and confirmation during revocation/outage and unknown responses", async () => {
    const { client, callbacks, commands } = callbackTestClient();
    const report = ref<CapabilityReport | undefined>(callbackTestReport());
    const { root } = mountCommandTest(CallbacksPage, { client }, { report });
    await flushCommandUI();
    await showReplay(root);
    const inputs = root.querySelectorAll<HTMLInputElement>(
      'input[autocomplete="off"]',
    );
    inputs[0].value = callbackDeliveryID;
    inputs[0].dispatchEvent(new Event("input"));
    inputs[1].value = callbackTestDelivery().event_id;
    inputs[1].dispatchEvent(new Event("input"));
    report.value = undefined;
    await flushCommandUI();
    expect(commandButton(root, "确认原投递 CAS 排队").disabled).toBe(true);
    expect(commands.execute).not.toHaveBeenCalled();
    report.value = callbackTestReport();
    await flushCommandUI();
    commands.execute.mockRejectedValueOnce(new Error("回应丢失，原结果未知"));
    commandButton(root, "确认原投递 CAS 排队").click();
    await flushCommandUI();
    const original = commands.execute.mock.calls[0][0];
    expect(root.textContent).toContain("回应丢失");
    expect(commands.execute).toHaveBeenCalledTimes(1);
    commands.get.mockResolvedValueOnce(executedTestIntent(original));
    commandButton(root, "仅查询原意图 / 回执").click();
    await flushCommandUI();
    expect(commands.get.mock.calls[0][0]).toBe(original.intent_id);
    expect(commands.execute).toHaveBeenCalledTimes(1);
    expect(commands.prepare).toHaveBeenCalledTimes(1);
    expect(callbacks.replayDelivery).not.toHaveBeenCalled();
  });
  it("keeps metadata recovery readable when delivery/write guidance is disabled", async () => {
    const { client, callbacks } = callbackTestClient();
    const report = callbackTestReport();
    Object.assign(
      report.capabilities.find((item) => item.name === "callback.manage")!,
      { enabled: false, available: false },
    );
    const { root } = mountCommandTest(
      CallbacksPage,
      { client },
      { report: ref(report) },
    );
    await flushCommandUI();
    await selectDelivery(root);
    expect(callbacks.getDelivery).toHaveBeenCalledTimes(1);
    expect(commandButton(root, "验证并注册").disabled).toBe(true);
    expect(commandButton(root, "删除").disabled).toBe(true);
    expect(commandButton(root, "确认此失败投递的重放意图").disabled).toBe(true);
    expect(callbacks.replayDelivery).not.toHaveBeenCalled();
  });
  it("does not replay an expired delivering row or accept foreign tenant metadata", async () => {
    const { client, callbacks } = callbackTestClient();
    callbacks.getDelivery.mockResolvedValueOnce({
      ...callbackTestDelivery(),
      status: "delivering",
      lease_until: new Date(Date.now() - 1000).toISOString(),
    });
    const { root } = mountCommandTest(
      CallbacksPage,
      { client },
      { report: ref(callbackTestReport()) },
    );
    await flushCommandUI();
    await selectDelivery(root);
    expect(commandButton(root, "确认此失败投递的重放意图").disabled).toBe(true);
    callbacks.getDelivery.mockResolvedValueOnce({
      ...callbackTestDelivery(),
      tenant_id: "tenant-b",
    });
    commandButton(root, "仅刷新投递状态").click();
    await flushCommandUI();
    expect(root.textContent).toContain("投递详情与原目标/当前租户不一致");
    expect(callbacks.replayDelivery).not.toHaveBeenCalled();
  });
  it("aborts stale detail GETs when another subscription is selected", async () => {
    const { client, callbacks } = callbackTestClient();
    callbacks.list.mockResolvedValueOnce({
      items: [
        {
          id: callbackSubscriptionID,
          tenant_id: "tenant-a",
          owner_uid: "caller",
          endpoint: "https://callback.example/a",
          verified: true,
          enabled: true,
          event_types: [],
        },
        {
          id: callbackOtherSubscriptionID,
          tenant_id: "tenant-a",
          owner_uid: "caller",
          endpoint: "https://callback.example/b",
          verified: true,
          enabled: true,
          event_types: [],
        },
      ],
      nextCursor: undefined,
    });
    let finish!: (value: CallbackDelivery) => void;
    callbacks.getDelivery.mockImplementationOnce(
      async () =>
        new Promise<CallbackDelivery>((resolve) => {
          finish = resolve;
        }),
    );
    const { root } = mountCommandTest(
      CallbacksPage,
      { client },
      { report: ref(callbackTestReport()) },
    );
    await flushCommandUI();
    await selectDelivery(root);
    const signal = callbacks.getDelivery.mock.calls[0]?.[2];
    expect(signal?.aborted).toBe(false);
    const second = [
      ...root.querySelectorAll<HTMLButtonElement>("button"),
    ].filter((button) => button.textContent?.includes("查看此订阅投递"))[1];
    second.click();
    await flushCommandUI();
    expect(signal?.aborted).toBe(true);
    finish(callbackTestDelivery());
    await flushCommandUI();
    expect(root.textContent).not.toContain("确认此失败投递的重放意图");
    expect(callbacks.replayDelivery).not.toHaveBeenCalled();
  });
  it("aborts pagination on filter replacement and does not merge late rows", async () => {
    const { client, callbacks } = callbackTestClient();
    const item = callbackTestDelivery();
    let finish!: (value: {
      items: CallbackDelivery[];
      nextCursor: undefined;
    }) => void;
    callbacks.listDeliveries.mockResolvedValueOnce({
      items: [item],
      nextCursor: item.id,
    });
    callbacks.listDeliveries.mockImplementationOnce(
      async () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    callbacks.listDeliveries.mockResolvedValueOnce({
      items: [{ ...item, status: "success" }],
      nextCursor: undefined,
    });
    const { root } = mountCommandTest(
      CallbacksPage,
      { client },
      { report: ref(callbackTestReport()) },
    );
    await flushCommandUI();
    commandButton(root, "查看此订阅投递").click();
    await flushCommandUI();
    commandButton(root, "加载更多").click();
    await flushCommandUI();
    const signal = callbacks.listDeliveries.mock.calls[1]?.[1]?.signal;
    expect(signal?.aborted).toBe(false);
    const filter = root.querySelector<HTMLSelectElement>("select")!;
    filter.value = "success";
    filter.dispatchEvent(new Event("change"));
    await flushCommandUI();
    expect(signal?.aborted).toBe(true);
    finish({
      items: [
        {
          ...item,
          id: "01800000-0000-7000-8000-000000000008",
          action: "late.old",
        },
      ],
      nextCursor: undefined,
    });
    await flushCommandUI();
    expect(root.textContent).not.toContain("late.old");
    expect(callbacks.listDeliveries).toHaveBeenCalledTimes(3);
    expect(callbacks.replayDelivery).not.toHaveBeenCalled();
  });
  it("rejects late detail metadata after issuer/client identity changes without a write retry", async () => {
    const { client, callbacks } = callbackTestClient();
    let finish!: (value: CallbackDelivery) => void;
    callbacks.getDelivery.mockImplementationOnce(
      async () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    const scope = ref<string | undefined>(testScope);
    const { root } = mountCommandTest(
      CallbacksPage,
      { client },
      { report: ref(callbackTestReport()), scope },
    );
    await flushCommandUI();
    await selectDelivery(root);
    scope.value = JSON.stringify([
      "https://id.example/v1",
      "https://another-issuer.example",
      "another-client",
      "tenant-a",
      callbackSubscriptionID,
    ]);
    finish(callbackTestDelivery());
    await flushCommandUI();
    expect(root.textContent).toContain("投递详情与原目标/当前租户不一致");
    expect(callbacks.getDelivery).toHaveBeenCalledTimes(1);
    expect(callbacks.replayDelivery).not.toHaveBeenCalled();
  });
  it("keeps the original registration key and hides secrets on a mismatched receipt", async () => {
    const { client, callbacks } = callbackTestClient();
    callbacks.create.mockResolvedValueOnce({
      subscription: {
        id: callbackSubscriptionID,
        tenant_id: "tenant-b",
        endpoint: "https://another.example/events",
        event_types: ["account.provisioned"],
        verified: true,
        enabled: true,
      },
      secret: "untrusted-secret",
    });
    const { root } = mountCommandTest(
      CallbacksPage,
      { client },
      { report: ref(callbackTestReport()) },
    );
    await flushCommandUI();
    submitRegistration(root);
    await flushCommandUI();
    expect(root.textContent).toContain("注册回执与原端点/范围/租户不一致");
    expect(root.textContent).not.toContain("untrusted-secret");
    root
      .querySelector("form")!
      .dispatchEvent(new Event("submit", { cancelable: true }));
    await flushCommandUI();
    expect(callbacks.create).toHaveBeenCalledTimes(2);
    expect(callbacks.create.mock.calls[1]).toEqual(
      callbacks.create.mock.calls[0],
    );
  });
  it("will not change an unresolved registration plan or create a second key", async () => {
    const { client, callbacks } = callbackTestClient();
    callbacks.create.mockRejectedValueOnce(new Error("注册结果未知"));
    const { root } = mountCommandTest(
      CallbacksPage,
      { client },
      { report: ref(callbackTestReport()) },
    );
    await flushCommandUI();
    submitRegistration(root);
    await flushCommandUI();
    const input = root.querySelector<HTMLInputElement>("input")!;
    input.value = "https://another-endpoint.example/events";
    input.dispatchEvent(new Event("input"));
    root
      .querySelector("form")!
      .dispatchEvent(new Event("submit", { cancelable: true }));
    await flushCommandUI();
    expect(root.textContent).toContain("原注册结果尚未确认");
    expect(callbacks.create).toHaveBeenCalledTimes(1);
  });
  it("preserves original registration input/key after an unknown result and ignores late identity replies", async () => {
    const { client, callbacks } = callbackTestClient();
    callbacks.create.mockRejectedValueOnce(new Error("注册回应丢失"));
    const scope = ref<string | undefined>(testScope);
    const { root } = mountCommandTest(
      CallbacksPage,
      { client },
      { report: ref(callbackTestReport()), scope },
    );
    await flushCommandUI();
    submitRegistration(root);
    await flushCommandUI();
    const key = callbacks.create.mock.calls[0]?.[1];
    expect(root.textContent).toContain("注册回应丢失");
    const input = root.querySelector<HTMLInputElement>("input")!;
    expect(input.disabled).toBe(true);
    const form = root.querySelector("form")!;
    form.dispatchEvent(new Event("submit", { cancelable: true }));
    await flushCommandUI();
    expect(callbacks.create.mock.calls.map((call) => call[1])).toEqual([
      key,
      key,
    ]);
    let finish!: (value: {
      subscription: { id: string };
      secret: string;
    }) => void;
    callbacks.create.mockImplementationOnce(
      async () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    submitRegistration(root);
    await flushCommandUI();
    scope.value = JSON.stringify([
      "https://id.example/v1",
      "https://snaplink.example",
      "console",
      "tenant-b",
      callbackSubscriptionID,
    ]);
    unmountCommandTests();
    finish({
      subscription: { id: callbackSubscriptionID },
      secret: "late-one-time-secret",
    });
    await flushCommandUI();
    expect(root.textContent).not.toContain("late-one-time-secret");
  });
});
