import { nextTick } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "../api/aeroId";
import type { Profile } from "../types";
import {
  mountCommandTest,
  testAccountID,
  unmountCommandTests,
} from "../components/commandTestFixtures";
import ProfilePage from "./ProfilePage.vue";

let retainedSubmit: (() => void) | undefined;
vi.mock("@iris-ui-kit/vue", async () => {
  const { defineComponent, h } = await import("vue");
  const { irisCommandTestStubs } =
    await import("../components/commandTestFixtures");
  return {
    ...irisCommandTestStubs(),
    IrisCard: defineComponent({
      setup:
        (_, { slots }) =>
        () => {
          const children = slots.default?.();
          const form = children?.find((child) => child.type === "form");
          const listener = form?.props?.onSubmit;
          if (typeof listener === "function")
            retainedSubmit = () =>
              listener(new Event("submit", { cancelable: true }));
          return h("div", children);
        },
    }),
  };
});
vi.mock("../components/AccountEraseAction.vue", () => ({
  default: { props: ["client", "accountId"], template: "<div />" },
}));

const original: Profile = {
  account_id: testAccountID,
  display_name: "Original name",
  avatar_url: "https://avatar.example.test/original.png",
  locale: "zh-CN",
  timezone: "UTC",
  preferences: { theme: "dark" },
};
const edited = {
  display_name: "Explicit name",
  avatar_url: "https://avatar.example.test/edited.png",
  locale: "en",
  timezone: "Asia/Shanghai",
};
const newer = {
  display_name: "New mounted draft",
  avatar_url: "https://avatar.example.test/newer.png",
  locale: "fr",
  timezone: "Europe/Paris",
};
const empty = { display_name: "", avatar_url: "", locale: "", timezone: "" };
const cleanups: Array<() => void> = [];
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}
function json(data: unknown): Response {
  return new Response(JSON.stringify({ data }));
}
async function settleUI(): Promise<void> {
  await vi.advanceTimersByTimeAsync(0);
  await nextTick();
}
function inputs(root: HTMLElement): HTMLInputElement[] {
  return [...root.querySelectorAll<HTMLInputElement>(".profile-form input")];
}
async function edit(root: HTMLElement, values = edited): Promise<void> {
  inputs(root).forEach((input, index) => {
    input.value = Object.values(values)[index];
    input.dispatchEvent(new Event("input"));
  });
  await settleUI();
}
function storageSnapshot(): Record<string, string | null> {
  return Object.fromEntries(
    Object.keys(sessionStorage).map((key) => [
      key,
      sessionStorage.getItem(key),
    ]),
  );
}
function mountProfile(profile: Profile = original) {
  let token = "original-test-session";
  const writes: Array<{
    body: unknown;
    signal: AbortSignal | null | undefined;
    response: ReturnType<typeof deferred<Response>>;
  }> = [];
  const fetcher = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      expect(new URL(String(input)).pathname).toBe("/v1/me/profile");
      expect(new Headers(init?.headers).get("Idempotency-Key")).toBeNull();
      if (init?.method === "GET") return json(profile);
      if (!init) throw new Error("missing Profile PATCH options");
      expect(init.method).toBe("PATCH");
      const response = deferred<Response>();
      cleanups.push(() => response.reject(new Error("test request cleanup")));
      writes.push({
        body: JSON.parse(String(init.body)),
        signal: init.signal,
        response,
      });
      return response.promise;
    },
  );
  const client = new AeroIdClient(
    "https://accounts.example.test/v1",
    () => token,
    fetcher,
  );
  const update = vi.spyOn(client, "updateProfile");
  const otherWrites = [
    vi.spyOn(client.commands, "prepare"),
    vi.spyOn(client.commands, "execute"),
    vi.spyOn(client.commands, "abandon"),
    vi.spyOn(client, "eraseMyAccount"),
    vi.spyOn(client, "createOperation"),
    vi.spyOn(client, "runOperation"),
    vi.spyOn(client, "reconcileSyncJob"),
    vi.spyOn(client, "downloadExport"),
  ];
  return {
    ...mountCommandTest(ProfilePage, { client }, { provide: false }),
    client,
    update,
    fetcher,
    writes,
    otherWrites,
    rotateSession: () => {
      token = "replacement-test-session";
    },
  };
}
type MountedProfile = ReturnType<typeof mountProfile>;
async function captureSubmit(item: MountedProfile, values = edited) {
  await settleUI();
  await edit(item.root, values);
  expect(retainedSubmit).toBeTypeOf("function");
  return retainedSubmit!;
}
function expectUnchanged(
  item: MountedProfile,
  writes: number,
  stored: ReturnType<typeof storageSnapshot>,
): void {
  expect(item.update).toHaveBeenCalledTimes(writes);
  expect(item.writes).toHaveLength(writes);
  expect(item.fetcher).toHaveBeenCalledTimes(1 + writes);
  for (const write of item.otherWrites) expect(write).not.toHaveBeenCalled();
  expect(storageSnapshot()).toEqual(stored);
}
const outcomes = [
  "success",
  "denied",
  "audit unavailable",
  "invalid shape",
  "lost acknowledgment",
] as const;
function settleWrite(item: MountedProfile, outcome: (typeof outcomes)[number]) {
  const write = item.writes[0];
  if (outcome === "lost acknowledgment") {
    write.response.reject(new TypeError("test acknowledgment lost"));
    return;
  }
  if (outcome === "denied" || outcome === "audit unavailable") {
    write.response.resolve(
      new Response(
        JSON.stringify({
          error: {
            code: "account.profile_update_failed",
            message: "Profile save unavailable",
          },
        }),
        { status: outcome === "denied" ? 403 : 503 },
      ),
    );
    return;
  }
  write.response.resolve(
    json(
      outcome === "success"
        ? { ...original, ...edited }
        : { ...original, locale: ["bad-profile-marker"] },
    ),
  );
}

