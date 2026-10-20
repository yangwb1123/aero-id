import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "../api/aeroId";
import type { Profile } from "../types";
import {
  commandButton,
  flushCommandUI,
  mountCommandTest,
  testAccountID,
  unmountCommandTests,
} from "../components/commandTestFixtures";
import ProfilePage from "./ProfilePage.vue";

vi.mock("@iris-ui-kit/vue", async () =>
  (await import("../components/commandTestFixtures")).irisCommandTestStubs(),
);
vi.mock("../components/AccountEraseAction.vue", () => ({
  default: { props: ["client", "accountId"], template: "<div />" },
}));

const cleanups: Array<() => void> = [];
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
  display_name: "Unsaved name",
  avatar_url: "https://avatar.example.test/unsaved.png",
  locale: "fr",
  timezone: "Europe/Paris",
};
const padded = {
  display_name: `  ${edited.display_name}  `,
  avatar_url: `  ${edited.avatar_url}  `,
  locale: `  ${edited.locale}  `,
  timezone: `  ${edited.timezone}  `,
};
const ownedFields = [
  "display_name",
  "avatar_url",
  "locale",
  "timezone",
] as const;
const unsavedNotice = "上次提交已保存，仍有未保存的修改。";
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
  return new Response(JSON.stringify({ data }), {
    headers: { "Content-Type": "application/json" },
  });
}
function inputs(root: HTMLElement): HTMLInputElement[] {
  return [...root.querySelectorAll<HTMLInputElement>(".profile-form input")];
}
async function edit(root: HTMLElement, values = edited): Promise<void> {
  inputs(root).forEach((input, index) => {
    input.value = Object.values(values)[index];
    input.dispatchEvent(new Event("input"));
  });
  await flushCommandUI();
}
function submit(root: HTMLElement): void {
  const form = root.querySelector(".profile-form");
  if (!form) throw new Error("missing profile form");
  form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
}
function mountProfile(profile: unknown = original) {
  const writes: Array<{
    body: unknown;
    signal: AbortSignal | null | undefined;
    response: ReturnType<typeof deferred<Response>>;
  }> = [];
  const fetcher = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      expect(new URL(String(input)).pathname).toBe("/v1/me/profile");
      if (init?.method === "GET") return json(profile);
      if (!init) throw new Error("missing PATCH options");
      expect(init.method).toBe("PATCH");
      expect(new Headers(init.headers).get("Idempotency-Key")).toBeNull();
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
    () => "test-token",
    fetcher,
  );
  const commands = [
    vi.spyOn(client.commands, "prepare"),
    vi.spyOn(client.commands, "execute"),
    vi.spyOn(client, "eraseMyAccount"),
  ];
  return {
    ...mountCommandTest(ProfilePage, { client }, { provide: false }),
    client,
    fetcher,
    writes,
    commands,
  };
}
function stored(): Record<string, string | null> {
  return Object.fromEntries(
    Object.keys(sessionStorage).map((key) => [
      key,
      sessionStorage.getItem(key),
    ]),
  );
}

beforeEach(() => {
  sessionStorage.clear();
  sessionStorage.setItem("original-task-pointer", "retained-original-key");
});
afterEach(async () => {
  unmountCommandTests();
  cleanups.splice(0).forEach((cleanup) => cleanup());
  await flushCommandUI();
  vi.restoreAllMocks();
});

