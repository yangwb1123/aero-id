import { createApp, defineComponent, h, nextTick, ref } from "vue";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "../api/aeroId";
import AccountDataPanel from "../components/AccountDataPanel.vue";
import {
  commandButton,
  commandTestReport,
  testAccountID,
  testOtherAccountID,
} from "../components/commandTestFixtures";
import { capabilityContextKey } from "../hooks/capabilityContext";
import type { AggregateView, Dataset, JsonRecord } from "../types";
import OverviewPage from "./OverviewPage.vue";

vi.mock("@iris-ui-kit/vue", async () => {
  const { irisCommandTestStubs } =
    await import("../components/commandTestFixtures");
  const { defineComponent, h } = await import("vue");
  return {
    ...irisCommandTestStubs(),
    IrisAlert: defineComponent({
      props: { title: String },
      setup:
        (props, { slots }) =>
        () =>
          h("div", [props.title, slots.default?.()]),
    }),
    IrisSelect: defineComponent({
      props: ["modelValue", "items", "multiple"],
      emits: ["update:modelValue"],
      setup:
        (props, { emit }) =>
        () =>
          h(
            "select",
            {
              multiple: props.multiple !== undefined,
              value: props.modelValue,
              onChange: (event: Event) => {
                const select = event.target as HTMLSelectElement;
                emit(
                  "update:modelValue",
                  select.multiple
                    ? [...select.selectedOptions].map((item) => item.value)
                    : select.value,
                );
              },
            },
            props.items.map((item: { value: string; label: string }) =>
              h("option", { value: item.value }, item.label),
            ),
          ),
    }),
  };
});

const datasets: Dataset[] = [
  { name: "aero-vault.usage", source: "aero-vault", pii: false },
  { name: "aero-vault.buckets", source: "aero-vault", pii: false },
  { name: "aero-im.workspaces", source: "aero-im", pii: false },
  { name: "snaplink.identity", source: "snaplink", pii: true },
];
const sentinel = "UNAPPROVED-SOURCE-JSON-MUST-NOT-RENDER";
function snapshot(dataset: string, data: JsonRecord): JsonRecord {
  return {
    dataset,
    source: dataset.split(".")[0],
    source_region: "local",
    status: "fresh",
    version: 42,
    generated_at: "2026-10-16T09:00:00Z",
    last_synced_at: "2026-10-16T09:01:00Z",
    data,
  };
}
function view(): AggregateView {
  return {
    account: {
      account_id: testAccountID,
      tenant_id: "tenant-a",
      home_region: "local",
    },
    consistency: "eventual",
    partial: false,
    snapshots: [
      snapshot("aero-vault.usage", {
        tenant_id: "tenant-a",
        used_bytes: 1048576,
        secret: sentinel,
      }),
      snapshot("aero-vault.buckets", { items: ["design-assets"] }),
      snapshot("aero-im.workspaces", {
        items: [
          {
            id: "workspace-demo",
            name: "设计协作",
            slug: "design",
            body: sentinel,
          },
        ],
      }),
      snapshot("snaplink.identity", { unknown: { token: sentinel } }),
    ],
    source_errors: {},
    stale_datasets: [],
  };
}
interface RecordedRead {
  url: URL;
  init?: RequestInit;
}
const cleanups: Array<() => void> = [];
afterEach(() => {
  cleanups.splice(0).forEach((cleanup) => cleanup());
  vi.restoreAllMocks();
});
function mount(
  kind: "overview" | "data" = "data",
  adminAccountId?: string,
  initialIntercept?: (read: RecordedRead) => Promise<Response> | undefined,
) {
  let current = view();
  let status = 200;
  let intercept = initialIntercept;
  const reads: RecordedRead[] = [];
  const fetcher = vi.fn<typeof fetch>(async (input, init) => {
    const read = { url: new URL(String(input)), init };
    reads.push(read);
    const pending = intercept?.(read);
    if (pending) return pending;
    let data: unknown = current;
    if (read.url.pathname.endsWith("account-datasets")) data = { datasets };
    if (read.url.pathname === "/v1/me")
      data = { account: current.account, sources: [], memberships: [] };
    return new Response(
      JSON.stringify(
        status === 200
          ? { data }
          : { error: { code: "source.unavailable", message: "读取失败" } },
      ),
      { status, headers: { "Content-Type": "application/json" } },
    );
  });
  const client = new AeroIdClient(
    "https://id.example/v1",
    () => "fixture-only",
    fetcher,
  );
  const report = ref(commandTestReport());
  report.value.datasets = datasets.map((item) => ({
    ...item,
    registered: true,
    enabled: true,
    authorization: "allowed",
    resource_authorization: "unknown",
    status: "fresh",
    fresh: true,
    version: 42,
  }));
  const root = document.createElement("div");
  document.body.append(root);
  const app = createApp(
    defineComponent({
      setup: () => () =>
        kind === "overview"
          ? h(OverviewPage, { client })
          : h(AccountDataPanel, { client, datasets, adminAccountId }),
    }),
  );
  app.provide(capabilityContextKey, () => report.value);
  app.mount(root);
  let mounted = true;
  const unmount = () => {
    if (mounted) {
      app.unmount();
      root.remove();
      mounted = false;
    }
  };
  cleanups.push(unmount);
  return {
    root,
    reads,
    report,
    unmount,
    replace: (value: AggregateView) => {
      current = value;
    },
    reject: () => {
      status = 503;
    },
    allow: () => {
      status = 200;
    },
    intercept: (value: typeof intercept) => {
      intercept = value;
    },
  };
}
async function select(root: HTMLElement, selected: string[]) {
  const input = root.querySelector<HTMLSelectElement>("select[multiple]");
  expect(input).not.toBeNull();
  for (const option of input!.options)
    option.selected = selected.includes(option.value);
  input!.dispatchEvent(new Event("change", { bubbles: true }));
  await nextTick();
}
async function read(root: HTMLElement) {
  commandButton(root, "读取账户数据").click();
  await vi.waitFor(() =>
    expect(root.querySelector('[aria-label="本次读取范围"]')).not.toBeNull(),
  );
}
function card(root: HTMLElement, dataset: string) {
  return root.querySelector(`[data-resource-dataset="${dataset}"]`);
}
function onlyGets(reads: RecordedRead[]) {
  expect(
    reads.every(
      (item) =>
        item.init?.method === "GET" &&
        !item.init?.body &&
        !new Headers(item.init.headers).has("Idempotency-Key"),
    ),
  ).toBe(true);
}

