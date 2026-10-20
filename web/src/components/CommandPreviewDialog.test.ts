import { createApp, nextTick, h, ref } from "vue";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AeroIdClient } from "../api/aeroId";
import type { CommandIntent } from "../api/commandIntent";
import CommandPreviewDialog from "./CommandPreviewDialog.vue";
import { abandonedTestIntent, executedTestIntent } from "./commandTestFixtures";

vi.mock("@iris-ui-kit/vue", async () => {
  const { defineComponent, h } = await import("vue");
  const wrapper = defineComponent({
    setup:
      (_, { slots }) =>
      () =>
        h("div", slots.default?.()),
  });
  return {
    IrisAlert: wrapper,
    IrisDialog: wrapper,
    IrisDialogContent: wrapper,
    IrisDialogDescription: wrapper,
    IrisDialogTitle: wrapper,
    IrisFormField: wrapper,
    IrisButton: defineComponent({
      props: { disabled: Boolean },
      emits: ["click"],
      setup:
        (props, { slots, emit }) =>
        () =>
          h(
            "button",
            { disabled: props.disabled, onClick: () => emit("click") },
            slots.default?.(),
          ),
    }),
    IrisInput: defineComponent({
      props: { modelValue: String, disabled: Boolean },
      emits: ["update:modelValue"],
      setup:
        (props, { emit }) =>
        () =>
          h("input", {
            value: props.modelValue,
            disabled: props.disabled,
            onInput: (event: Event) =>
              emit(
                "update:modelValue",
                (event.target as HTMLInputElement).value,
              ),
          }),
    }),
  };
});
const apps: ReturnType<typeof createApp>[] = [];
afterEach(() => {
  apps.splice(0).forEach((app) => app.unmount());
  vi.useRealTimers();
});
async function flush() {
  for (let i = 0; i < 4; i++) {
    await Promise.resolve();
    await nextTick();
  }
}
function fixture(): CommandIntent {
  return {
    intent_id: "01800000-0000-7000-8000-000000000001",
    schema_version: 1,
    account_id: "01800000-0000-7000-8000-000000000002",
    tenant_id: "tenant-a",
    client_id: "console",
    issuer: "https://snaplink.example",
    version: 1,
    status: "prepared",
    plan: { kind: "export", datasets: ["aero-vault.usage"] },
    preconditions: {
      account_status: "active",
      home_region: "local",
      writer_epoch: 3,
      resolver_version: "v1",
      snapshots: [],
    },
    confirmation_digest: `v1:${"a".repeat(64)}`,
    destructive: false,
    authorization_version: "unsupported",
    resource_authorization: "unknown",
    created_at: new Date(Date.now() - 1_000).toISOString(),
    expires_at: new Date(Date.now() + 300_000).toISOString(),
  };
}
function mount(intent: CommandIntent) {
  const execute = vi.fn().mockRejectedValue(new Error("uncertain response"));
  const root = document.createElement("div");
  const executed = vi.fn();
  const abandoned = vi.fn();
  const abandon = vi.fn().mockRejectedValue(new Error("uncertain disposition"));
  const eligible = ref(true);
  const get = vi.fn(async (_id: string, _signal?: AbortSignal) => intent);
  const client = {
    commands: { execute, get, abandon },
  } as unknown as AeroIdClient;
  const app = createApp({
    setup: () => () =>
      h(CommandPreviewDialog, {
        intent,
        client,
        eligible: eligible.value,
        onExecuted: executed,
        onAbandoned: abandoned,
      }),
  });
  apps.push(app);
  app.mount(root);
  return { root, execute, executed, get, eligible, app, abandon, abandoned };
}
function confirm(root: HTMLElement, value: string) {
  const input = root.querySelector("input")!;
  input.value = value;
  input.dispatchEvent(new Event("input"));
}
function executeButton(root: HTMLElement) {
  return [...root.querySelectorAll("button")].find((button) =>
    button.textContent?.includes("确认创建任务"),
  )!;
}

