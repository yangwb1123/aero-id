import { afterEach, describe, expect, it, vi } from "vitest";
import { createApp, ref } from "vue";
import { AeroIdClient } from "../api/aeroId";
import { capabilityContextKey } from "../hooks/capabilityContext";
import type { AggregateView, JsonRecord } from "../types";
import { commandTestReport } from "../components/commandTestFixtures";
import ResourcesPage from "./ResourcesPage.vue";

vi.mock("@iris-ui-kit/vue", async () =>
  (await import("../components/commandTestFixtures")).irisCommandTestStubs(),
);
const cleanups: Array<() => void> = [];
afterEach(() => cleanups.splice(0).forEach((cleanup) => cleanup()));
const descriptors = [
  { name: "aero-vault.usage", source: "aero-vault", pii: false },
  { name: "aero-vault.buckets", source: "aero-vault", pii: false },
  { name: "aero-im.workspaces", source: "aero-im", pii: false },
];
function snapshot(dataset: string, data: JsonRecord): JsonRecord {
  return {
    dataset,
    source: dataset.startsWith("aero-vault.") ? "aero-vault" : "aero-im",
    source_region: "local",
    status: "fresh",
    version: 42,
    generated_at: "2026-10-16T09:00:00Z",
    last_synced_at: "2026-10-16T09:01:00Z",
    data,
  };
}
function resourceView(): AggregateView {
  return {
    account: {
      account_id: "01800000-0000-7000-8000-000000000002",
      tenant_id: "tenant-demo",
      home_region: "local",
    },
    consistency: "eventual",
    partial: false,
    source_errors: {},
    stale_datasets: [],
    snapshots: [
      snapshot("aero-vault.usage", {
        tenant_id: "tenant-demo",
        used_bytes: 1048576,
        used_objects: 12,
        max_bytes: 2097152,
        max_objects: 100,
      }),
      snapshot("aero-vault.buckets", {
        items: ["design-assets", "project-backups"],
      }),
      snapshot("aero-im.workspaces", {
        items: [{ id: "workspace-demo", name: "设计协作", slug: "design" }],
      }),
    ],
  };
}
async function mount(view = resourceView()) {
  let current = view;
  const fetcher = vi.fn<typeof fetch>(async (input) => {
    const path = new URL(String(input)).pathname;
    return new Response(
      JSON.stringify({
        data: path.endsWith("account-datasets")
          ? { datasets: descriptors }
          : current,
      }),
      { headers: { "Content-Type": "application/json" } },
    );
  });
  const initialReport = commandTestReport();
  initialReport.tenant_id = "tenant-demo";
  initialReport.datasets = descriptors.map((item) => ({
    ...item,
    registered: true,
    enabled: true,
    authorization: "allowed",
    resource_authorization: "unknown",
    status: "fresh",
    fresh: true,
    version: 42,
  }));
  const report = ref(initialReport);
  const root = document.createElement("div");
  document.body.append(root);
  const app = createApp(ResourcesPage, {
    client: new AeroIdClient(
      "https://id.example/v1",
      () => "test-only",
      fetcher,
    ),
  });
  app.provide(capabilityContextKey, () => report.value);
  app.mount(root);
  cleanups.push(() => {
    app.unmount();
    root.remove();
  });
  await vi.waitFor(() => expect(root.textContent).toContain("允许的数据集"));
  return {
    root,
    fetcher,
    report,
    replace: (value: AggregateView) => {
      current = value;
    },
  };
}
function button(root: HTMLElement, text: string): HTMLButtonElement {
  const found = [...root.querySelectorAll("button")].find((item) =>
    item.textContent?.includes(text),
  );
  expect(found, `button ${text}`).toBeDefined();
  return found!;
}
function card(root: HTMLElement, dataset: string): HTMLElement {
  const found = root.querySelector<HTMLElement>(
    `[data-resource-dataset="${dataset}"]`,
  );
  expect(found).not.toBeNull();
  return found!;
}
describe("ResourcesPage business summaries on the real API client", () => {
  it("distinguishes valid empty lists, a missing projection and an unconfigured source", async () => {
    const view = resourceView();
    view.snapshots = [snapshot("aero-vault.buckets", { items: [] })];
    view.source_errors = {
      "aero-vault": "source.no_projection",
      "aero-im": "source.not_configured",
    };
    view.partial = true;
    const { root } = await mount(view);
    expect(card(root, "aero-vault.buckets").textContent).toContain(
      "本次投影未列出桶引用",
    );
    expect(card(root, "aero-vault.usage").textContent).toContain("待同步");
    expect(card(root, "aero-im.workspaces").textContent).toContain("未接通");
    expect(root.textContent).not.toContain("0 B");
  });
  it("keeps stale/partial reference content visibly historical and separates unknown from denial", async () => {
    const view = resourceView();
    view.partial = true;
    view.stale_datasets = ["aero-vault.buckets"];
    const { root, report } = await mount(view);
    expect(card(root, "aero-vault.buckets").textContent).toContain("陈旧投影");
    expect(card(root, "aero-vault.buckets").textContent).toContain(
      "design-assets",
    );
    report.value.datasets[0]!.authorization = "unknown";
    await vi.waitFor(() =>
      expect(card(root, "aero-vault.usage").textContent).toContain(
        "本地读取资格：未知",
      ),
    );
    expect(card(root, "aero-vault.usage").textContent).toContain("1,048,576 B");
    report.value.datasets[0]!.authorization = "denied";
    await vi.waitFor(() =>
      expect(card(root, "aero-vault.usage").textContent).toContain(
        "读取被拒绝",
      ),
    );
    expect(card(root, "aero-vault.usage").textContent).not.toContain(
      "1,048,576 B",
    );
  });
  it("shows IM not_found as no linked source account, not an empty workspace inventory", async () => {
    const view = resourceView();
    view.snapshots![2]!.data = { status: "not_found" };
    const { root } = await mount(view);
    const im = card(root, "aero-im.workspaces");
    expect(im.textContent).toContain("未关联 IM 账户");
    expect(im.textContent).not.toContain("本次投影未列出工作空间");
  });
  it("does not expand unknown source JSON, including inside visible references or open diagnostics", async () => {
    const view = resourceView();
    view.snapshots![0]!.data = {
      tenant_id: "tenant-demo",
      used_bytes: { token: "DO-NOT-RENDER" },
      max_objects: Number.MAX_SAFE_INTEGER + 1,
      secret: "DO-NOT-RENDER",
      arbitrary: { body: "DO-NOT-RENDER" },
    };
    view.snapshots![2]!.data = {
      items: [
        {
          id: "workspace-demo",
          name: "设计协作",
          slug: "design",
          acl: "DO-NOT-RENDER",
          message_body: "DO-NOT-RENDER",
        },
      ],
    };
    const { root } = await mount(view);
    root.querySelector("details")!.open = true;
    expect(root.textContent).toContain("字段无效（未展示）");
    expect(root.textContent).toContain("来源未提供");
    expect(root.innerHTML).not.toContain("DO-NOT-RENDER");
    expect(card(root, "aero-vault.usage").textContent).not.toContain("0 B");
  });
  it("bounds the visible reference list and preserves source truncation without inventing totals", async () => {
    const view = resourceView();
    view.snapshots![1]!.data = {
      truncated: true,
      items: Array.from({ length: 51 }, (_, index) => `bucket-${index}`),
      total: 9999,
    };
    const { root } = await mount(view);
    const vault = card(root, "aero-vault.buckets");
    expect(vault.querySelectorAll("li")).toHaveLength(50);
    expect(vault.textContent).toContain("页面仅显示前 50 条");
    expect(vault.textContent).toContain("来源投影已截断");
    expect(vault.textContent).toContain("部分投影（已截断）");
    expect(vault.textContent!.indexOf("来源投影已截断")).toBeLessThan(
      vault.textContent!.indexOf("bucket-0"),
    );
    expect(vault.textContent).not.toContain("bucket-50");
    expect(vault.textContent).not.toContain("9999");
  });
  it("rejects malformed lists and wrong source pairs rather than showing a successful empty result", async () => {
    const view = resourceView();
    view.snapshots![1]!.data = { items: ["good", { id: "not-a-bucket-name" }] };
    view.snapshots![2]!.source = "aero-vault";
    const { root } = await mount(view);
    expect(card(root, "aero-vault.buckets").textContent).toContain(
      "引用列表字段无效",
    );
    expect(card(root, "aero-vault.buckets").textContent).not.toContain(
      "本次投影未列出",
    );
    expect(card(root, "aero-im.workspaces").textContent).toContain(
      "摘要字段无效",
    );
    expect(root.textContent).not.toContain("not-a-bucket-name");
    expect(root.textContent).not.toContain("workspace-demo");
  });
  it("lets the user read tenant usage with units, bucket names and workspace references without opening JSON", async () => {
    const { root, fetcher } = await mount();
    expect(root.textContent).toContain("1,048,576 B");
    expect(root.textContent).toContain("12 个对象");
    expect(root.textContent).toContain("租户级用量");
    expect(root.textContent).toContain("design-assets");
    expect(root.textContent).toContain("设计协作");
    expect(root.textContent).toContain("workspace-demo");
    expect(fetcher).toHaveBeenCalledTimes(2);
    for (const [, init] of fetcher.mock.calls) expect(init?.method).toBe("GET");
  });
  it("refreshes the original local resource GET explicitly, not a source command", async () => {
    const { root, fetcher, replace } = await mount();
    const updated = resourceView();
    updated.snapshots![0]!.data = {
      tenant_id: "tenant-demo",
      used_bytes: 0,
      used_objects: 0,
    };
    replace(updated);
    button(root, "刷新资源摘要").click();
    await vi.waitFor(() => expect(root.textContent).toContain("0 B"));
    expect(root.textContent).toContain("0 个对象");
    expect(root.textContent).toContain("来源未提供");
    expect(root.textContent).not.toContain("1,048,576 B");
    expect(fetcher).toHaveBeenCalledTimes(4);
    for (const [url, init] of fetcher.mock.calls) {
      expect(init?.method).toBe("GET");
      expect(new URL(String(url)).pathname).toMatch(
        /account-datasets|me\/resources/,
      );
    }
  });
});
