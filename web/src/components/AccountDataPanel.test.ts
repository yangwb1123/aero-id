import { createApp, nextTick } from "vue";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AeroIdClient } from "../api/aeroId";
import type { AggregateView, Dataset } from "../types";
import AccountDataPanel from "./AccountDataPanel.vue";

vi.mock("@iris-ui-kit/vue", async () => {
  const { defineComponent, h } = await import("vue");
  const wrapper = defineComponent({
    setup(_props, { slots }) {
      return () => h("div", slots.default?.());
    },
  });
  const IrisButton = defineComponent({
    props: { type: String, disabled: Boolean, loading: Boolean },
    emits: ["click"],
    setup(props, { emit, slots }) {
      return () =>
        h(
          "button",
          {
            type: props.type ?? "button",
            disabled: props.disabled,
            onClick: () => emit("click"),
          },
          slots.default?.(),
        );
    },
  });
  const IrisInput = defineComponent({
    props: { modelValue: String, placeholder: String },
    emits: ["update:modelValue"],
    setup(props, { emit }) {
      return () =>
        h("input", {
          value: props.modelValue,
          placeholder: props.placeholder,
          onInput: (event: Event) =>
            emit("update:modelValue", (event.target as HTMLInputElement).value),
        });
    },
  });
  const IrisAlert = defineComponent({
    props: { title: String },
    setup(props, { slots }) {
      return () => h("div", [props.title, slots.default?.()]);
    },
  });
  return {
    IrisAlert,
    IrisButton,
    IrisCard: wrapper,
    IrisFormField: wrapper,
    IrisInput,
    IrisSelect: wrapper,
  };
});

vi.mock("./RecordTable.vue", async () => {
  const { defineComponent, h } = await import("vue");
  return {
    default: defineComponent({
      props: { records: { type: Array, default: () => [] } },
      setup(props) {
        return () => h("pre", JSON.stringify(props.records));
      },
    }),
  };
});

const datasets: Dataset[] = [
  { name: "aero-im.workspaces", source: "aero-im", pii: false },
];

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function mountPanel(client: AeroIdClient): {
  app: ReturnType<typeof createApp>;
  root: HTMLDivElement;
} {
  const root = document.createElement("div");
  const app = createApp(AccountDataPanel, { client, datasets });
  app.mount(root);
  return { app, root };
}

async function flushPromises(): Promise<void> {
  await Promise.resolve();
  await nextTick();
  await Promise.resolve();
  await nextTick();
}

function clickRead(root: HTMLElement): void {
  const button = [...root.querySelectorAll("button")].find((item) =>
    item.textContent?.includes("读取账户数据"),
  );
  if (!button) throw new Error("read button was not rendered");
  button.click();
}

function result(value: string): AggregateView {
  return {
    snapshots: [{ dataset: "aero-im.workspaces", data: { value } }],
  };
}

const mounted: Array<{
  app: ReturnType<typeof createApp>;
  root: HTMLDivElement;
}> = [];

afterEach(() => {
  for (const item of mounted.splice(0)) {
    item.app.unmount();
    item.root.remove();
  }
});

describe("AccountDataPanel request races", () => {
  it("keeps the newest result when requests resolve out of order", async () => {
    const older = deferred<AggregateView>();
    const newer = deferred<AggregateView>();
    const getMyData = vi
      .fn()
      .mockReturnValueOnce(older.promise)
      .mockReturnValueOnce(newer.promise);
    const { app, root } = mountPanel({ getMyData } as unknown as AeroIdClient);
    mounted.push({ app, root });

    clickRead(root);
    clickRead(root);
    newer.resolve(result("new"));
    await flushPromises();
    older.resolve(result("old"));
    await flushPromises();

    expect(root.textContent).toContain('"value":"new"');
    expect(root.textContent).not.toContain('"value":"old"');
  });

  it("discards an in-flight result after the query changes", async () => {
    const pending = deferred<AggregateView>();
    const getMyData = vi.fn(() => pending.promise);
    const { app, root } = mountPanel({ getMyData } as unknown as AeroIdClient);
    mounted.push({ app, root });

    clickRead(root);
    const maxAge = root.querySelector<HTMLInputElement>(
      'input[placeholder="5m"]',
    );
    if (!maxAge) throw new Error("Max Age field was not rendered");
    maxAge.value = "30s";
    maxAge.dispatchEvent(new Event("input", { bubbles: true }));
    await nextTick();
    pending.resolve(result("stale-query"));
    await flushPromises();

    expect(root.textContent).not.toContain("stale-query");
  });
});