describe("server preview explicit confirmation", () => {
  it("requires the exact intent ID to withdraw even when generation guidance is unavailable", async () => {
    const intent = fixture();
    const item = mount(intent);
    item.eligible.value = false;
    const button = [...item.root.querySelectorAll("button")].find((node) =>
      node.textContent?.includes("放弃未执行预览"),
    )!;
    const input = item.root.querySelectorAll("input")[1];
    input.value = intent.account_id;
    input.dispatchEvent(new Event("input"));
    await flush();
    expect(button.disabled).toBe(true);
    input.value = intent.intent_id;
    input.dispatchEvent(new Event("input"));
    await flush();
    button.click();
    await flush();
    expect(item.abandon).toHaveBeenCalledWith(intent);
    expect(item.root.textContent).toContain("uncertain disposition");
    expect(input.value).toBe(intent.intent_id);
    expect(item.execute).not.toHaveBeenCalled();
    expect(item.abandoned).not.toHaveBeenCalled();
  });
  it("emits only confirmed withdrawal, or the original receipt if execute won", async () => {
    for (const receiptWon of [false, true]) {
      const intent = fixture();
      const item = mount(intent);
      const result = receiptWon
        ? executedTestIntent(intent)
        : abandonedTestIntent(intent);
      item.abandon.mockResolvedValue(result);
      const input = item.root.querySelectorAll("input")[1];
      input.value = intent.intent_id;
      input.dispatchEvent(new Event("input"));
      await flush();
      [...item.root.querySelectorAll("button")]
        .find((node) => node.textContent?.includes("放弃未执行预览"))!
        .click();
      await flush();
      expect(receiptWon ? item.executed : item.abandoned).toHaveBeenCalledWith(
        result,
      );
      expect(
        receiptWon ? item.abandoned : item.executed,
      ).not.toHaveBeenCalled();
      expect(item.execute).not.toHaveBeenCalled();
    }
  });
  it("does not execute on open; uncertain responses retain the same confirmed intent", async () => {
    const intent = fixture();
    const { root, execute } = mount(intent);
    await flush();
    expect(execute).not.toHaveBeenCalled();
    expect(executeButton(root).disabled).toBe(true);
    confirm(root, "确认");
    await flush();
    executeButton(root).click();
    await flush();
    expect(execute).toHaveBeenCalledTimes(1);
    expect(root.textContent).toContain("不会自动重试");
    executeButton(root).click();
    await flush();
    expect(execute.mock.calls[1][0]).toBe(intent);
    expect(execute.mock.calls[0][0]).toBe(intent);
  });
  it("pauses confirmation on revocation/outage without losing input or auto-posting after refresh", async () => {
    const { root, execute, eligible } = mount(fixture());
    confirm(root, "确认");
    eligible.value = false;
    await flush();
    expect(executeButton(root).disabled).toBe(true);
    expect(root.querySelector("input")?.value).toBe("确认");
    expect(execute).not.toHaveBeenCalled();
    eligible.value = true;
    await flush();
    expect(executeButton(root).disabled).toBe(false);
    expect(execute).not.toHaveBeenCalled();
  });
  it("queries an expired original receipt with GET only and never changes TTL or confirmation", async () => {
    const intent = fixture();
    intent.expires_at = new Date(Date.now() - 1).toISOString();
    const { root, execute, get, executed } = mount(intent);
    const result: CommandIntent = {
      ...intent,
      version: 2,
      status: "executed",
      receipt_kind: "sync_job",
      receipt_id: "01800000-0000-7000-8000-000000000003",
    };
    get.mockResolvedValue(result);
    const query = [...root.querySelectorAll("button")].find((item) =>
      item.textContent?.includes("仅查询原意图"),
    )!;
    query.click();
    await flush();
    expect(get).toHaveBeenCalledWith(intent.intent_id, expect.any(AbortSignal));
    expect(executed).toHaveBeenCalledWith(result);
    expect(execute).not.toHaveBeenCalled();
    expect(executeButton(root).disabled).toBe(true);
  });
  it("requires the exact target ID for destructive plans", async () => {
    const intent = { ...fixture(), destructive: true };
    const { root, execute } = mount(intent);
    confirm(root, "确认");
    await flush();
    expect(executeButton(root).disabled).toBe(true);
    confirm(root, intent.account_id);
    await flush();
    executeButton(root).click();
    await flush();
    expect(execute).toHaveBeenCalledOnce();
  });
  it("disables an expired preview without sending a command", async () => {
    vi.useFakeTimers();
    const intent = fixture();
    intent.expires_at = new Date(Date.now() + 100).toISOString();
    const { root, execute } = mount(intent);
    confirm(root, "确认");
    await flush();
    await vi.advanceTimersByTimeAsync(101);
    await flush();
    expect(executeButton(root).disabled).toBe(true);
    expect(execute).not.toHaveBeenCalled();
  });
});
