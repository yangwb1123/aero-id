import { nextTick } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "../api/aeroId";
import type { AuditHoldScope } from "../api/compliance";
import type { JsonRecord } from "../types";
import {
  commandButton,
  mountCommandTest,
  unmountCommandTests,
} from "../components/commandTestFixtures";
import CompliancePage from "./CompliancePage.vue";

let retainedConfirm: (() => void) | undefined;
const retainedRemovals: Array<() => void> = [];
vi.mock("@iris-ui-kit/vue", async () => {
  const { defineComponent, getCurrentInstance, h } = await import("vue");
  const { irisCommandTestStubs } =
    await import("../components/commandTestFixtures");
  return {
    ...irisCommandTestStubs(),
    IrisButton: defineComponent({
      props: { disabled: Boolean, type: String },
      emits: ["click"],
      setup(props, { slots, emit }) {
        const listener = getCurrentInstance()?.vnode.props?.onClick;
        let captured = false;
        return () => {
          const content = slots.default?.();
          if (
            !captured &&
            typeof listener === "function" &&
            content?.some((node) => node.children === "解除")
          ) {
            // Keep the real page listener, including after its row changes.
            retainedRemovals.push(() => listener());
            captured = true;
          }
          return h(
            "button",
            {
              type: props.type,
              disabled: props.disabled,
              onClick: () => emit("click"),
            },
            content,
          );
        };
      },
    }),
    IrisDialog: defineComponent({
      props: { open: Boolean },
      setup(props, { slots }) {
        const parent = getCurrentInstance()?.parent?.vnode.props;
        const compliance = parent && Object.hasOwn(parent, "command");
        if (compliance && typeof parent.onConfirm === "function") {
          const listener = parent.onConfirm;
          retainedConfirm = () => listener();
        }
        return () =>
          props.open
            ? h(
                "div",
                compliance ? { "data-compliance-dialog": "" } : {},
                slots.default?.(),
              )
            : null;
      },
    }),
  };
});

const scopes: AuditHoldScope[] = ["tenant", "account", "actor"];
const invalidIDs = [
  { name: "missing", value: undefined },
  { name: "null", value: null },
  { name: "true", value: true },
  { name: "false", value: false },
  { name: "number", value: 17 },
  { name: "zero", value: 0 },
  { name: "array", value: ["original-target"] },
  { name: "nested array", value: [["original-target"]] },
  { name: "empty array", value: [] },
  { name: "object", value: { id: "original-target" } },
  { name: "empty string", value: "" },
];
const cleanups: Array<() => void> = [];
function row(scopeType: unknown, scopeID: unknown): JsonRecord {
  return {
    scope_type: scopeType,
    scope_id: scopeID,
    reason: "preserve-original-evidence",
    created_by: "operator",
    created_at: "2026-10-16T01:00:00Z",
    expires_at: null,
  };
}
function json(data: unknown): Response {
  return new Response(JSON.stringify({ data }));
}
async function settleUI(): Promise<void> {
  await vi.advanceTimersByTimeAsync(0);
  await nextTick();
}
function storageSnapshot(): Record<string, string | null> {
  return Object.fromEntries(
    Object.keys(sessionStorage).map((key) => [
      key,
      sessionStorage.getItem(key),
    ]),
  );
}
function mountHolds(rows: JsonRecord[]) {
  const reads: URL[] = [];
  const writes: Array<{ url: URL; init: RequestInit }> = [];
  const fetcher = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input));
      if (init?.method === "GET") {
        expect(url.pathname).toBe("/v1/admin/audit/holds");
        expect(url.search).toBe("");
        expect(init.body).toBeUndefined();
        expect(new Headers(init.headers).get("Idempotency-Key")).toBeNull();
        reads.push(url);
        return json({ holds: rows });
      }
      if (!init) throw new Error("missing original write options");
      writes.push({ url, init });
      return new Promise<Response>((_resolve, reject) => {
        cleanups.push(() => reject(new Error("test request cleanup")));
      });
    },
  );
  const client = new AeroIdClient(
    "https://accounts.example.test/v1",
    () => "test-session",
    fetcher,
  );
  const list = vi.spyOn(client.compliance, "listHolds");
  const remove = vi.spyOn(client.compliance, "removeHold");
  const otherCalls = [
    vi.spyOn(client.compliance, "putHold"),
    vi.spyOn(client.compliance, "createAuditExport"),
    vi.spyOn(client.compliance, "runAuditAnchors"),
    vi.spyOn(client.compliance, "getAuditAnchorManifest"),
    vi.spyOn(client.commands, "prepare"),
    vi.spyOn(client.commands, "execute"),
    vi.spyOn(client.commands, "abandon"),
    vi.spyOn(client, "runOperation"),
    vi.spyOn(client, "reconcileOperation"),
    vi.spyOn(client, "downloadExport"),
  ];
  return {
    ...mountCommandTest(CompliancePage, { client }, { provide: false }),
    client,
    list,
    remove,
    otherCalls,
    reads,
    writes,
    fetcher,
    stored: storageSnapshot(),
  };
}
type MountedHolds = ReturnType<typeof mountHolds>;
function dialog(item: MountedHolds): HTMLElement | null {
  return item.root.querySelector("[data-compliance-dialog]");
}
async function fill(item: MountedHolds, value: string): Promise<void> {
  const input = dialog(item)?.querySelector("input");
  if (!input) throw new Error("missing original Scope ID confirmation input");
  input.value = value;
  input.dispatchEvent(new Event("input"));
  await settleUI();
}
function expectCalls(item: MountedHolds, count = 0): void {
  expect(item.reads).toHaveLength(1);
  expect(item.remove).toHaveBeenCalledTimes(count);
  expect(item.writes).toHaveLength(count);
  expect(item.fetcher).toHaveBeenCalledTimes(1 + count);
  for (const call of item.otherCalls) expect(call).not.toHaveBeenCalled();
  expect(storageSnapshot()).toEqual(item.stored);
}
async function confirmIfOffered(item: MountedHolds, scopeID: unknown) {
  const first = item.root.querySelector("tbody tr");
  const button = first?.querySelector("button");
  if (button) {
    button.click();
    await settleUI();
    await fill(item, String(scopeID ?? ""));
    retainedConfirm?.();
    retainedConfirm?.();
    await settleUI();
  }
  return button;
}

