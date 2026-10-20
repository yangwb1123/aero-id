import {
  createApp,
  defineComponent,
  h,
  nextTick,
  reactive,
  ref,
  watch,
  type Component,
} from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AuthState } from "./auth/auth";
import type { PlatformConfig } from "./config";
import type { CapabilityReport, JsonRecord } from "./types";
import {
  commandTestReport,
  testAccountID,
  testReceiptID,
} from "./components/commandTestFixtures";

type Gate = ReturnType<typeof deferred<void>>;
const controls = vi.hoisted(() => ({
  auth: undefined as unknown as AuthState,
  config: undefined as unknown as PlatformConfig,
  report: undefined as unknown as CapabilityReport,
  account: undefined as unknown as JsonRecord,
  readReport: undefined as (() => Promise<CapabilityReport>) | undefined,
  code: undefined as unknown as Gate,
  load: vi.fn(),
  mounts: [] as { client: unknown; id?: string; report?: CapabilityReport }[],
  adoptedIDs: [] as (string | undefined)[],
  clients: [] as {
    getCapabilities: ReturnType<typeof vi.fn>;
    cancelPendingReads: ReturnType<typeof vi.fn>;
  }[],
}));

function placeholder(name: string): Component {
  return defineComponent({
    setup: () => () => h("div", { "data-page": name }, name),
  });
}
vi.mock("./pages/OverviewPage.vue", () => ({
  default: placeholder("overview"),
}));
vi.mock("./pages/ProfilePage.vue", () => ({ default: placeholder("profile") }));
vi.mock("./pages/ConnectionsPage.vue", () => ({
  default: placeholder("connections"),
}));
vi.mock("./pages/ResourcesPage.vue", () => ({
  default: placeholder("resources"),
}));
vi.mock("./pages/DataPage.vue", () => ({ default: placeholder("data") }));
vi.mock("./pages/ActivityPage.vue", () => ({
  default: placeholder("activity"),
}));
vi.mock("./pages/JobsPage.vue", () => ({ default: placeholder("jobs") }));
vi.mock("./pages/AdminAccountsPage.vue", () => ({
  default: placeholder("admin-accounts"),
}));
vi.mock("./pages/AdminGovernancePage.vue", () => ({
  default: placeholder("admin-governance"),
}));
vi.mock("./pages/MigrationPage.vue", () => ({
  default: placeholder("migration"),
}));
vi.mock("./pages/AuditPage.vue", () => ({ default: placeholder("audit") }));
vi.mock("./pages/CompliancePage.vue", () => ({
  default: placeholder("compliance"),
}));
vi.mock("./pages/CallbacksPage.vue", () => ({
  default: placeholder("callbacks"),
}));
vi.mock("./pages/ReplaysPage.vue", () => ({ default: placeholder("replays") }));
vi.mock("./pages/ServiceLinksPage.vue", () => ({
  default: placeholder("services"),
}));
vi.mock("./pages/LoginPage.vue", () => ({ default: placeholder("login") }));
vi.mock("./skin", () => ({ platformSkin: {} }));
vi.mock("@iris-ui-kit/plugin-locale-zh/core", () => ({ localeZhPlugin: {} }));
vi.mock("./auth/auth", () => ({
  authKey: Symbol("test-auth"),
  createAuthState: () => controls.auth,
  useAuth: () => controls.auth,
}));
vi.mock("./config", () => ({ loadPlatformConfig: () => controls.config }));
vi.mock("./api/aeroId", () => ({
  AeroIdClient: class {
    getMe = vi.fn(async () => ({ account: this.account }));
    getCapabilities = vi.fn(
      async () => controls.readReport?.() ?? controls.report,
    );
    cancelPendingReads = vi.fn();
    account = { ...controls.account };
    constructor() {
      controls.clients.push(this);
    }
  },
}));
vi.mock("@iris-ui-kit/vue", () => {
  const wrapper = defineComponent({
    props: { title: String, label: String },
    setup:
      (props, { slots }) =>
      () =>
        h("div", [
          props.title ?? props.label,
          slots.toolbar?.(),
          slots.header?.(),
          slots.default?.(),
          slots.footer?.(),
        ]),
  });
  return {
    IrisProvider: wrapper,
    SkinProvider: wrapper,
    IrisAlert: wrapper,
    IrisAvatar: wrapper,
    IrisBadge: wrapper,
    IrisIcon: wrapper,
    IrisCard: wrapper,
    IrisSpinner: wrapper,
    IrisButton: defineComponent({
      props: { disabled: Boolean },
      emits: ["click"],
      setup:
        (props, { slots, emit }) =>
        () =>
          h(
            "button",
            {
              disabled: props.disabled,
              onClick: () => emit("click"),
            },
            slots.default?.(),
          ),
    }),
    IrisAdminLayout: defineComponent({
      props: ["menus", "activeKey"],
      emits: ["update:activeKey"],
      setup:
        (props, { slots, emit }) =>
        () =>
          h("main", [
            h(
              "nav",
              props.menus.map((item: { key: string; disabled: boolean }) =>
                h(
                  "button",
                  {
                    "data-menu": item.key,
                    disabled: item.disabled,
                    onClick: () => emit("update:activeKey", item.key),
                  },
                  item.key,
                ),
              ),
            ),
            slots.toolbar?.(),
            slots.default?.(),
            slots.footer?.(),
          ]),
    }),
    useSkin: () => ({
      skin: ref({ type: "light" }),
      setMode: vi.fn(),
      setSkin: vi.fn(),
    }),
  };
});

