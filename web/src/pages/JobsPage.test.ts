import { createApp, nextTick } from "vue";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AeroIdClient } from "../api/aeroId";
import JobsPage from "./JobsPage.vue";

vi.mock("@iris-ui-kit/vue", async () => {
  const { defineComponent, h } = await import("vue");
  const wrapper = defineComponent({
    setup(_, { slots }) {
      return () => h("div", slots.default?.());
    },
  });
  return {
    IrisCard: wrapper,
    IrisAlert: wrapper,
    IrisButton: defineComponent({
      props: { disabled: Boolean },
      emits: ["click"],
      setup(props, { slots, emit }) {
        return () =>
          h(
            "button",
            { disabled: props.disabled, onClick: () => emit("click") },
            slots.default?.(),
          );
      },
    }),
    IrisCheckbox: defineComponent({
      props: { modelValue: Boolean, disabled: Boolean },
      emits: ["update:modelValue"],
      setup(props, { slots, emit }) {
        return () =>
          h("label", [
            h("input", {
              type: "checkbox",
              checked: props.modelValue,
              disabled: props.disabled,
              onChange: (event: Event) =>
                emit(
                  "update:modelValue",
                  (event.target as HTMLInputElement).checked,
                ),
            }),
            slots.default?.(),
          ]);
      },
    }),
  };
});
vi.mock("../components/PageLoading.vue", () => ({
  default: { template: "<div>加载中</div>" },
}));
vi.mock("../components/PageError.vue", () => ({
  default: { template: "<div>读取失败</div>" },
}));
vi.mock("../components/PagedRecordTable.vue", () => ({
  default: { template: "<div>任务列表</div>" },
}));
vi.mock("../components/JobDetails.vue", () => ({
  default: { template: "<div>任务详情</div>" },
}));
vi.mock("../components/CommandPreviewDialog.vue", () => ({
  default: {
    props: ["intent", "client"],
    emits: ["close", "executed"],
    template: '<div>服务端命令预览 {{ intent.plan.datasets.join("、") }}</div>',
  },
}));

const apps: ReturnType<typeof createApp>[] = [];
afterEach(() => {
  apps.splice(0).forEach((app) => app.unmount());
});
async function flush(): Promise<void> {
  for (let i = 0; i < 4; i += 1) {
    await Promise.resolve();
    await nextTick();
  }
}
function mount(client: AeroIdClient): HTMLElement {
  const root = document.createElement("div");
  const app = createApp(JobsPage, { client });
  apps.push(app);
  app.mount(root);
  return root;
}
function fakeClient() {
  return {
    listSyncJobs: vi.fn(async () => ({ items: [] })),
    listDatasets: vi.fn(async () => [
      { name: "aero-vault.usage", source: "aero-vault", pii: false },
      { name: "aero-im.workspaces", source: "aero-im", pii: false },
    ]),
    commands: {
      prepare: vi.fn(async (plan: unknown, _key: string) => ({
        intent_id: "01800000-0000-7000-8000-000000000001",
        status: "prepared",
        plan,
      })),
    },
    createSyncJob: vi.fn(async () => ({ job_id: "job-1", status: "pending" })),
    createExportJob: vi.fn(async () => ({
      job_id: "job-2",
      status: "pending",
    })),
  };
}
function choose(root: HTMLElement, index: number): void {
  const checkbox = root.querySelectorAll<HTMLInputElement>(
    'input[type="checkbox"]',
  )[index];
  checkbox.checked = true;
  checkbox.dispatchEvent(new Event("change"));
}
function button(root: HTMLElement, text: string): HTMLButtonElement {
  const found = [...root.querySelectorAll("button")].find((node) =>
    node.textContent?.includes(text),
  );
  if (!found) throw new Error(`missing button: ${text}`);
  return found;
}

describe("JobsPage explicit scope", () => {
  it("starts with no implicit full scope and only prepares selected datasets", async () => {
    const client = fakeClient();
    const root = mount(client as unknown as AeroIdClient);
    await flush();
    expect(button(root, "同步所选").disabled).toBe(true);
    expect(button(root, "创建加密导出").disabled).toBe(true);
    choose(root, 0);
    await flush();
    button(root, "创建加密导出").click();
    await flush();
    expect(client.commands.prepare).toHaveBeenCalledWith(
      { kind: "export", datasets: ["aero-vault.usage"] },
      expect.any(String),
    );
    expect(client.createExportJob).not.toHaveBeenCalled();
    expect(client.createSyncJob).not.toHaveBeenCalled();
    expect(root.textContent).toContain("服务端命令预览");
  });

  it("retains the same request key after an ambiguous write response", async () => {
    const client = fakeClient();
    client.commands.prepare.mockRejectedValue(new Error("network timeout"));
    const root = mount(client as unknown as AeroIdClient);
    await flush();
    choose(root, 1);
    await flush();
    button(root, "创建加密导出").click();
    await flush();
    expect(client.commands.prepare).toHaveBeenCalledTimes(1);
    await flush(); // No automatic write retry.
    button(root, "创建加密导出").click();
    await flush();
    expect(client.commands.prepare.mock.calls[1]).toEqual(
      client.commands.prepare.mock.calls[0],
    );
    expect(client.createExportJob).not.toHaveBeenCalled();
  });
});