beforeEach(() => {
  retainedConfirm = undefined;
  retainedRemovals.length = 0;
  sessionStorage.clear();
  sessionStorage.setItem("original-recovery-pointer", "retained-original-key");
  vi.useFakeTimers();
});
afterEach(async () => {
  unmountCommandTests();
  cleanups.splice(0).forEach((cleanup) => cleanup());
  await settleUI();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe.each(scopes)(
  "Hold release uses an exact %s target string",
  (scope) => {
    it.each(invalidIDs)(
      "does not offer or coerce $name into a removal target",
      async ({ value }) => {
        const item = mountHolds([
          row(scope, value),
          row("actor", "valid-target"),
        ]);
        await settleUI();
        const button = await confirmIfOffered(item, value);
        // On the old implementation this exercises the actual coerced DELETE
        // through the real client/transport seam, not just an enabled button.
        expectCalls(item);
        expect(button).toBeNull();
        expect(dialog(item)).toBeNull();
        expect(item.root.querySelectorAll("tbody tr")).toHaveLength(2);
        expect(item.root.querySelectorAll("tbody tr button")).toHaveLength(1);
        expect(item.root.textContent).not.toContain("Legal Hold 已解除");
        retainedConfirm?.();
        await vi.advanceTimersByTimeAsync(60_000);
        expectCalls(item);
      },
    );

    it.each([
      "17",
      "0",
      "false",
      "[object Object]",
      "Opaque Target:Alpha/百分比%?",
    ])(
      "preserves opaque string %s without normalization or automatic execution",
      async (scopeID) => {
        const item = mountHolds([row(scope, scopeID)]);
        await settleUI();
        commandButton(item.root, "解除").click();
        await settleUI();
        expect(dialog(item)?.textContent).toContain(`${scope}:${scopeID}`);
        expectCalls(item);
        await fill(item, `${scopeID} `);
        retainedConfirm?.();
        await settleUI();
        expectCalls(item);
        await fill(item, scopeID);
        expectCalls(item);
        retainedConfirm?.();
        retainedConfirm?.();
        expectCalls(item, 1);
        expect(item.remove).toHaveBeenCalledWith(scope, scopeID);
        expect(item.writes[0].url.pathname).toBe(
          `/v1/admin/audit/holds/${scope}/${encodeURIComponent(scopeID)}`,
        );
        expect(item.writes[0].url.search).toBe("");
        expect(item.writes[0].init.method).toBe("DELETE");
        expect(item.writes[0].init.body).toBeUndefined();
        expect(
          new Headers(item.writes[0].init.headers).get("Idempotency-Key"),
        ).toBeNull();
      },
    );
  },
);

describe("Hold target admission is local and does not reinterpret metadata", () => {
  it("refuses a changed row before replacing an existing selection or exact input", async () => {
    const item = mountHolds([
      row("account", "original-target"),
      row("actor", "other-target"),
    ]);
    await settleUI();
    expect(retainedRemovals).toHaveLength(2);
    retainedRemovals[0]();
    await settleUI();
    await fill(item, "original-target");
    const originalRows = await item.list.mock.results[0].value;
    originalRows[1].scope_id = ["other-target"];
    retainedRemovals[1]();
    await settleUI();
    expectCalls(item);
    expect(dialog(item)?.textContent).toContain("account:original-target");
    expect(dialog(item)?.querySelector("input")?.value).toBe("original-target");
    expect(commandButton(item.root, "确认解除保全").disabled).toBe(false);
  });

  it.each([
    { scopeType: "Account" },
    { scopeType: "unknown" },
    { scopeType: ["account"] },
    { scopeType: { value: "account" } },
    { scopeType: null },
  ])(
    "retains existing refusal of unsupported scope $scopeType",
    async ({ scopeType }) => {
      const item = mountHolds([row(scopeType, "original-target")]);
      await settleUI();
      expect(item.root.querySelector("tbody tr button")).toBeNull();
      retainedConfirm?.();
      await settleUI();
      expectCalls(item);
      expect(dialog(item)).toBeNull();
    },
  );

  it.each([
    { value: 17 },
    { value: ["original-target"] },
    { value: { id: "original-target" } },
  ])(
    "rechecks a retained real row listener without coercing changed target $value",
    async ({ value }) => {
      const item = mountHolds([row("account", "original-target")]);
      await settleUI();
      expect(retainedRemovals).toHaveLength(1);
      const select = retainedRemovals[0];
      const originalRows = await item.list.mock.results[0].value;
      originalRows[0].scope_id = value;
      select();
      await settleUI();
      if (dialog(item)) {
        await fill(item, String(value));
        retainedConfirm?.();
        await settleUI();
      }
      expectCalls(item);
      expect(dialog(item)).toBeNull();
    },
  );
});