function deferred<T = void>() {
  let yes!: (value: T | PromiseLike<T>) => void;
  let no!: (reason: Error) => void;
  let settled = false;
  const promise = new Promise<T>((resolve, reject) => {
    yes = resolve;
    no = reject;
  });
  return {
    promise,
    get settled() {
      return settled;
    },
    resolve: (value: T | PromiseLike<T>) => {
      settled = true;
      yes(value);
    },
    reject: (reason: Error) => {
      settled = true;
      no(reason);
    },
  };
}
const apps: ReturnType<typeof createApp>[] = [];
const originalKeys = JSON.stringify({ original: "retained-intent-pointer" });
const otherID = "01800000-0000-7000-8000-000000000099";

beforeEach(() => {
  vi.resetModules();
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-10-15T21:00:00Z"));
  controls.auth = reactive({
    status: "authenticated",
    session: {
      accessToken: "test-session",
      expiresAt: Date.now() + 3600000,
      returnTo: "",
      scope: [],
    },
    login: vi.fn(),
    logout: vi.fn(),
  });
  controls.config = {
    snaplinkIssuer: "https://snaplink.example",
    snaplinkClientId: "console",
    aeroIdApiBase: "https://id.example/v1",
    snaplinkScopes: ["openid"],
    snaplinkResource: "aero-id",
    redirectUri: window.location.origin,
    snaplinkHostedLoginUrl: "https://snaplink.example/login/",
  };
  controls.account = { account_id: testAccountID, tenant_id: "tenant-a" };
  controls.report = commandTestReport();
  controls.readReport = undefined;
  controls.code = deferred();
  controls.load.mockClear();
  controls.clients.length = 0;
  controls.mounts.length = 0;
  controls.adoptedIDs.length = 0;
  window.history.replaceState(null, "", "#/overview");
  sessionStorage.clear();
  sessionStorage.setItem("original-preview", originalKeys);
  vi.stubGlobal(
    "fetch",
    vi.fn(() => Promise.reject(new Error("unexpected fetch"))),
  );
  vi.doMock("./pages/OperationsPage.vue", async () => {
    controls.load();
    await controls.code.promise;
    const { useCapabilities } = await import("./hooks/capabilityContext");
    const { useTaskNavigation } = await import("./hooks/useTaskNavigation");
    return {
      __esModule: true,
      default: defineComponent({
        props: { client: Object, operationId: String },
        setup: (props) => {
          const selection = useTaskNavigation(
            "operations",
            () => props.operationId,
          );
          watch(selection.selected, (id) => controls.adoptedIDs.push(id), {
            immediate: true,
          });
          controls.mounts.push({
            client: props.client,
            id: props.operationId,
            report: useCapabilities()?.(),
          });
          return () =>
            h("div", { "data-page": "operations" }, [
              props.operationId,
              h(
                "button",
                {
                  "data-task-select": "",
                  onClick: () => selection.select(otherID),
                },
                "选择另一原回执",
              ),
            ]);
        },
      }),
    };
  });
});
afterEach(async () => {
  apps.splice(0).forEach((app) => app.unmount());
  controls.code.resolve();
  await settle();
  expect(fetch).not.toHaveBeenCalled();
  expect(sessionStorage.getItem("original-preview")).toBe(originalKeys);
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  vi.useRealTimers();
});
async function settle(): Promise<void> {
  if (controls.code.settled) await vi.dynamicImportSettled();
  await vi.advanceTimersByTimeAsync(0);
  for (let i = 0; i < 6; i++) {
    await Promise.resolve();
    await nextTick();
  }
}
async function mount(appBoundary = false) {
  const component = appBoundary
    ? (await import("./App.vue")).default
    : (await import("./AeroShell.vue")).default;
  const root = document.createElement("div");
  const app = createApp(
    component,
    appBoundary ? {} : { config: controls.config },
  );
  const errors: unknown[] = [];
  app.config.errorHandler = (error) => errors.push(error);
  apps.push(app);
  app.mount(root);
  await settle();
  return { root, app, errors };
}
async function navigate(route: string): Promise<void> {
  window.history.replaceState(null, "", `#/${route}`);
  window.dispatchEvent(new Event("hashchange"));
  await settle();
}
function refresh(root: HTMLElement): void {
  [...root.querySelectorAll("button")]
    .find((button) => button.textContent?.includes("刷新当前能力"))!
    .click();
}
function deny(): void {
  controls.report = {
    ...commandTestReport(),
    capabilities: commandTestReport().capabilities.map((item) => ({
      ...item,
      authorization: "denied",
      available: false,
    })),
  };
}

