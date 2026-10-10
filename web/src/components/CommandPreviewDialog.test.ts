import { createApp, nextTick } from "vue";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AeroIdClient } from "../api/aeroId";
import type { CommandIntent } from "../api/commandIntent";
import CommandPreviewDialog from "./CommandPreviewDialog.vue";

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
  const app = createApp(CommandPreviewDialog, {
    intent,
    client: { commands: { execute } } as unknown as AeroIdClient,
    onExecuted: executed,
  });
  apps.push(app);
  app.mount(root);
  return { root, execute, executed };
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