describe("Profile explicit save has one in-flight PATCH per mounted form", () => {
  it("reads without auto-saving and submits only the existing four owned text fields", async () => {
    const item = mountProfile();
    await flushCommandUI();
    expect(inputs(item.root).map((input) => input.value)).toEqual([
      original.display_name,
      original.avatar_url,
      original.locale,
      original.timezone,
    ]);
    expect(item.writes).toHaveLength(0);
    expect(item.fetcher).toHaveBeenCalledOnce();
    const before = stored();
    await edit(item.root);
    submit(item.root);
    expect(item.writes).toHaveLength(1);
    expect(item.writes[0].body).toEqual(edited);
    item.writes[0].response.resolve(json({ ...original, ...edited }));
    await flushCommandUI();
    expect(item.root.textContent).toContain("资料已保存");
    expect(stored()).toEqual(before);
    item.commands.forEach((command) => expect(command).not.toHaveBeenCalled());
  });

  it("ignores repeated same-turn submits, including changed draft submits while PATCH is pending", async () => {
    const item = mountProfile();
    await flushCommandUI();
    await edit(item.root);
    const before = stored();
    submit(item.root);
    submit(item.root);
    submit(item.root);
    expect(item.writes).toHaveLength(1);
    await edit(item.root, { ...edited, display_name: "Not queued" });
    submit(item.root);
    await flushCommandUI();
    expect(item.writes).toHaveLength(1);
    expect(item.writes[0].body).toEqual(edited);
    expect(item.root.textContent).not.toContain("资料已保存");
    item.writes[0].response.resolve(json({ ...original, ...edited }));
    await flushCommandUI();
    expect(item.writes).toHaveLength(1);
    expect(inputs(item.root)[0].value).toBe("Not queued");
    expect(item.root.textContent).toContain(unsavedNotice);
    expect(item.root.textContent).not.toContain("资料已保存");
    expect(stored()).toEqual(before);
  });

  it("does not queue suppressed submits; a later explicit save works after success", async () => {
    const item = mountProfile();
    await flushCommandUI();
    submit(item.root);
    submit(item.root);
    expect(item.writes).toHaveLength(1);
    item.writes[0].response.resolve(json(original));
    await flushCommandUI();
    expect(item.writes).toHaveLength(1);
    await edit(item.root);
    submit(item.root);
    submit(item.root);
    expect(item.writes).toHaveLength(2);
    expect(item.writes[1].body).toEqual(edited);
    await flushCommandUI();
    expect(item.root.textContent).not.toContain("资料已保存");
    item.writes[1].response.resolve(json({ ...original, ...edited }));
    await flushCommandUI();
    expect(item.root.textContent).toContain("资料已保存");
    expect(item.writes).toHaveLength(2);
  });

  const failures = [
    {
      name: "current server denial",
      status: 403,
      message: "Profile forbidden",
    },
    {
      name: "audit failure",
      status: 503,
      message: "Profile audit unavailable",
    },
    { name: "invalid response", status: 200, message: "返回了无效响应" },
    { name: "lost acknowledgment", status: 0, message: "无法连接 aero-id" },
  ];
  it.each(failures)(
    "releases the local guard after $name without an automatic write",
    async ({ status, message }) => {
      const item = mountProfile();
      await flushCommandUI();
      await edit(item.root);
      const before = stored();
      submit(item.root);
      submit(item.root);
      expect(item.writes).toHaveLength(1);
      await edit(item.root, newer);
      if (status === 0)
        item.writes[0].response.reject(new TypeError("connection lost"));
      else
        item.writes[0].response.resolve(
          new Response(
            status === 200
              ? "not-json"
              : JSON.stringify({
                  error: { code: "account.profile_update_failed", message },
                }),
            { status },
          ),
        );
      await flushCommandUI();
      expect(item.root.textContent).toContain(message);
      expect(item.root.textContent).not.toContain("资料已保存");
      expect(inputs(item.root).map((input) => input.value)).toEqual(
        Object.values(newer),
      );
      expect(item.root.textContent).not.toContain(unsavedNotice);
      expect(item.writes).toHaveLength(1);
      expect(stored()).toEqual(before);
      submit(item.root);
      submit(item.root);
      expect(item.writes).toHaveLength(2);
      expect(item.writes[1].body).toEqual(newer);
      await flushCommandUI();
      expect(item.root.textContent).not.toContain(message);
      item.writes[1].response.resolve(json({ ...original, ...newer }));
      await flushCommandUI();
      expect(item.root.textContent).toContain("资料已保存");
      expect(item.writes).toHaveLength(2);
      item.commands.forEach((command) =>
        expect(command).not.toHaveBeenCalled(),
      );
    },
  );

  it("keeps the guard until the response body is consumed, not just until headers arrive", async () => {
    const item = mountProfile();
    await flushCommandUI();
    submit(item.root);
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
    await flushCommandUI();
    submit(item.root);
    expect(item.writes).toHaveLength(1);
    expect(item.root.textContent).not.toContain("资料已保存");
    body.enqueue(new TextEncoder().encode(JSON.stringify({ data: original })));
    body.close();
    await flushCommandUI();
    expect(item.root.textContent).toContain("资料已保存");
    expect(item.writes).toHaveLength(1);
  });

  it("preserves explicit empty-string fallback without submitting account or preferences", async () => {
    const empty = {
      account_id: testAccountID,
      preferences: original.preferences,
      display_name: "",
      avatar_url: "",
      locale: "",
      timezone: "",
    };
    const item = mountProfile(empty);
    await flushCommandUI();
    submit(item.root);
    submit(item.root);
    expect(item.writes).toHaveLength(1);
    expect(item.writes[0].body).toEqual({
      display_name: "",
      avatar_url: "",
      locale: "",
      timezone: "",
    });
    item.writes[0].response.resolve(json(empty));
    await flushCommandUI();
    expect(item.root.textContent).toContain("资料已保存");
  });

  it.each(ownedFields)(
    "preserves newer %s while adopting server normalization for untouched fields",
    async (key) => {
      const item = mountProfile();
      await flushCommandUI();
      const before = stored();
      await edit(item.root, padded);
      submit(item.root);
      await edit(item.root, { ...padded, [key]: newer[key] });
      submit(item.root);
      expect(item.writes).toHaveLength(1);
      expect(item.writes[0].body).toEqual(padded);
      item.writes[0].response.resolve(json({ ...original, ...edited }));
      await flushCommandUI();
      const retained = { ...edited, [key]: newer[key] };
      expect(inputs(item.root).map((input) => input.value)).toEqual(
        Object.values(retained),
      );
      expect(item.root.textContent).toContain(unsavedNotice);
      expect(item.root.textContent).not.toContain("资料已保存");
      const notice = [...item.root.querySelectorAll("[tone]")].find(
        (element) => element.textContent?.trim() === unsavedNotice,
      );
      expect(notice?.getAttribute("tone")).toBe("info");
      expect(item.writes).toHaveLength(1);
      expect(stored()).toEqual(before);
      submit(item.root);
      expect(item.writes).toHaveLength(2);
      expect(item.writes[1].body).toEqual(retained);
      item.writes[1].response.resolve(json({ ...original, ...retained }));
      await flushCommandUI();
      expect(item.root.textContent).toContain("资料已保存");
      expect(item.root.textContent).not.toContain(unsavedNotice);
      expect(item.writes).toHaveLength(2);
      expect(stored()).toEqual(before);
      item.commands.forEach((command) =>
        expect(command).not.toHaveBeenCalled(),
      );
    },
  );

  it.each(ownedFields)(
    "preserves an explicit in-flight clear of %s",
    async (key) => {
      const item = mountProfile();
      await flushCommandUI();
      await edit(item.root);
      submit(item.root);
      await edit(item.root, { ...edited, [key]: "" });
      item.writes[0].response.resolve(json({ ...original, ...edited }));
      await flushCommandUI();
      expect(inputs(item.root).map((input) => input.value)).toEqual(
        Object.values({ ...edited, [key]: "" }),
      );
      expect(item.root.textContent).toContain(unsavedNotice);
      expect(item.writes).toHaveLength(1);
    },
  );

  it("compares edits with the submitted snapshot, not the previously loaded profile", async () => {
    const item = mountProfile();
    await flushCommandUI();
    await edit(item.root);
    submit(item.root);
    const reverted = {
      display_name: original.display_name!,
      avatar_url: original.avatar_url!,
      locale: original.locale!,
      timezone: original.timezone!,
    };
    await edit(item.root, reverted);
    item.writes[0].response.resolve(json({ ...original, ...edited }));
    await flushCommandUI();
    expect(inputs(item.root).map((input) => input.value)).toEqual(
      Object.values(reverted),
    );
    expect(item.root.textContent).toContain(unsavedNotice);
    expect(item.writes).toHaveLength(1);
  });

  it("adopts server normalization when edits return to the submitted values", async () => {
    const item = mountProfile();
    await flushCommandUI();
    await edit(item.root, padded);
    submit(item.root);
    await edit(item.root, newer);
    await edit(item.root, padded);
    item.writes[0].response.resolve(json({ ...original, ...edited }));
    await flushCommandUI();
    expect(inputs(item.root).map((input) => input.value)).toEqual(
      Object.values(edited),
    );
    expect(item.root.textContent).toContain("资料已保存");
    expect(item.root.textContent).not.toContain(unsavedNotice);
    expect(item.writes).toHaveLength(1);
  });

  it("retains input changed after response headers while its body is still pending", async () => {
    const item = mountProfile();
    await flushCommandUI();
    await edit(item.root);
    submit(item.root);
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
    await flushCommandUI();
    await edit(item.root, newer);
    submit(item.root);
    expect(item.writes).toHaveLength(1);
    body.enqueue(
      new TextEncoder().encode(
        JSON.stringify({ data: { ...original, ...edited } }),
      ),
    );
    body.close();
    await flushCommandUI();
    expect(inputs(item.root).map((input) => input.value)).toEqual(
      Object.values(newer),
    );
    expect(item.root.textContent).toContain(unsavedNotice);
    expect(item.writes).toHaveLength(1);
  });

  it("does not label subsequent idle edits as fully saved or automatically submit them", async () => {
    const item = mountProfile();
    await flushCommandUI();
    submit(item.root);
    item.writes[0].response.resolve(json(original));
    await flushCommandUI();
    expect(item.root.textContent).toContain("资料已保存");
    await edit(item.root, newer);
    expect(item.root.textContent).toContain(unsavedNotice);
    expect(item.root.textContent).not.toContain("资料已保存");
    expect(item.writes).toHaveLength(1);
  });

  it.each(ownedFields)(
    "rejects an invalid initial %s instead of mounting a saveable form; retry remains GET-only",
    async (key) => {
      const marker = "rejected-profile-read-marker";
      const before = stored();
      const item = mountProfile({ ...original, [key]: { value: marker } });
      await flushCommandUI();
      expect(item.root.textContent).toContain("资料响应无效");
      expect(item.root.textContent).not.toContain(marker);
      expect(item.root.querySelector(".profile-form")).toBeNull();
      expect(item.writes).toHaveLength(0);
      expect(item.fetcher).toHaveBeenCalledOnce();
      item.fetcher.mockResolvedValueOnce(json(original));
      commandButton(item.root, "重试").click();
      await flushCommandUI();
      expect(inputs(item.root).map((input) => input.value)).toEqual(
        ownedFields.map((field) => original[field]),
      );
      expect(item.fetcher).toHaveBeenCalledTimes(2);
      for (const [url, init] of item.fetcher.mock.calls) {
        expect(new URL(String(url)).pathname).toBe("/v1/me/profile");
        expect(init?.method).toBe("GET");
      }
      expect(item.writes).toHaveLength(0);
      expect(stored()).toEqual(before);
      item.commands.forEach((command) =>
        expect(command).not.toHaveBeenCalled(),
      );
    },
  );

  it.each(
    ownedFields.flatMap((key) => [
      { key, laterEdit: false },
      { key, laterEdit: true },
    ]),
  )(
    "rejects the whole PATCH response with invalid $key (later edit: $laterEdit), keeping the current draft",
    async ({ key, laterEdit }) => {
      const item = mountProfile();
      await flushCommandUI();
      const before = stored();
      await edit(item.root);
      submit(item.root);
      const current = laterEdit ? newer : edited;
      if (laterEdit) await edit(item.root, current);
      const marker = "rejected-profile-save-marker";
      item.writes[0].response.resolve(
        json({ ...original, [key]: { value: marker } }),
      );
      await flushCommandUI();
      expect(item.root.textContent).toContain("资料响应无效");
      expect(item.root.textContent).not.toContain(marker);
      expect(item.root.textContent).not.toContain("资料已保存");
      expect(item.root.textContent).not.toContain(unsavedNotice);
      expect(inputs(item.root).map((input) => input.value)).toEqual(
        Object.values(current),
      );
      expect(item.writes).toHaveLength(1);
      expect(item.writes[0].body).toEqual(edited);
      expect(item.fetcher).toHaveBeenCalledTimes(2);
      expect(stored()).toEqual(before);
      item.commands.forEach((command) =>
        expect(command).not.toHaveBeenCalled(),
      );
    },
  );

  it("does not treat read cancellation or unmount as PATCH rollback or change another mounted form", async () => {
    const first = mountProfile();
    await flushCommandUI();
    submit(first.root);
    first.client.cancelPendingReads();
    expect(first.writes[0].signal?.aborted).toBe(false);
    unmountCommandTests();
    expect(first.writes[0].signal?.aborted).toBe(false);
    const second = mountProfile({
      ...original,
      display_name: "New mounted form",
    });
    await flushCommandUI();
    const before = stored();
    first.writes[0].response.resolve(
      json({ ...original, display_name: "Old late reply" }),
    );
    await flushCommandUI();
    expect(first.root.textContent).toBe("");
    expect(inputs(second.root)[0].value).toBe("New mounted form");
    expect(second.root.textContent).not.toContain("资料已保存");
    expect(second.writes).toHaveLength(0);
    submit(second.root);
    expect(second.writes).toHaveLength(1);
    second.writes[0].response.resolve(
      json({ ...original, display_name: "New mounted form" }),
    );
    await flushCommandUI();
    expect(second.root.textContent).toContain("资料已保存");
    expect(stored()).toEqual(before);
  });
});
