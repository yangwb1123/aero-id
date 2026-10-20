import { computed, defineComponent, h } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "../api/aeroId";
import { routeDetails, useHashRoute } from "../router";
import { taskAuditHref } from "../auditNavigation";
import JobDetails from "../components/JobDetails.vue";
import OperationDetails from "../components/OperationDetails.vue";
import AuditPage from "./AuditPage.vue";
import {
  commandButton,
  commandTestReport,
  flushCommandUI,
  mountCommandTest,
  testAccountID,
  testReceiptID,
  unmountCommandTests,
} from "../components/commandTestFixtures";

vi.mock("@iris-ui-kit/vue", async () => ({
  ...(await import("../components/commandTestFixtures")).irisCommandTestStubs(),
  IrisFormField: defineComponent({
    props: { label: String },
    setup:
      (props, { slots }) =>
      () =>
        h("label", [props.label, slots.default?.()]),
  }),
}));
const requestID = "request:original/part?x=1&marker=%+";
const otherID = "01800000-0000-7000-8000-000000000004";
const families = ["jobs", "operations"] as const;
const clients: AeroIdClient[] = [];
beforeEach(() => {
  sessionStorage.clear();
  sessionStorage.setItem("audit-original-pointer", "retained-uuid-hash");
});
afterEach(() => {
  unmountCommandTests();
  clients.splice(0).forEach((client) => client.cancelPendingReads());
  expect(sessionStorage.getItem("audit-original-pointer")).toBe(
    "retained-uuid-hash",
  );
  window.history.replaceState(null, "", "/");
  vi.restoreAllMocks();
});
function mountFlow(family: (typeof families)[number]) {
  window.history.replaceState(null, "", `#/${family}/${testReceiptID}`);
  const requests: { url: URL; signal: AbortSignal | null | undefined }[] = [];
  let auditStatus = 200;
  let hold: Promise<Response> | undefined;
  const fetcher = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      expect(init?.method).toBe("GET");
      const url = new URL(String(input));
      requests.push({ url, signal: init?.signal });
      if (url.pathname === "/v1/audit/events") {
        if (hold) return hold;
        return new Response(
          JSON.stringify(
            auditStatus === 200
              ? {
                  data: {
                    events: url.searchParams.has("cursor")
                      ? [{ id: otherID, action: "sync.completed" }]
                      : [{ id: testReceiptID, action: "sync.requested" }],
                    next_cursor: url.searchParams.has("cursor")
                      ? ""
                      : "original-audit-cursor",
                  },
                }
              : {
                  error: {
                    code: "auth.forbidden",
                    message: "受控审计读取拒绝",
                  },
                },
          ),
          { status: auditStatus },
        );
      }
      if (url.pathname.startsWith("/v1/audit/operations/"))
        return new Response(
          JSON.stringify({
            data: { operation_id: url.pathname.split("/").at(-1), events: [] },
          }),
        );
      const id = url.pathname.split("/").at(-1);
      return new Response(
        JSON.stringify({
          data: {
            [family === "jobs" ? "job_id" : "operation_id"]: id,
            status: "unknown",
            job_type: "sync",
            operation_type: "sync",
            account_id: testAccountID,
            tenant_id: "tenant-a",
            request_id: requestID,
            request: { datasets: ["aero-vault.usage"] },
            steps: [],
          },
        }),
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
      const { route } = useHashRoute(family);
      const target = computed(() => routeDetails(route.value));
      return () =>
        target.value.page === "audit"
          ? h(AuditPage, {
              client,
              navigation: target.value.auditNavigation,
            })
          : target.value.page === "jobs" && target.value.jobID
            ? h(JobDetails, {
                key: target.value.jobID,
                client,
                jobId: target.value.jobID,
              })
            : target.value.operationID
              ? h(OperationDetails, {
                  key: target.value.operationID,
                  client,
                  operationId: target.value.operationID,
                })
              : h("p", "No target");
    },
  });
  return {
    ...mountCommandTest(host, {}),
    requests,
    auditRequests: () =>
      requests.filter(({ url }) => url.pathname === "/v1/audit/events"),
    setStatus: (status: number) => {
      auditStatus = status;
    },
    setHold: (pending: Promise<Response> | undefined) => {
      hold = pending;
    },
  };
}
async function openAudit(
  item: ReturnType<typeof mountFlow>,
  kind: "operation" | "request",
) {
  await flushCommandUI();
  const link = item.root.querySelector<HTMLAnchorElement>(
    `a[data-audit-link="${kind}"]`,
  );
  expect(
    link,
    "task result offers an exact prefilled audit link",
  ).not.toBeNull();
  link!.click();
  await vi.waitFor(() =>
    expect(item.root.querySelector("[data-task-audit-context]")).not.toBeNull(),
  );
  await flushCommandUI();
}
function input(root: HTMLElement, label: string): HTMLInputElement {
  const field = [...root.querySelectorAll("label")].find((item) =>
    item.textContent?.includes(label),
  );
  if (!field?.querySelector("input"))
    throw new Error(`missing input: ${label}`);
  return field.querySelector("input")!;
}
async function query(item: ReturnType<typeof mountFlow>) {
  item.root
    .querySelector("form")!
    .dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
  await flushCommandUI();
}

