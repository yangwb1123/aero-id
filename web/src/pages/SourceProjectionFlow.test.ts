import { afterEach, describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "../api/aeroId";
import ConnectionsPage from "./ConnectionsPage.vue";
import {
  commandButton,
  flushCommandUI,
  mountCommandTest,
  testAccountID,
  testDatasets,
  unmountCommandTests,
} from "../components/commandTestFixtures";

vi.mock("@iris-ui-kit/vue", async () => {
  const { irisCommandTestStubs } =
    await import("../components/commandTestFixtures");
  const { defineComponent, h } = await import("vue");
  return {
    ...irisCommandTestStubs(),
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
              onChange: (event: Event) =>
                emit(
                  "update:modelValue",
                  [...(event.target as HTMLSelectElement).selectedOptions].map(
                    (item) => item.value,
                  ),
                ),
            },
            props.items.map((item: { value: string; label: string }) =>
              h("option", { value: item.value }, item.label),
            ),
          ),
    }),
  };
});
const nativeScope = "opaque-native-tenant-not-query-scope";
const reference = {
  id: "01800000-0000-7000-8000-000000000011",
  source: "aero-vault",
  source_account_id: "native-account",
  scope_type: "tenant",
  scope_id: nativeScope,
};
const clients: AeroIdClient[] = [];
const roots: HTMLElement[] = [];
afterEach(() => {
  unmountCommandTests();
  roots.splice(0).forEach((root) => root.remove());
  clients.splice(0).forEach((client) => client.cancelPendingReads());
  vi.restoreAllMocks();
});
function flow(
  options: {
    source?: unknown;
    intercept?: (
      url: URL,
      signal?: AbortSignal | null,
    ) => Promise<Response> | undefined;
  } = {},
) {
  const reads: { url: URL; signal?: AbortSignal | null }[] = [];
  let failed = false;
  const client = new AeroIdClient(
    "https://id.example/v1",
    () => "fixture-marker",
    async (input, init) => {
      expect(init?.method).toBe("GET");
      expect(new Headers(init?.headers).has("Idempotency-Key")).toBe(false);
      const url = new URL(String(input));
      reads.push({ url, signal: init?.signal });
      const intercepted = options.intercept?.(url, init?.signal);
      if (intercepted) return intercepted;
      if (url.pathname === "/v1/account-datasets")
        return Response.json({ data: { datasets: testDatasets } });
      if (url.pathname === "/v1/me/data") {
        if (failed)
          return Response.json(
            {
              error: {
                code: "source.unavailable",
                message: "受控投影读取故障",
              },
            },
            { status: 503 },
          );
        return Response.json({
          data: {
            account: { account_id: testAccountID, tenant_id: "tenant-a" },
            consistency: "eventual",
            partial: true,
            snapshots: [
              {
                dataset: "aero-vault.usage",
                source: "aero-vault",
                status: "stale",
                version: 42,
                data: { used_bytes: 1048576, token: "UNAPPROVED-CONTENT" },
              },
            ],
            source_errors: {},
            stale_datasets: ["aero-vault.usage"],
          },
        });
      }
      expect(["/v1/me/sources", "/v1/me/memberships"]).toContain(url.pathname);
      const isSource = url.pathname.endsWith("/sources");
      return Response.json({
        data: {
          [isSource ? "sources" : "memberships"]: [
            {
              ...reference,
              ...(Object.hasOwn(options, "source")
                ? { source: options.source }
                : {}),
              ...(isSource
                ? {}
                : {
                    source: "aero-im",
                    role: "owner",
                    scope_type: "workspace",
                  }),
            },
          ],
          next_cursor: "",
        },
      });
    },
  );
  clients.push(client);
  const mounted = mountCommandTest(ConnectionsPage, { client });
  document.body.append(mounted.root);
  roots.push(mounted.root);
  return {
    ...mounted,
    reads,
    fail: (value: boolean) => {
      failed = value;
    },
  };
}
async function browse(test: ReturnType<typeof flow>) {
  await flushCommandUI();
  const sources = test.root.querySelector<HTMLElement>(
    '[data-reference-list="sources"]',
  )!;
  commandButton(sources, "查看来源范围").click();
  await flushCommandUI();
  commandButton(sources, "选择此来源的数据集").click();
  await flushCommandUI();
  const panel = sources.querySelector<HTMLElement>(
    "[data-source-data-browser]",
  )!;
  expect(panel).not.toBeNull();
  return { sources, panel };
}
async function choose(panel: HTMLElement) {
  const select = panel.querySelector<HTMLSelectElement>("select[multiple]")!;
  select.options[0]!.selected = true;
  select.dispatchEvent(new Event("change", { bubbles: true }));
  await flushCommandUI();
}