describe("account overview and scoped business data browsing", () => {
  it("opens the overview with tenant usage and references ahead of technical counts, without a data query", async () => {
    const { root, reads } = mount("overview");
    await vi.waitFor(() => expect(root.textContent).toContain("1,048,576 B"));
    expect(root.textContent).toContain("design-assets");
    expect(root.textContent).toContain("设计协作");
    expect(
      root.querySelector('[aria-label="本次投影账户范围"]')?.textContent,
    ).toContain(testAccountID);
    expect(root.textContent).toContain("tenant-a");
    expect(root.textContent).toContain("来源未提供");
    expect(root.textContent).not.toContain(sentinel);
    expect(root.innerHTML.indexOf("data-resource-dataset=")).toBeLessThan(
      root.innerHTML.indexOf("技术统计与来源新鲜度"),
    );
    expect(reads.map((item) => item.url.pathname).sort()).toEqual([
      "/v1/account-datasets",
      "/v1/me",
      "/v1/me/overview",
    ]);
    onlyGets(reads);
  });
  it("explicitly refreshes the same three overview reads and does not query or submit a task", async () => {
    const fixture = mount("overview");
    await vi.waitFor(() =>
      expect(fixture.root.textContent).toContain("1,048,576 B"),
    );
    const next = view();
    next.snapshots![0]!.data = { used_bytes: 0 };
    fixture.replace(next);
    commandButton(fixture.root, "刷新账户概览").click();
    await vi.waitFor(() => expect(fixture.root.textContent).toContain("0 B"));
    expect(fixture.reads).toHaveLength(6);
    expect(
      fixture.reads.filter((item) => item.url.pathname.endsWith("/overview")),
    ).toHaveLength(2);
    onlyGets(fixture.reads);
  });
  it("keeps unknown guidance separate from read denial on the overview", async () => {
    const { root, report } = mount("overview");
    await vi.waitFor(() => expect(root.textContent).toContain("1,048,576 B"));
    report.value.datasets[0]!.authorization = "unknown";
    await nextTick();
    expect(card(root, "aero-vault.usage")?.textContent).toContain(
      "本地读取资格：未知",
    );
    report.value.datasets[0]!.authorization = "denied";
    await nextTick();
    expect(card(root, "aero-vault.usage")?.textContent).toContain("读取被拒绝");
    expect(root.textContent).not.toContain("1,048,576 B");
  });
  it("does not read until explicit action and renders only the submitted dataset scope, not extra returned source JSON", async () => {
    const { root, reads } = mount();
    expect(root.textContent).toContain("尚未读取");
    expect(reads).toHaveLength(0);
    await select(root, ["aero-vault.usage"]);
    expect(reads).toHaveLength(0);
    await read(root);
    expect(card(root, "aero-vault.usage")?.textContent).toContain(
      "1,048,576 B",
    );
    expect(card(root, "aero-vault.buckets")).toBeNull();
    expect(card(root, "aero-im.workspaces")).toBeNull();
    expect(
      root.querySelector('[aria-label="本次读取范围"]')?.textContent,
    ).toContain("aero-vault.usage");
    expect(root.innerHTML).not.toContain(sentinel);
    expect(reads[0]!.url.searchParams.getAll("dataset")).toEqual([
      "aero-vault.usage",
    ]);
    expect(reads[0]!.url.searchParams.get("consistency")).toBe("eventual");
    onlyGets(reads);
  });
  it("distinguishes a selected missing projection, valid empty references and unselected datasets", async () => {
    const fixture = mount();
    const next = view();
    next.snapshots = [snapshot("aero-vault.buckets", { items: [] })];
    next.partial = true;
    next.source_errors = { "aero-im": "source.no_projection" };
    fixture.replace(next);
    await select(fixture.root, ["aero-vault.buckets", "aero-im.workspaces"]);
    await read(fixture.root);
    expect(card(fixture.root, "aero-vault.buckets")?.textContent).toContain(
      "本次投影未列出桶引用",
    );
    expect(card(fixture.root, "aero-im.workspaces")?.textContent).toContain(
      "待同步",
    );
    expect(card(fixture.root, "aero-vault.usage")).toBeNull();
    expect(fixture.root.textContent).not.toContain("0 B");
    expect(fixture.root.textContent).toContain("聚合结果不完整");
  });
  it("shows stale and truncated workspace references with warnings, not fabricated totals", async () => {
    const fixture = mount();
    const next = view();
    next.stale_datasets = ["aero-im.workspaces"];
    next.partial = true;
    next.snapshots![2]!.data = {
      truncated: true,
      items: [{ id: "workspace-demo", name: "设计协作", slug: "design" }],
      total: 9999,
    };
    fixture.replace(next);
    await select(fixture.root, ["aero-im.workspaces"]);
    await read(fixture.root);
    const summary = card(fixture.root, "aero-im.workspaces")!;
    expect(summary.textContent).toContain("陈旧投影");
    expect(summary.textContent).toContain("来源投影已截断");
    expect(summary.textContent).toContain("不是来源总数");
    expect(summary.textContent).not.toContain("9999");
  });
  it("retains blank-as-all compatibility as an explicit read and separates unsupported business views from empty data", async () => {
    const { root, reads } = mount();
    expect(root.textContent).toContain("未选择时");
    await read(root);
    expect(root.querySelectorAll("[data-resource-dataset]")).toHaveLength(3);
    expect(
      root.querySelector('[aria-label="本次读取范围"]')?.textContent,
    ).toContain("全部已注册数据集");
    expect(root.textContent).toContain("暂未提供业务字段视图");
    expect(root.textContent).toContain("snaplink.identity");
    expect(reads[0]!.url.searchParams.getAll("dataset")).toEqual([]);
    for (const details of root.querySelectorAll("details")) details.open = true;
    expect(root.innerHTML).not.toContain(sentinel);
  });
  it("clears adopted scope/results on selection change, but does not automatically reread", async () => {
    const { root, reads } = mount();
    await select(root, ["aero-vault.usage"]);
    await read(root);
    commandButton(root, "清除选择").click();
    await nextTick();
    expect(root.querySelector('[aria-label="本次读取范围"]')).toBeNull();
    expect(root.querySelector("[data-resource-dataset]")).toBeNull();
    expect(root.textContent).toContain("尚未读取");
    expect(reads).toHaveLength(1);
    await select(root, ["aero-im.workspaces"]);
    await read(root);
    expect(card(root, "aero-vault.usage")).toBeNull();
    expect(card(root, "aero-im.workspaces")?.textContent).toContain("设计协作");
    onlyGets(reads);
  });
  it("uses the admin target path/context without borrowing the current caller's self-dataset guidance", async () => {
    const fixture = mount("data", testOtherAccountID);
    const next = view();
    next.account = { ...next.account, account_id: testOtherAccountID };
    fixture.replace(next);
    fixture.report.value.datasets[0]!.authorization = "denied";
    await select(fixture.root, ["aero-vault.usage"]);
    await read(fixture.root);
    expect(fixture.reads[0]!.url.pathname).toBe(
      `/v1/admin/accounts/${testOtherAccountID}/data`,
    );
    expect(
      fixture.root.querySelector('[aria-label="本次投影账户范围"]')
        ?.textContent,
    ).toContain(testOtherAccountID);
    expect(card(fixture.root, "aero-vault.usage")?.textContent).toContain(
      "本地读取资格：未知",
    );
    expect(fixture.root.textContent).not.toContain("本地读取资格：允许");
    onlyGets(fixture.reads);
  });
  it("shows read failure instead of an empty result, and requires a separate explicit GET to recover", async () => {
    const fixture = mount();
    fixture.reject();
    await select(fixture.root, ["aero-vault.usage"]);
    commandButton(fixture.root, "读取账户数据").click();
    await vi.waitFor(() =>
      expect(fixture.root.textContent).toContain("读取失败"),
    );
    expect(fixture.root.querySelector("[data-resource-dataset]")).toBeNull();
    expect(fixture.reads).toHaveLength(1);
    fixture.allow();
    await read(fixture.root);
    expect(fixture.reads).toHaveLength(2);
    expect(fixture.reads[1]!.url.href).toBe(fixture.reads[0]!.url.href);
    onlyGets(fixture.reads);
  });
  it("cancels an in-flight scoped data read when conditions change without adopting late business content", async () => {
    const fixture = mount();
    let complete!: (value: Response) => void;
    fixture.intercept(
      () =>
        new Promise<Response>((resolve) => {
          complete = resolve;
        }),
    );
    await select(fixture.root, ["aero-vault.usage"]);
    commandButton(fixture.root, "读取账户数据").click();
    await vi.waitFor(() => expect(fixture.reads).toHaveLength(1));
    try {
      await select(fixture.root, ["aero-im.workspaces"]);
      expect(fixture.reads[0]!.init?.signal?.aborted).toBe(true);
    } finally {
      complete(new Response(JSON.stringify({ data: view() })));
    }
    await nextTick();
    expect(fixture.root.querySelector("[data-resource-dataset]")).toBeNull();
    expect(fixture.reads).toHaveLength(1);
  });
  it("cancels all three in-flight overview reads on unmount", async () => {
    const completions: Array<(value: Response) => void> = [];
    const fixture = mount(
      "overview",
      undefined,
      () =>
        new Promise<Response>((resolve) => {
          completions.push(resolve);
        }),
    );
    try {
      await vi.waitFor(() => expect(fixture.reads).toHaveLength(3));
      fixture.unmount();
      expect(fixture.reads.every((item) => item.init?.signal?.aborted)).toBe(
        true,
      );
      onlyGets(fixture.reads);
    } finally {
      completions.forEach((complete) =>
        complete(new Response(JSON.stringify({ data: view() }))),
      );
    }
  });
});