beforeEach(() => {
  retainedSubmit = undefined;
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

describe("Profile submit admission belongs to the mounted form", () => {
  it.each([
    { name: "edited", values: edited },
    { name: "empty", values: empty },
  ])(
    "refuses a captured $name submit after unmount, before calling the client",
    async ({ values }) => {
      const item = mountProfile();
      const submit = await captureSubmit(item, values);
      const stored = storageSnapshot();
      unmountCommandTests();
      submit();
      submit();
      await settleUI();
      expectUnchanged(item, 0, stored);
      expect(item.root.textContent).toBe("");
    },
  );

  it.each(outcomes)(
    "does not admit another old submit after a mounted %s finishes and the form unmounts",
    async (outcome) => {
      const item = mountProfile();
      const submit = await captureSubmit(item);
      const stored = storageSnapshot();
      submit();
      expectUnchanged(item, 1, stored);
      settleWrite(item, outcome);
      await settleUI();
      unmountCommandTests();
      submit();
      await settleUI();
      expectUnchanged(item, 1, stored);
    },
  );

  it.each(["same session", "replacement session"])(
    "does not lend a fresh same-account form or %s to an old listener",
    async (session) => {
      const previous = mountProfile();
      const oldSubmit = await captureSubmit(previous);
      const stored = storageSnapshot();
      unmountCommandTests();
      if (session === "replacement session") previous.rotateSession();
      const current = mountProfile();
      const submit = await captureSubmit(current, newer);
      oldSubmit();
      await settleUI();
      expectUnchanged(previous, 0, stored);
      expectUnchanged(current, 0, stored);
      expect(inputs(current.root).map((input) => input.value)).toEqual(
        Object.values(newer),
      );
      submit();
      expectUnchanged(current, 1, stored);
      expect(current.writes[0].body).toEqual(newer);
      current.writes[0].response.resolve(json({ ...original, ...newer }));
      await settleUI();
      expect(current.root.textContent).toContain("资料已保存");
      expectUnchanged(previous, 0, stored);
    },
  );

  it.each(outcomes)(
    "keeps an admitted PATCH independent of unmount and refuses old submits after its late %s",
    async (outcome) => {
      const previous = mountProfile();
      const oldSubmit = await captureSubmit(previous);
      const stored = storageSnapshot();
      oldSubmit();
      previous.client.cancelPendingReads();
      unmountCommandTests();
      expect(previous.writes[0].signal?.aborted).toBe(false);
      const current = mountProfile();
      await captureSubmit(current, newer);
      oldSubmit();
      expectUnchanged(previous, 1, stored);
      settleWrite(previous, outcome);
      await settleUI();
      expect(previous.writes[0].signal?.aborted).toBe(false);
      oldSubmit();
      await settleUI();
      expectUnchanged(previous, 1, stored);
      expectUnchanged(current, 0, stored);
      expect(previous.writes[0].body).toEqual(edited);
      expect(inputs(current.root).map((input) => input.value)).toEqual(
        Object.values(newer),
      );
      expect(current.root.textContent).not.toContain("资料已保存");
      expect(current.root.textContent).not.toContain(
        "Profile save unavailable",
      );
      expect(current.root.textContent).not.toContain("bad-profile-marker");
    },
  );

  it("does not abort an admitted response body on unmount or reopen old admission when it finishes", async () => {
    const item = mountProfile();
    const submit = await captureSubmit(item);
    const stored = storageSnapshot();
    submit();
    let body!: ReadableStreamDefaultController<Uint8Array>;
    item.writes[0].response.resolve(
      new Response(
        new ReadableStream<Uint8Array>({
          start(controller) {
            body = controller;
          },
        }),
      ),
    );
    cleanups.push(() => body.error(new Error("test stream cleanup")));
    await settleUI();
    submit();
    expectUnchanged(item, 1, stored);
    unmountCommandTests();
    expect(item.writes[0].signal?.aborted).toBe(false);
    submit();
    expectUnchanged(item, 1, stored);
    body.enqueue(
      new TextEncoder().encode(
        JSON.stringify({ data: { ...original, ...edited } }),
      ),
    );
    body.close();
    await settleUI();
    submit();
    await settleUI();
    expectUnchanged(item, 1, stored);
  });

  it.each([
    { name: "edited", values: edited },
    { name: "empty", values: empty },
  ])(
    "still admits one explicit mounted $name save without queueing repeated submits",
    async ({ values }) => {
      const item = mountProfile();
      const submit = await captureSubmit(item, values);
      const stored = storageSnapshot();
      expectUnchanged(item, 0, stored);
      submit();
      submit();
      await settleUI();
      expectUnchanged(item, 1, stored);
      expect(item.writes[0].body).toEqual(values);
      item.writes[0].response.resolve(json({ ...original, ...values }));
      await settleUI();
      expect(item.root.textContent).toContain("资料已保存");
      expectUnchanged(item, 1, stored);
    },
  );
});
