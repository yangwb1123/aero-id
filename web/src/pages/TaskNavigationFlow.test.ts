import { computed, defineComponent, h } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "../api/aeroId";
import { routeDetails, useHashRoute } from "../router";
import JobsPage from "./JobsPage.vue";
import OperationsPage from "./OperationsPage.vue";
import {
  commandButton,
  commandTestClient,
  chooseCommandDataset,
  flushCommandUI,
  mountCommandTest,
  testAccountID,
  testDatasets,
  testReceiptID,
  unmountCommandTests,
} from "../components/commandTestFixtures";

vi.mock("@iris-ui-kit/vue", async () =>
  (await import("../components/commandTestFixtures")).irisCommandTestStubs(),
);
vi.mock("../components/OperationCreateDialog.vue", () => ({
  default: defineComponent({
    emits: ["created"],
    setup:
      (_, { emit }) =>
      () =>
        h(
          "button",
          { onClick: () => emit("created", { operation_id: testReceiptID }) },
          "采用创建回执事件",
        ),
  }),
}));
const otherID = "01800000-0000-7000-8000-000000000004";
const families = [
  {
    page: "jobs",
    field: "job_id",
    list: "jobs",
    path: "/sync/jobs",
    prop: "jobId",
    button: "查看详情",
    summary: "[data-job-summary]",
  },
  {
    page: "operations",
    field: "operation_id",
    list: "operations",
    path: "/operations",
    prop: "operationId",
    button: "查看时间线",
    summary: "[data-operation-summary]",
  },
] as const;
type Family = (typeof families)[number];
const clients: AeroIdClient[] = [];
beforeEach(() => {
  sessionStorage.clear();
  sessionStorage.setItem(
    "navigation-original-pointer",
    "retained-uuid-hash-marker",
  );
  window.history.replaceState(null, "", "/");
});
afterEach(() => {
  unmountCommandTests();
  clients.splice(0).forEach((client) => client.cancelPendingReads());
  expect(sessionStorage.getItem("navigation-original-pointer")).toBe(
    "retained-uuid-hash-marker",
  );
  window.history.replaceState(null, "", "/");
  vi.restoreAllMocks();
});
function mountFlow(
  family: Family,
  options: {
    listFails?: boolean;
    detailFails?: boolean;
    direct?: boolean;
    held?: Promise<Response>;
  } = {},
) {
  if (options.direct)
    window.history.replaceState(null, "", `#/${family.page}/${testReceiptID}`);
  const requests: {
    path: string;
    method: string | undefined;
    signal: AbortSignal | null | undefined;
  }[] = [];
  const fetcher = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input)).pathname;
      requests.push({ path, method: init?.method, signal: init?.signal });
      if (options.held && path === `/v1${family.path}/${testReceiptID}`)
        return options.held;
      if (init?.method !== "GET") throw new Error("navigation must not write");
      let data: unknown;
      let status = 200;
      if (path === "/v1/account-datasets") data = { datasets: testDatasets };
      else if (path === "/v1/me")
        data = { account: { account_id: testAccountID } };
      else if (path === `/v1${family.path}`) {
        data = {
          [family.list]: [testReceiptID, otherID].map((id) => ({
            [family.field]: id,
            status: "unknown",
          })),
        };
        if (options.listFails) status = 503;
      } else if (path.startsWith("/v1/audit/operations/"))
        data = { operation_id: path.split("/").at(-1), events: [] };
      else if (path.startsWith(`/v1${family.path}/`)) {
        data = {
          [family.field]: path.split("/").at(-1),
          account_id: testAccountID,
          tenant_id: "tenant-a",
          status: "unknown",
          job_type: "sync",
          operation_type: "sync",
          request: { datasets: ["aero-vault.usage"] },
          steps: [],
        };
        if (options.detailFails) status = 404;
      } else throw new Error("unexpected navigation GET");
      return new Response(
        JSON.stringify(
          status === 200
            ? { data }
            : {
                error: {
                  code: "fixture.unavailable",
                  message: "受控读取未完成",
                },
              },
        ),
        { status },
      );
    },
  );
  const client = new AeroIdClient(
    "https://accounts.example.test/v1",
    () => "component-fixture-marker",
    fetcher as typeof fetch,
  );
  clients.push(client);
  const host = defineComponent({
    setup() {
      const { route } = useHashRoute(family.page);
      const target = computed(() => routeDetails(route.value));
      return () =>
        h(family.page === "jobs" ? JobsPage : OperationsPage, {
          client,
          [family.prop]:
            family.page === "jobs"
              ? target.value.jobID
              : target.value.operationID,
        });
    },
  });
  return { ...mountCommandTest(host, {}), requests };
}
async function routeTo(hash: string) {
  window.history.replaceState(null, "", hash);
  window.dispatchEvent(new Event("hashchange"));
  await flushCommandUI();
}
function details(root: HTMLElement, family: Family) {
  return root.querySelector(family.summary);
}
function assertGETs(requests: ReturnType<typeof mountFlow>["requests"]) {
  expect(requests.every((request) => request.method === "GET")).toBe(true);
}

