import { afterEach, describe, expect, it, vi } from "vitest";
import { createApp, h, ref } from "vue";
import type { JsonRecord, PageData } from "../types";
import PagedRecordTable from "./PagedRecordTable.vue";
import { mergePage } from "./table";
import { commandButton, flushCommandUI } from "./commandTestFixtures";
vi.mock("@iris-ui-kit/vue", async () =>
  (await import("./commandTestFixtures")).irisCommandTestStubs(),
);
const dispose: Array<() => void> = [];
afterEach(() => {
  dispose.splice(0).forEach((stop) => stop());
});
function mountPager() {
  const root = document.createElement("div");
  const page = ref<PageData<JsonRecord>>({
    items: [{ id: "first" }],
    nextCursor: "cursor-a",
  });
  let finish!: (value: PageData<JsonRecord>) => void;
  const loadPage = vi.fn(
    async (_cursor: string, _signal: AbortSignal) =>
      new Promise<PageData<JsonRecord>>((resolve) => {
        finish = resolve;
      }),
  );
  const app = createApp({
    setup: () => () =>
      h(PagedRecordTable, {
        page: page.value,
        loadPage,
        columns: [{ key: "id", label: "ID" }],
      }),
  });
  app.mount(root);
  let mounted = true;
  const stop = () => {
    if (mounted) {
      mounted = false;
      app.unmount();
    }
  };
  dispose.push(stop);
  return {
    root,
    page,
    loadPage,
    finish: (value: PageData<JsonRecord>) => finish(value),
    stop,
  };
}
describe("mergePage", () => {
  it("appends a keyset page and advances its cursor", () => {
    expect(
      mergePage(
        { items: [{ id: "3" }], nextCursor: "3" },
        { items: [{ id: "2" }], nextCursor: "2" },
        "3",
      ),
    ).toEqual({ items: [{ id: "3" }, { id: "2" }], nextCursor: "2" });
  });

  it("rejects a cursor cycle across already-consumed pages", () => {
    const consumed = new Set(["cursor-a", "cursor-b"]);

    expect(() =>
      mergePage(
        { items: [{ id: "1" }], nextCursor: "cursor-c" },
        { items: [{ id: "2" }], nextCursor: "cursor-a" },
        "cursor-c",
        consumed,
      ),
    ).toThrow("分页游标出现循环");
  });

  it("rejects a cursor that does not advance", () => {
    expect(() =>
      mergePage(
        { items: [{ id: "3" }], nextCursor: "3" },
        { items: [{ id: "3" }], nextCursor: "3" },
        "3",
      ),
    ).toThrow("分页游标出现循环");
  });
});

describe("bounded page read lifetime", () => {
  it("passes a cancellation signal, aborts superseded pages and rejects late adoption", async () => {
    const pager = mountPager();
    commandButton(pager.root, "加载更多").click();
    await flushCommandUI();
    const signal = pager.loadPage.mock.calls[0][1];
    expect(signal.aborted).toBe(false);
    pager.page.value = { items: [{ id: "new-context" }] };
    await flushCommandUI();
    expect(signal.aborted).toBe(true);
    pager.finish({ items: [{ id: "late-old-row" }] });
    await flushCommandUI();
    expect(pager.root.textContent).toContain("new-context");
    expect(pager.root.textContent).not.toContain("late-old-row");
    expect(pager.loadPage).toHaveBeenCalledTimes(1);
  });
  it("aborts unmounted page requests without automatically reading or writing", async () => {
    const pager = mountPager();
    commandButton(pager.root, "加载更多").click();
    await flushCommandUI();
    const signal = pager.loadPage.mock.calls[0][1];
    pager.stop();
    expect(signal.aborted).toBe(true);
    pager.finish({ items: [{ id: "late-row" }], nextCursor: "cursor-b" });
    await flushCommandUI();
    expect(pager.loadPage).toHaveBeenCalledTimes(1);
    expect(pager.root.textContent).not.toContain("late-row");
  });
});