describe("source reference to explicitly scoped local projections", () => {
  it("requires explicit nonempty same-source selection and never treats the native reference as an API filter", async () => {
    const test = flow();
    const { panel, sources } = await browse(test);
    expect(test.reads.map((read) => read.url.pathname)).toEqual([
      "/v1/me/sources",
      "/v1/me/memberships",
      "/v1/account-datasets",
    ]);
    expect(panel.textContent).toContain("不是按原生租户、工作空间或桶 ID 过滤");
    expect(
      [...panel.querySelectorAll("option")].map((item) => item.value),
    ).toEqual(["aero-vault.usage"]);
    commandButton(panel, "读取账户数据").click();
    await flushCommandUI();
    expect(panel.textContent).toContain("请显式选择至少一个同源数据集");
    expect(test.reads).toHaveLength(3);
    await choose(panel);
    expect(test.reads).toHaveLength(3);
    commandButton(panel, "读取账户数据").click();
    await flushCommandUI();
    expect(test.reads.at(-1)?.url.pathname).toBe("/v1/me/data");
    expect([...test.reads.at(-1)!.url.searchParams]).toEqual([
      ["dataset", "aero-vault.usage"],
      ["consistency", "eventual"],
    ]);
    expect(panel.textContent).toContain("1,048,576 B");
    expect(panel.textContent).toContain("陈旧");
    expect(test.root.innerHTML).not.toContain("UNAPPROVED-CONTENT");
    expect(test.reads.at(-1)!.url.href).not.toContain(nativeScope);
    const before = test.reads.length;
    commandButton(panel, "返回来源范围").click();
    await flushCommandUI();
    expect(
      sources.querySelector("[data-reference-details]")?.textContent,
    ).toContain(nativeScope);
    expect(document.activeElement).toBe(
      sources.querySelector("[data-reference-details]"),
    );
    expect(test.reads).toHaveLength(before);
    commandButton(sources, "返回来源列表").click();
    await flushCommandUI();
    expect(sources.querySelector("[data-reference-details]")).toBeNull();
    expect(test.reads).toHaveLength(before);
  });

  it("shows a failed projection read without automatically retrying or losing the explicit selection", async () => {
    const test = flow();
    const { panel } = await browse(test);
    await choose(panel);
    test.fail(true);
    commandButton(panel, "读取账户数据").click();
    await flushCommandUI();
    expect(panel.textContent).toContain("受控投影读取故障");
    expect(test.reads).toHaveLength(4);
    test.fail(false);
    commandButton(panel, "读取账户数据").click();
    await flushCommandUI();
    expect(test.reads).toHaveLength(5);
    expect(test.reads.at(-1)!.url.search).toBe(test.reads.at(-2)!.url.search);
    expect(panel.textContent).toContain("1,048,576 B");
  });

  it.each(["denied", "unknown"] as const)(
    "does not offer %s dataset qualification as a same-source read",
    async (authorization) => {
      const test = flow();
      test.report.value!.datasets[0]!.authorization = authorization;
      const { panel } = await browse(test);
      expect(panel.querySelectorAll("option")).toHaveLength(0);
      commandButton(panel, "读取账户数据").click();
      await flushCommandUI();
      expect(test.reads).toHaveLength(3);
      expect(panel.textContent).toContain("没有已核实可读的同源数据集");
    },
  );

  it("may read a permitted historical local projection without enabling its source connector", async () => {
    const test = flow();
    test.report.value!.datasets[0]!.enabled = false;
    const { panel } = await browse(test);
    await choose(panel);
    commandButton(panel, "读取账户数据").click();
    await flushCommandUI();
    expect(test.reads.at(-1)?.url.searchParams.get("consistency")).toBe(
      "eventual",
    );
    expect(panel.textContent).toContain("摘要连接未启用");
    expect(test.reads).toHaveLength(4);
  });

  it("keeps membership owner and unknown sources from manufacturing a source entry", async () => {
    const test = flow({ source: "unregistered-source" });
    await flushCommandUI();
    for (const [kind, action] of [
      ["sources", "查看来源范围"],
      ["memberships", "查看成员范围"],
    ]) {
      const list = test.root.querySelector<HTMLElement>(
        `[data-reference-list="${kind}"]`,
      )!;
      commandButton(list, action!).click();
      await flushCommandUI();
      expect(
        [...list.querySelectorAll("button")].some(
          (button) => button.textContent === "选择此来源的数据集",
        ),
      ).toBe(false);
    }
    expect(test.reads).toHaveLength(2);
  });

  it("cancels an in-flight local GET on return and never adopts its late content or resumes it automatically", async () => {
    let release!: (reply: Response) => void;
    const held = new Promise<Response>((resolve) => {
      release = resolve;
    });
    const test = flow({
      intercept: (url) => (url.pathname === "/v1/me/data" ? held : undefined),
    });
    try {
      const { panel, sources } = await browse(test);
      await choose(panel);
      commandButton(panel, "读取账户数据").click();
      await flushCommandUI();
      const signal = test.reads.at(-1)!.signal;
      expect(signal?.aborted).toBe(false);
      commandButton(panel, "返回来源范围").click();
      await flushCommandUI();
      expect(signal?.aborted).toBe(true);
      expect(sources.querySelector("[data-reference-details]")).not.toBeNull();
    } finally {
      release(
        Response.json({
          data: {
            snapshots: [],
            source_errors: { secret: "LATE-PROJECTION-MUST-NOT-RENDER" },
          },
        }),
      );
      await flushCommandUI();
    }
    expect(test.root.textContent).not.toContain(
      "LATE-PROJECTION-MUST-NOT-RENDER",
    );
    expect(test.reads).toHaveLength(4);
  });

  it("blocks a mismatched current account context even when the reference and dataset appear readable", async () => {
    const test = flow();
    const { panel } = await browse(test);
    await choose(panel);
    test.scope.value = test.scope.value!.replace(
      testAccountID,
      "01800000-0000-7000-8000-000000000099",
    );
    await flushCommandUI();
    commandButton(panel, "读取账户数据").click();
    await flushCommandUI();
    expect(test.reads).toHaveLength(3);
    expect(panel.textContent).toContain("当前来源读取资格未确认");
  });

  it.each(["permission", "scope"])(
    "cancels an in-flight read when %s changes, ignores late content and never restarts it",
    async (change) => {
      let release!: (reply: Response) => void;
      const held = new Promise<Response>((resolve) => {
        release = resolve;
      });
      const test = flow({
        intercept: (url) => (url.pathname === "/v1/me/data" ? held : undefined),
      });
      const { panel } = await browse(test);
      await choose(panel);
      try {
        commandButton(panel, "读取账户数据").click();
        await flushCommandUI();
        const signal = test.reads.at(-1)!.signal;
        const button = commandButton(panel, "读取账户数据");
        if (change === "permission") test.report.value = undefined;
        else {
          const other = "01800000-0000-7000-8000-000000000099";
          test.scope.value = test.scope.value!.replace(testAccountID, other);
          test.report.value!.account_id = other;
        }
        await flushCommandUI();
        expect(signal?.aborted).toBe(true);
        expect(test.reads).toHaveLength(4);
        if (change === "permission") {
          button.click();
          await flushCommandUI();
          expect(test.reads).toHaveLength(4);
        }
      } finally {
        release(
          Response.json({
            data: {
              snapshots: [],
              source_errors: { source: "LATE-SCOPE-MUST-NOT-RENDER" },
            },
          }),
        );
        await flushCommandUI();
      }
      expect(test.root.textContent).not.toContain("LATE-SCOPE-MUST-NOT-RENDER");
      expect(test.reads).toHaveLength(4);
    },
  );
});
