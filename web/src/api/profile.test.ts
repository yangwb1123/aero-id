import { describe, expect, it, vi } from "vitest";
import { AeroIdClient, AeroIdError } from "./aeroId";

const fields = ["display_name", "avatar_url", "locale", "timezone"] as const;
const profile = {
  account_id: "01800000-0000-7000-8000-000000000002",
  display_name: "资料 name",
  avatar_url: "https://avatar.example.test/picture.png",
  locale: "zh-CN",
  timezone: "UTC",
  preferences: { theme: "dark" },
};
const submitted = {
  display_name: "Explicit draft",
  avatar_url: "",
  locale: "en",
  timezone: "Asia/Shanghai",
};
const marker = "rejected-profile-content-marker";
const invalidValues = [
  { name: "missing", value: undefined },
  { name: "null", value: null },
  { name: "number", value: 7 },
  { name: "boolean", value: false },
  { name: "array", value: [marker] },
  { name: "object", value: { text: marker } },
];
function json(data: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify({ data }), {
    ...init,
    headers: { "Content-Type": "application/json", ...init.headers },
  });
}
function clientWith(fetcher: typeof fetch): AeroIdClient {
  return new AeroIdClient(
    "https://accounts.example.test/v1",
    () => "test-token",
    fetcher,
  );
}
function request(client: AeroIdClient, method: string) {
  return method === "GET"
    ? client.getProfile()
    : client.updateProfile(submitted);
}

describe.each(["GET", "PATCH"])(
  "Profile %s response text contract",
  (method) => {
    const invalidFields = fields.flatMap((field) =>
      invalidValues.map(({ name, value }) => ({
        name: `${name} ${field}`,
        data: { ...profile, [field]: value, diagnostic: marker },
      })),
    );
    it.each(invalidFields)(
      "rejects $name without adopting, coercing or retrying the response",
      async ({ data }) => {
        const fetcher = vi.fn(
          async (_input: RequestInfo | URL, _init?: RequestInit) => json(data),
        );
        const result: unknown = await request(
          clientWith(fetcher),
          method,
        ).catch((reason: unknown) => reason);
        expect(result).toBeInstanceOf(AeroIdError);
        expect(result).toMatchObject({ status: 0, code: "response.invalid" });
        expect(String(result)).not.toContain(marker);
        expect(String(result)).not.toContain(profile.account_id);
        expect(fetcher).toHaveBeenCalledOnce();
        const [url, init] = fetcher.mock.calls[0];
        expect(new URL(String(url)).pathname).toBe("/v1/me/profile");
        expect(init?.method).toBe(method);
      },
    );

    it("preserves exact text and existing metadata without rewriting the request", async () => {
      const data = {
        ...profile,
        display_name: "  资料 name  ",
        locale: " en ",
        extension: { version: 3 },
      };
      const fetcher = vi.fn(
        async (_input: RequestInfo | URL, _init?: RequestInit) => json(data),
      );
      await expect(request(clientWith(fetcher), method)).resolves.toEqual(data);
      expect(fetcher).toHaveBeenCalledOnce();
      const [url, init] = fetcher.mock.calls[0];
      expect(new URL(String(url)).pathname).toBe("/v1/me/profile");
      expect(new URL(String(url)).search).toBe("");
      expect(init?.method).toBe(method);
      expect(init?.credentials).toBe("omit");
      expect(new Headers(init?.headers).get("Idempotency-Key")).toBeNull();
      expect(
        init?.body === undefined ? undefined : JSON.parse(String(init.body)),
      ).toEqual(method === "PATCH" ? submitted : undefined);
    });

    it("accepts all four explicit empty strings and does not reinterpret nullable preferences", async () => {
      const data = {
        ...profile,
        display_name: "",
        avatar_url: "",
        locale: "",
        timezone: "",
        preferences: null,
      };
      const fetcher = vi.fn(async () => json(data));
      await expect(request(clientWith(fetcher), method)).resolves.toEqual(data);
      expect(fetcher).toHaveBeenCalledOnce();
    });

    it("does not borrow text from a nested profile or aliases", async () => {
      const fetcher = vi.fn(async () =>
        json({ account_id: profile.account_id, profile, displayName: marker }),
      );
      await expect(request(clientWith(fetcher), method)).rejects.toMatchObject({
        status: 0,
        code: "response.invalid",
      });
      expect(fetcher).toHaveBeenCalledOnce();
    });

    it.each([null, [], "profile", 7])(
      "preserves invalid data-envelope rejection: %j",
      async (data) => {
        const fetcher = vi.fn(async () => json(data));
        await expect(
          request(clientWith(fetcher), method),
        ).rejects.toMatchObject({
          status: 0,
          code: "response.invalid",
        });
        expect(fetcher).toHaveBeenCalledOnce();
      },
    );

    it.each([403, 503])(
      "preserves server refusal %i without retry",
      async (status) => {
        const fetcher = vi.fn(
          async () =>
            new Response(
              JSON.stringify({
                error: {
                  code: "account.profile_unavailable",
                  message: "Profile unavailable",
                  request_id: "original-request-id",
                },
              }),
              { status },
            ),
        );
        await expect(
          request(clientWith(fetcher), method),
        ).rejects.toMatchObject({
          status,
          code: "account.profile_unavailable",
          requestId: "original-request-id",
        });
        expect(fetcher).toHaveBeenCalledOnce();
      },
    );
  },
);

describe("Profile GET transport boundaries", () => {
  it.each(["signal", "client"])(
    "keeps %s cancellation and never makes a replacement request",
    async (kind) => {
      const controller = new AbortController();
      let signal: AbortSignal | null | undefined;
      const fetcher = vi.fn(
        async (_input: RequestInfo | URL, init?: RequestInit) => {
          signal = init?.signal;
          return new Promise<Response>((_resolve, reject) => {
            signal?.addEventListener(
              "abort",
              () => reject(new DOMException("Aborted", "AbortError")),
              { once: true },
            );
          });
        },
      );
      const client = clientWith(fetcher);
      const pending = client.getProfile(controller.signal);
      if (kind === "signal") controller.abort();
      else client.cancelPendingReads();
      await expect(pending).rejects.toMatchObject({ code: "request.canceled" });
      expect(signal?.aborted).toBe(true);
      expect(fetcher).toHaveBeenCalledOnce();
    },
  );

  it("rejects marked stale reads before applying the Profile text check", async () => {
    const fetcher = vi.fn(async () =>
      json({}, { headers: { "X-Aero-Stale-Read": "true" } }),
    );
    await expect(clientWith(fetcher).getProfile()).rejects.toMatchObject({
      code: "region.stale_read",
    });
    expect(fetcher).toHaveBeenCalledOnce();
  });
});