describe("Operation page async loading in the actual shell", () => {
  it("retains all 16 routes and does not load Operation code on another page", async () => {
    const { root } = await mount();
    expect(
      [...root.querySelectorAll("[data-menu]")].map((item) =>
        item.getAttribute("data-menu"),
      ),
    ).toEqual([
      "overview",
      "profile",
      "connections",
      "resources",
      "data",
      "activity",
      "jobs",
      "operations",
      "admin-accounts",
      "admin-governance",
      "migration",
      "audit",
      "compliance",
      "callbacks",
      "replays",
      "services",
    ]);
    expect(root.querySelector('[data-page="overview"]')).not.toBeNull();
    expect(controls.load).not.toHaveBeenCalled();
  });
  it("waits for code only after admission, then passes the exact deep link and client", async () => {
    window.history.replaceState(null, "", `#/operations/${testReceiptID}`);
    const { root, errors } = await mount();
    expect(controls.load).toHaveBeenCalledOnce();
    expect(root.textContent).toContain("正在加载页面代码");
    expect(root.querySelector("[client], [operationid], [error]")).toBeNull();
    expect(controls.mounts).toEqual([]);
    controls.code.resolve();
    await settle();
    expect(errors).toEqual([]);
    expect(controls.mounts).toEqual([
      {
        client: controls.clients[0],
        id: testReceiptID,
        report: controls.report,
      },
    ]);
    expect(root.textContent).not.toContain("正在加载页面代码");
  });
  it.each(["denied", "unknown", "scope mismatch", "outage"])(
    "does not start a loader for a new %s deep link",
    async (reason) => {
      if (reason === "denied") deny();
      if (reason === "unknown") controls.report.capabilities = [];
      if (reason === "scope mismatch")
        controls.account.tenant_id = "other-tenant";
      if (reason === "outage")
        controls.readReport = async () => {
          throw new Error("capability unavailable");
        };
      window.history.replaceState(null, "", `#/operations/${testReceiptID}`);
      const { root } = await mount();
      expect(controls.load).not.toHaveBeenCalled();
      expect(controls.mounts).toEqual([]);
      expect(root.querySelector('[data-page="operations"]')).toBeNull();
    },
  );
  it("uses the latest admitted detail ID if the hash changes while code is pending", async () => {
    window.history.replaceState(null, "", `#/operations/${testReceiptID}`);
    await mount();
    await navigate(`operations/${otherID}`);
    controls.code.resolve();
    await settle();
    expect(controls.load).toHaveBeenCalledOnce();
    expect(controls.mounts.map((item) => item.id)).toEqual([otherID]);
  });
  it("does not mount a late-loaded page after navigation away", async () => {
    window.history.replaceState(null, "", "#/operations");
    const { root } = await mount();
    await navigate("profile");
    controls.code.resolve();
    await settle();
    expect(controls.mounts).toEqual([]);
    expect(root.querySelector('[data-page="profile"]')).not.toBeNull();
  });
  it("does not mount late code after a successful current denial", async () => {
    window.history.replaceState(null, "", "#/operations");
    const { root } = await mount();
    deny();
    refresh(root);
    await settle();
    controls.code.resolve();
    await settle();
    expect(controls.mounts).toEqual([]);
    expect(root.textContent).toContain("当前页面不可用");
  });
  it("preserves only the exact admitted route during checking, without lending its old report", async () => {
    window.history.replaceState(null, "", `#/operations/${testReceiptID}`);
    const { root } = await mount();
    const report = deferred<CapabilityReport>();
    controls.readReport = () => report.promise;
    refresh(root);
    await nextTick();
    controls.code.resolve();
    await settle();
    expect(controls.mounts.map((item) => item.report)).toEqual([undefined]);
    expect(root.textContent).toContain("不沿用旧写入资格");
    await navigate(`operations/${otherID}`);
    expect(root.querySelector('[data-page="operations"]')).toBeNull();
    deny();
    report.resolve(controls.report);
    await settle();
    expect(
      controls.mounts.map((item) => ({
        id: item.id,
        report: item.report?.capabilities[0].authorization,
      })),
    ).toEqual([{ id: testReceiptID, report: undefined }]);
  });
  it("publishes a selected receipt through the actual shell route before adopting the new detail prop", async () => {
    window.history.replaceState(null, "", `#/operations/${testReceiptID}`);
    const { root } = await mount();
    controls.code.resolve();
    await settle();
    root.querySelector<HTMLButtonElement>("[data-task-select]")!.click();
    await settle();
    expect(window.location.hash).toBe(`#/operations/${otherID}`);
    expect(controls.adoptedIDs).toEqual([testReceiptID, otherID]);
    expect(root.querySelector('[data-page="operations"]')).not.toBeNull();
  });
  it("does not locally adopt a new list or receipt selection while only the old exact detail is admitted", async () => {
    window.history.replaceState(null, "", `#/operations/${testReceiptID}`);
    const { root } = await mount();
    controls.code.resolve();
    await settle();
    const report = deferred<CapabilityReport>();
    controls.readReport = () => report.promise;
    refresh(root);
    await settle();
    root.querySelector<HTMLButtonElement>("[data-task-select]")!.click();
    await settle();
    expect(window.location.hash).toBe(`#/operations/${otherID}`);
    expect(controls.adoptedIDs).toEqual([testReceiptID]);
    expect(root.querySelector('[data-page="operations"]')).toBeNull();
    deny();
    report.resolve(controls.report);
    await settle();
    expect(controls.adoptedIDs).toEqual([testReceiptID]);
    expect(root.querySelector('[data-page="operations"]')).toBeNull();
  });
  it("shows a fixed safe error without retrying a rejected code load or clearing original keys", async () => {
    window.history.replaceState(null, "", "#/operations");
    const { root, errors } = await mount();
    const failure = new Error("private chunk location and diagnostic");
    controls.code.reject(failure);
    await settle();
    expect(root.textContent).toContain("页面代码加载失败");
    expect(root.textContent).not.toContain(failure.message);
    expect(root.querySelector("[client], [operationid], [error]")).toBeNull();
    expect(errors).toHaveLength(1);
    expect(errors[0]).toBeInstanceOf(Error);
    await vi.advanceTimersByTimeAsync(30_000);
    await settle();
    expect(controls.load).toHaveBeenCalledOnce();
    expect(controls.mounts).toEqual([]);
  });
  it("bounds the loading indication at 15 seconds but does not claim to cancel the import", async () => {
    window.history.replaceState(null, "", "#/operations");
    const { root } = await mount();
    await vi.advanceTimersByTimeAsync(15_000);
    await settle();
    expect(root.textContent).toContain("页面代码加载失败");
    expect(controls.load).toHaveBeenCalledOnce();
    controls.code.resolve();
    await settle();
    expect(controls.mounts).toHaveLength(1);
  });
  it("still refuses late timeout completion if the route is no longer admitted", async () => {
    window.history.replaceState(null, "", "#/operations");
    const { root } = await mount();
    await vi.advanceTimersByTimeAsync(15_000);
    await settle();
    deny();
    refresh(root);
    await settle();
    controls.code.resolve();
    await settle();
    expect(controls.mounts).toEqual([]);
  });
  it("uses the real App identity remount: old client is canceled and only the new tenant mounts", async () => {
    window.history.replaceState(null, "", `#/operations/${testReceiptID}`);
    const { root, errors } = await mount(true);
    controls.account = { account_id: otherID, tenant_id: "tenant-b" };
    controls.report = {
      ...commandTestReport(),
      account_id: otherID,
      tenant_id: "tenant-b",
    };
    const currentReport = deferred<CapabilityReport>();
    controls.readReport = () => currentReport.promise;
    controls.auth.session = {
      ...controls.auth.session!,
      accessToken: "new-test-session",
    };
    await settle();
    expect(controls.clients).toHaveLength(2);
    expect(controls.clients[0].cancelPendingReads).toHaveBeenCalledOnce();
    controls.code.resolve();
    await settle();
    expect(controls.mounts).toEqual([]);
    currentReport.resolve(controls.report);
    await settle();
    expect(errors).toEqual([]);
    expect(controls.mounts, root.innerHTML).toEqual([
      {
        client: controls.clients[1],
        id: testReceiptID,
        report: controls.report,
      },
    ]);
  });
  it("does not adopt late code after logout at the real App boundary", async () => {
    window.history.replaceState(null, "", "#/operations");
    const { root } = await mount(true);
    controls.auth.session = undefined;
    controls.auth.status = "anonymous";
    await settle();
    controls.code.resolve();
    await settle();
    expect(controls.clients[0].cancelPendingReads).toHaveBeenCalledOnce();
    expect(controls.mounts).toEqual([]);
    expect(root.querySelector('[data-page="login"]')).not.toBeNull();
  });
  it("does not mount after unmount and retains the existing read-cancellation boundary", async () => {
    window.history.replaceState(null, "", "#/operations");
    const { app } = await mount();
    app.unmount();
    controls.code.resolve();
    await settle();
    expect(controls.mounts).toEqual([]);
    expect(controls.clients[0].cancelPendingReads).toHaveBeenCalledOnce();
    apps.splice(apps.indexOf(app), 1);
  });
  it("can revisit cached code only through current navigation admission", async () => {
    window.history.replaceState(null, "", "#/operations");
    const { root } = await mount();
    controls.code.resolve();
    await settle();
    await navigate("profile");
    await navigate(`operations/${testReceiptID}`);
    expect(controls.load).toHaveBeenCalledOnce();
    expect(controls.mounts).toHaveLength(2);
    deny();
    refresh(root);
    await settle();
    await navigate("profile");
    await navigate("operations");
    expect(controls.mounts).toHaveLength(2);
    expect(root.querySelector('[data-page="operations"]')).toBeNull();
  });
});