describe.each(families)("%s result to audit and back", (family) => {
  it("prefills only the original Request ID, waits for explicit query, keeps applied filters while paging, and returns to the exact receipt", async () => {
    const item = mountFlow(family);
    await openAudit(item, "request");
    expect(input(item.root, "Request ID").value).toBe(requestID);
    expect(input(item.root, "Operation ID").value).toBe("");
    expect(item.auditRequests()).toHaveLength(0);
    await query(item);
    expect(item.auditRequests()).toHaveLength(1);
    expect(item.auditRequests()[0].url.searchParams.get("request_id")).toBe(
      requestID,
    );
    expect(item.auditRequests()[0].url.searchParams.has("operation_id")).toBe(
      false,
    );
    expect(item.auditRequests()[0].url.searchParams.has("job_id")).toBe(false);
    const draft = input(item.root, "Request ID");
    draft.value = "edited-but-not-submitted";
    draft.dispatchEvent(new Event("input"));
    await flushCommandUI();
    commandButton(item.root, "加载更多").click();
    await flushCommandUI();
    expect(item.auditRequests()[1].url.searchParams.get("request_id")).toBe(
      requestID,
    );
    expect(item.auditRequests()[1].url.searchParams.get("cursor")).toBe(
      "original-audit-cursor",
    );
    item.root
      .querySelector<HTMLAnchorElement>(
        `[data-task-audit-context] a[href="#/${family}/${testReceiptID}"]`,
      )!
      .click();
    await vi.waitFor(() =>
      expect(
        item.root.querySelector(
          family === "jobs" ? "[data-job-summary]" : "[data-operation-summary]",
        ),
      ).not.toBeNull(),
    );
    expect(window.location.hash).toBe(`#/${family}/${testReceiptID}`);
    expect(item.auditRequests()).toHaveLength(2);
  });
  it("keeps the prefill and exact return available on audit denial, and explicitly retries the original submitted query", async () => {
    const item = mountFlow(family);
    await openAudit(item, "request");
    item.setStatus(403);
    await query(item);
    expect(item.root.textContent).toContain("受控审计读取拒绝");
    expect(input(item.root, "Request ID").value).toBe(requestID);
    expect(
      item.root.querySelector("[data-task-audit-context] a"),
    ).not.toBeNull();
    expect(item.root.textContent).not.toContain("没有符合条件的审计事实");
    item.setStatus(200);
    commandButton(item.root, "重试").click();
    await flushCommandUI();
    expect(item.auditRequests()).toHaveLength(2);
    expect(item.auditRequests()[1].url.searchParams.get("request_id")).toBe(
      requestID,
    );
  });
});
it("prefills Operation ID separately, never substitutes a source operation ID or adds a Request ID intersection", async () => {
  const item = mountFlow("operations");
  await openAudit(item, "operation");
  expect(input(item.root, "Operation ID").value).toBe(testReceiptID);
  expect(input(item.root, "Request ID").value).toBe("");
  expect(item.auditRequests()).toHaveLength(0);
  await query(item);
  expect(item.auditRequests()[0].url.searchParams.get("operation_id")).toBe(
    testReceiptID,
  );
  expect(item.auditRequests()[0].url.searchParams.has("request_id")).toBe(
    false,
  );
});
it("pauses an explicit query without current read guidance and does not auto-query when guidance returns", async () => {
  const item = mountFlow("jobs");
  await openAudit(item, "request");
  item.report.value = undefined;
  await flushCommandUI();
  expect(commandButton(item.root, "查询").disabled).toBe(true);
  await query(item);
  expect(item.auditRequests()).toHaveLength(0);
  item.report.value = commandTestReport();
  await flushCommandUI();
  expect(commandButton(item.root, "查询").disabled).toBe(false);
  expect(item.auditRequests()).toHaveLength(0);
  await query(item);
  expect(item.auditRequests()).toHaveLength(1);
});
it("cancels an in-flight audit query when returning to the original receipt and ignores its late result", async () => {
  const item = mountFlow("jobs");
  await openAudit(item, "request");
  let release!: (response: Response) => void;
  item.setHold(
    new Promise<Response>((resolve) => {
      release = resolve;
    }),
  );
  try {
    await query(item);
    expect(item.auditRequests()[0].signal?.aborted).toBe(false);
    item.root
      .querySelector<HTMLAnchorElement>("[data-task-audit-context] a")!
      .click();
    await vi.waitFor(() =>
      expect(item.root.querySelector("[data-job-summary]")).not.toBeNull(),
    );
    expect(item.auditRequests()[0].signal?.aborted).toBe(true);
    release(
      new Response(
        JSON.stringify({
          data: { events: [{ action: "LATE-AUDIT-MUST-NOT-RENDER" }] },
        }),
      ),
    );
    await flushCommandUI();
    expect(item.root.textContent).not.toContain("LATE-AUDIT-MUST-NOT-RENDER");
  } finally {
    release(new Response("{}"));
  }
});
it("switching audit context cancels the old query, clears its results and waits for explicit submission of the new filter", async () => {
  const item = mountFlow("operations");
  await openAudit(item, "request");
  let release!: (response: Response) => void;
  item.setHold(
    new Promise<Response>((resolve) => {
      release = resolve;
    }),
  );
  try {
    await query(item);
    const first = item.auditRequests()[0];
    window.history.replaceState(
      null,
      "",
      taskAuditHref("operations", testReceiptID, "operation", testReceiptID)!,
    );
    window.dispatchEvent(new Event("hashchange"));
    await flushCommandUI();
    expect(first.signal?.aborted).toBe(true);
    expect(input(item.root, "Operation ID").value).toBe(testReceiptID);
    expect(input(item.root, "Request ID").value).toBe("");
    expect(item.auditRequests()).toHaveLength(1);
    release(
      new Response(
        JSON.stringify({
          data: { events: [{ action: "OLD-FILTER-MUST-NOT-RENDER" }] },
        }),
      ),
    );
    await flushCommandUI();
    expect(item.root.textContent).not.toContain("OLD-FILTER-MUST-NOT-RENDER");
    expect(item.root.querySelector("[data-applied-audit-filter]")).toBeNull();
    item.setHold(undefined);
    await query(item);
    expect(item.auditRequests()[1].url.searchParams.get("operation_id")).toBe(
      testReceiptID,
    );
    expect(item.auditRequests()[1].url.searchParams.has("request_id")).toBe(
      false,
    );
  } finally {
    release(new Response("{}"));
  }
});
it("reset clears the submitted result without silently querying all audit facts or losing the original return pointer", async () => {
  const item = mountFlow("jobs");
  await openAudit(item, "request");
  await query(item);
  expect(item.auditRequests()).toHaveLength(1);
  commandButton(item.root, "重置").click();
  await flushCommandUI();
  expect(item.auditRequests()).toHaveLength(1);
  expect(input(item.root, "Request ID").value).toBe("");
  expect(item.root.querySelector("[data-applied-audit-filter]")).toBeNull();
  expect(
    item.root.querySelector(
      `[data-task-audit-context] a[href="#/jobs/${testReceiptID}"]`,
    ),
  ).not.toBeNull();
});
