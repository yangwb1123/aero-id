import { defineComponent, h, reactive, ref } from "vue";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import AeroShell from "./AeroShell.vue";
import { taskAuditHref } from "./auditNavigation";
import {
  commandTestReport,
  commandButton,
  flushCommandUI,
  mountCommandTest,
  testAccountID,
  testReceiptID,
  unmountCommandTests,
} from "./components/commandTestFixtures";
import type { PlatformConfig } from "./config";
const auth = vi.hoisted(() => ({
  session: { accessToken: "component-fixture-marker" },
  logout: vi.fn(),
}));
vi.mock("./auth/auth", () => ({ useAuth: () => auth }));
vi.mock("@iris-ui-kit/vue", async () => {
  const wrapper = defineComponent({
    setup:
      (_, { slots }) =>
      () =>
        h("main", [slots.toolbar?.(), slots.default?.()]),
  });
  return {
    ...(
      await import("./components/commandTestFixtures")
    ).irisCommandTestStubs(),
    IrisAdminLayout: wrapper,
    IrisAvatar: wrapper,
    IrisIcon: wrapper,
    useSkin: () => ({
      skin: ref({ type: "light" }),
      setMode: vi.fn(),
      setSkin: vi.fn(),
    }),
  };
});
const config = {
  snaplinkIssuer: "https://snaplink.example",
  snaplinkClientId: "console",
  aeroIdApiBase: "https://id.example/v1",
  snaplinkScopes: ["openid"],
  snaplinkResource: "aero-id",
  redirectUri: window.location.origin,
} as PlatformConfig;
beforeEach(() =>
  window.history.replaceState(
    null,
    "",
    taskAuditHref("jobs", testReceiptID, "request", "original-request")!,
  ),
);
afterEach(() => {
  unmountCommandTests();
  window.history.replaceState(null, "", "/");
  vi.unstubAllGlobals();
});
function mountShell(allowed: boolean) {
  const report = reactive(commandTestReport());
  const read = report.capabilities.find((item) => item.name === "audit.read")!;
  read.authorization = allowed ? "allowed" : "denied";
  read.available = allowed;
  const paths: URL[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      expect(init?.method).toBe("GET");
      const url = new URL(String(input));
      paths.push(url);
      const data =
        url.pathname === "/v1/me"
          ? { account: { account_id: testAccountID, tenant_id: "tenant-a" } }
          : url.pathname === "/v1/me/capabilities"
            ? report
            : { events: [] };
      return new Response(JSON.stringify({ data }));
    }),
  );
  return {
    ...mountCommandTest(AeroShell, { config }, { provide: false }),
    paths,
  };
}
it("the actual shell passes an allowed audit deep link to the real filter form, without querying until submit", async () => {
  const item = mountShell(true);
  await flushCommandUI();
  expect(
    item.root.querySelector("[data-task-audit-context]")?.textContent,
  ).toContain("original-request");
  expect(
    item.paths.filter((url) => url.pathname === "/v1/audit/events"),
  ).toHaveLength(0);
  expect(commandButton(item.root, "查询").disabled).toBe(false);
  item.root
    .querySelector("form")!
    .dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
  await flushCommandUI();
  expect(item.paths.at(-1)?.searchParams.get("request_id")).toBe(
    "original-request",
  );
});
it("an original task link does not borrow operation/account read permission when the actual shell denies audit.read", async () => {
  const item = mountShell(false);
  await flushCommandUI();
  expect(item.root.querySelector("[data-task-audit-context]")).toBeNull();
  expect(item.root.querySelector("form")).toBeNull();
  expect(
    item.paths.filter((url) => url.pathname.startsWith("/v1/audit/")),
  ).toHaveLength(0);
});