describe.each(families)("$page original-receipt navigation", (family) => {
  it("opens from the original list into a shareable route, then refreshes the exact receipt without a command", async () => {
    const item = mountFlow(family);
    await flushCommandUI();
    commandButton(item.root, family.button).click();
    await flushCommandUI();
    expect(details(item.root, family)).not.toBeNull();
    expect(window.location.hash).toBe(`#/${family.page}/${testReceiptID}`);
    item.app.unmount();
    const refreshed = mountFlow(family);
    await flushCommandUI();
    expect(details(refreshed.root, family)).not.toBeNull();
    expect(
      refreshed.requests.some(
        (request) => request.path === `/v1${family.path}/${testReceiptID}`,
      ),
    ).toBe(true);
    assertGETs([...item.requests, ...refreshed.requests]);
  });
  it("returns from a direct detail to the canonical list so refresh cannot reopen the closed detail", async () => {
    const item = mountFlow(family, { direct: true });
    await flushCommandUI();
    commandButton(
      item.root,
      family.page === "jobs" ? "关闭" : "返回操作列表",
    ).click();
    await flushCommandUI();
    expect(window.location.hash).toBe(`#/${family.page}`);
    expect(details(item.root, family)).toBeNull();
    item.app.unmount();
    const refreshed = mountFlow(family);
    await flushCommandUI();
    expect(details(refreshed.root, family)).toBeNull();
    expect(
      refreshed.requests.some((request) =>
        request.path.startsWith(`/v1${family.path}/`),
      ),
    ).toBe(false);
    assertGETs([...item.requests, ...refreshed.requests]);
  });
  it("keeps original detail readable when its independent list fails, with a working return link", async () => {
    const item = mountFlow(family, { direct: true, listFails: true });
    await flushCommandUI();
    expect(item.root.textContent).toContain("受控读取未完成");
    expect(details(item.root, family)).not.toBeNull();
    const back = item.root.querySelector<HTMLAnchorElement>(
      `nav[data-task-navigation] a[href="#/${family.page}"]`,
    );
    expect(back).not.toBeNull();
    back!.click();
    await flushCommandUI();
    expect(window.location.hash).toBe(`#/${family.page}`);
    expect(details(item.root, family)).toBeNull();
    assertGETs(item.requests);
  });
  it("offers original-link and return navigation even if detail GET is refused or missing", async () => {
    const item = mountFlow(family, { direct: true, detailFails: true });
    await flushCommandUI();
    const nav = item.root.querySelector("nav[data-task-navigation]");
    expect(nav).not.toBeNull();
    expect(
      nav!.querySelector(`a[href="#/${family.page}/${testReceiptID}"]`),
    ).not.toBeNull();
    nav!
      .querySelector<HTMLAnchorElement>(`a[href="#/${family.page}"]`)!
      .click();
    await flushCommandUI();
    expect(window.location.hash).toBe(`#/${family.page}`);
    assertGETs(item.requests);
  });
  it("cancels the original detail read on another route and never adopts its late result", async () => {
    let release!: (response: Response) => void;
    const held = new Promise<Response>((resolve) => {
      release = resolve;
    });
    const item = mountFlow(family, { direct: true, held });
    try {
      await flushCommandUI();
      const original = item.requests.find(
        (request) => request.path === `/v1${family.path}/${testReceiptID}`,
      );
      expect(original?.signal?.aborted).toBe(false);
      await routeTo(`#/${family.page}/${otherID}`);
      expect(original?.signal?.aborted).toBe(true);
      expect(details(item.root, family)).not.toBeNull();
      expect(item.root.textContent).toContain(otherID);
      release(
        new Response(
          JSON.stringify({
            data: {
              [family.field]: testReceiptID,
              status: "completed",
              result: { private_body: "LATE-OLD-RECEIPT-MUST-NOT-RENDER" },
            },
          }),
        ),
      );
      await flushCommandUI();
      expect(item.root.textContent).not.toContain(
        "LATE-OLD-RECEIPT-MUST-NOT-RENDER",
      );
      expect(window.location.hash).toBe(`#/${family.page}/${otherID}`);
      assertGETs(item.requests);
    } finally {
      release(new Response("{}"));
    }
  });
  it("follows back/forward targets and rejects malformed URLs without writes or pointer changes", async () => {
    const item = mountFlow(family, { direct: true });
    await flushCommandUI();
    await routeTo(`#/${family.page}/${otherID}`);
    expect(details(item.root, family)).not.toBeNull();
    expect(item.requests.at(-1)?.path).toBe(
      family.page === "jobs"
        ? `/v1${family.path}/${otherID}`
        : `/v1/audit/operations/${otherID}`,
    );
    expect(item.root.textContent).toContain(otherID);
    await routeTo(`#/${family.page}`);
    expect(details(item.root, family)).toBeNull();
    await routeTo(`#/${family.page}/${testReceiptID}`);
    expect(details(item.root, family)).not.toBeNull();
    expect(item.root.textContent).toContain(testReceiptID);
    await routeTo(`#/${family.page}/not-a-uuid`);
    expect(details(item.root, family)).toBeNull();
    expect(
      item.requests.some((request) => request.path.includes("not-a-uuid")),
    ).toBe(false);
    assertGETs(item.requests);
  });
});
it("adopts a confirmed export receipt into a durable Job route and refreshes without another preparation or execution", async () => {
  function mountCreation() {
    const client = commandTestClient();
    const host = defineComponent({
      setup() {
        const { route } = useHashRoute("jobs");
        return () =>
          h(JobsPage, {
            client: client as unknown as AeroIdClient,
            jobId: routeDetails(route.value).jobID,
          });
      },
    });
    return { ...mountCommandTest(host, {}), client };
  }
  const item = mountCreation();
  await flushCommandUI();
  chooseCommandDataset(item.root, 0);
  await flushCommandUI();
  commandButton(item.root, "创建加密导出").click();
  await vi.waitFor(() =>
    expect(item.client.commands.prepare).toHaveBeenCalledOnce(),
  );
  await flushCommandUI();
  const confirmation = [
    ...item.root.querySelectorAll<HTMLInputElement>("input"),
  ].find((input) => input.type !== "checkbox")!;
  confirmation.value = "确认";
  confirmation.dispatchEvent(new Event("input"));
  await flushCommandUI();
  commandButton(item.root, "确认创建任务 / Operation 回执").click();
  await vi.waitFor(() =>
    expect(window.location.hash).toBe(`#/jobs/${testReceiptID}`),
  );
  await flushCommandUI();
  expect(details(item.root, families[0])).not.toBeNull();
  expect(item.client.commands.execute).toHaveBeenCalledOnce();
  expect(item.client.runOperation).not.toHaveBeenCalled();
  expect(item.client.createExportJob).not.toHaveBeenCalled();
  item.app.unmount();
  const refreshed = mountCreation();
  await flushCommandUI();
  expect(details(refreshed.root, families[0])).not.toBeNull();
  expect(refreshed.client.commands.prepare).not.toHaveBeenCalled();
  expect(refreshed.client.commands.execute).not.toHaveBeenCalled();
  expect(refreshed.client.createExportJob).not.toHaveBeenCalled();
});
it("connects an adopted Operation creation receipt to its canonical original route, without Run", async () => {
  const item = mountFlow(families[1]);
  await flushCommandUI();
  commandButton(item.root, "采用创建回执事件").click();
  await flushCommandUI();
  expect(window.location.hash).toBe(`#/operations/${testReceiptID}`);
  expect(details(item.root, families[1])).not.toBeNull();
  assertGETs(item.requests);
});
