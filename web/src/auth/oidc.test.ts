import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { PlatformConfig } from "../config";
import { OidcClient } from "./oidc";

const config: PlatformConfig = {
  snaplinkIssuer: "https://identity.example.test",
  snaplinkHostedLoginUrl: "https://identity.example.test/login/",
  snaplinkClientId: "aero-account-console",
  snaplinkResource: "aero-id",
  snaplinkScopes: ["openid", "profile", "account:read"],
  redirectUri: "https://accounts.example.test/",
  aeroIdApiBase: "https://accounts-api.example.test/v1",
};

const discovery = {
  issuer: config.snaplinkIssuer,
  authorization_endpoint: `${config.snaplinkIssuer}/auth/login`,
  token_endpoint: `${config.snaplinkIssuer}/token`,
  end_session_endpoint: `${config.snaplinkIssuer}/end_session`,
  code_challenge_methods_supported: ["S256"],
};

async function exchangeToken(
  overrides: Record<string, unknown>,
): Promise<Awaited<ReturnType<OidcClient["completeLogin"]>>> {
  const fetcher = vi.fn(async (input: RequestInfo | URL) => {
    if (String(input).endsWith("/.well-known/openid-configuration")) {
      return new Response(JSON.stringify(discovery), { status: 200 });
    }
    return new Response(
      JSON.stringify({
        access_token: "short-lived-token",
        token_type: "Bearer",
        expires_in: 300,
        ...overrides,
      }),
      { status: 200 },
    );
  });
  const client = new OidcClient(config, {
    fetch: fetcher as typeof fetch,
    replaceUrl: vi.fn(),
    now: () => 1_000,
  });
  const authorization = new URL(await client.prepareLogin());
  const callback = new URL(config.redirectUri);
  callback.searchParams.set("code", "authorization-code");
  callback.searchParams.set("state", authorization.searchParams.get("state")!);
  return client.completeLogin(callback.toString());
}

describe("OidcClient", () => {
  beforeEach(() => sessionStorage.clear());
  afterEach(() => vi.useRealTimers());

  it("retries OIDC discovery after a transient failure", async () => {
    const fetcher = vi
      .fn()
      .mockRejectedValueOnce(new TypeError("network unavailable"))
      .mockResolvedValueOnce(
        new Response(JSON.stringify(discovery), { status: 200 }),
      );
    const client = new OidcClient(config, { fetch: fetcher as typeof fetch });

    await expect(client.prepareLogin()).rejects.toThrow("network unavailable");
    await expect(client.prepareLogin()).resolves.toMatch(/^https:\/\//);
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it("builds an S256 authorization request without a client secret", async () => {
    const fetcher = vi.fn(
      async () => new Response(JSON.stringify(discovery), { status: 200 }),
    );
    const client = new OidcClient(config, { fetch: fetcher as typeof fetch });

    const target = new URL(await client.prepareLogin("#/profile"));

    expect(target.origin + target.pathname).toBe(config.snaplinkHostedLoginUrl);
    expect(target.searchParams.get("client_id")).toBe(config.snaplinkClientId);
    expect(target.searchParams.get("resource")).toBe("aero-id");
    expect(target.searchParams.get("code_challenge_method")).toBe("S256");
    expect(target.searchParams.get("code_challenge")).toHaveLength(43);
    expect(target.searchParams.has("client_secret")).toBe(false);
    expect(
      [...Array(sessionStorage.length)].map((_, index) =>
        sessionStorage.key(index),
      ),
    ).toEqual(["aero-platform-console:pkce:aero-account-console"]);
  });

  it("exchanges a valid callback once and never persists the access token", async () => {
    const fetcher = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.endsWith("/.well-known/openid-configuration")) {
          return new Response(JSON.stringify(discovery), { status: 200 });
        }
        expect(url).toBe(discovery.token_endpoint);
        expect(init?.headers).toMatchObject({
          "Content-Type": "application/x-www-form-urlencoded",
        });
        const form = init?.body as URLSearchParams;
        expect(form.get("client_secret")).toBeNull();
        expect(form.get("resource")).toBe("aero-id");
        return new Response(
          JSON.stringify({
            access_token: "access-token-must-stay-in-memory",
            id_token: "id-token-must-stay-in-memory",
            token_type: "Bearer",
            expires_in: 300,
            scope: "openid profile account:read",
          }),
          { status: 200 },
        );
      },
    );
    const replaceUrl = vi.fn();
    const client = new OidcClient(config, {
      fetch: fetcher as typeof fetch,
      replaceUrl,
      now: () => 1_000,
    });
    const authorization = new URL(await client.prepareLogin("#/connections"));
    const callback = new URL(config.redirectUri);
    callback.searchParams.set("code", "authorization-code");
    callback.searchParams.set(
      "state",
      authorization.searchParams.get("state")!,
    );
    callback.searchParams.set("iss", config.snaplinkIssuer);

    const first = client.completeLogin(callback.toString());
    const second = client.completeLogin(callback.toString());
    const session = await first;

    expect(second).toBe(first);
    expect(session.returnTo).toBe("#/connections");
    expect(session.accessToken).toBe("access-token-must-stay-in-memory");
    expect(session.idToken).toBe("id-token-must-stay-in-memory");
    expect(sessionStorage.length).toBe(0);
    expect(replaceUrl).toHaveBeenCalledWith(config.redirectUri);
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it("honors token lifetimes shorter than 30 seconds", async () => {
    const session = await exchangeToken({ expires_in: 5 });

    expect(session.expiresAt).toBe(6_000);
  });

  it("bounds the token response by streamed UTF-8 bytes", async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      if (String(input).endsWith("/.well-known/openid-configuration")) {
        return new Response(JSON.stringify(discovery), { status: 200 });
      }
      const body = new ReadableStream<Uint8Array>({
        start(controller) {
          controller.enqueue(new TextEncoder().encode('{"access_token":"'));
          controller.enqueue(new Uint8Array(65 * 1024).fill(0x61));
          controller.enqueue(
            new TextEncoder().encode('","token_type":"Bearer"}'),
          );
          controller.close();
        },
      });
      return new Response(body, { status: 200 });
    });
    const client = new OidcClient(config, {
      fetch: fetcher as typeof fetch,
      replaceUrl: vi.fn(),
    });
    const authorization = new URL(await client.prepareLogin());
    const callback = new URL(config.redirectUri);
    callback.searchParams.set("code", "authorization-code");
    callback.searchParams.set(
      "state",
      authorization.searchParams.get("state")!,
    );

    await expect(client.completeLogin(callback.toString())).rejects.toThrow(
      "token 响应过大",
    );
  });

  it("allows a fresh authorization attempt after token exchange failure", async () => {
    let tokenRequests = 0;
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      if (String(input).endsWith("/.well-known/openid-configuration")) {
        return new Response(JSON.stringify(discovery), { status: 200 });
      }
      tokenRequests += 1;
      if (tokenRequests === 1) throw new TypeError("network unavailable");
      return new Response(
        JSON.stringify({ access_token: "new-token", token_type: "Bearer" }),
        { status: 200 },
      );
    });
    const client = new OidcClient(config, {
      fetch: fetcher as typeof fetch,
      replaceUrl: vi.fn(),
    });
    const callbackForNewLogin = async (): Promise<string> => {
      const authorization = new URL(await client.prepareLogin());
      const callback = new URL(config.redirectUri);
      callback.searchParams.set("code", `authorization-code-${tokenRequests}`);
      callback.searchParams.set(
        "state",
        authorization.searchParams.get("state")!,
      );
      return callback.toString();
    };

    await expect(
      client.completeLogin(await callbackForNewLogin()),
    ).rejects.toThrow("network unavailable");
    await expect(
      client.completeLogin(await callbackForNewLogin()),
    ).resolves.toMatchObject({ accessToken: "new-token" });
    expect(tokenRequests).toBe(2);
  });

  it("aborts a hung token exchange and reports a recoverable timeout", async () => {
    vi.useFakeTimers();
    const fetcher = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        if (String(input).endsWith("/.well-known/openid-configuration")) {
          return new Response(JSON.stringify(discovery), { status: 200 });
        }
        return new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener(
            "abort",
            () => reject(new DOMException("Aborted", "AbortError")),
            { once: true },
          );
        });
      },
    );
    const client = new OidcClient(config, {
      fetch: fetcher as typeof fetch,
      replaceUrl: vi.fn(),
      requestTimeoutMs: 25,
    });
    const authorization = new URL(await client.prepareLogin());
    const callback = new URL(config.redirectUri);
    callback.searchParams.set("code", "authorization-code");
    callback.searchParams.set(
      "state",
      authorization.searchParams.get("state")!,
    );
    const login = client.completeLogin(callback.toString());
    const timeout = expect(login).rejects.toThrow("Snaplink 请求超时");

    await vi.advanceTimersByTimeAsync(25);
    await timeout;
    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(fetcher.mock.calls[1][1]?.signal?.aborted).toBe(true);
  });

  it("enforces its deadline if a fetch implementation ignores abort", async () => {
    vi.useFakeTimers();
    let resolveToken!: (response: Response) => void;
    let tokenSignal: AbortSignal | undefined;
    const fetcher = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        if (String(input).endsWith("/.well-known/openid-configuration")) {
          return new Response(JSON.stringify(discovery), { status: 200 });
        }
        tokenSignal = init?.signal ?? undefined;
        return new Promise<Response>((resolve) => {
          resolveToken = resolve;
        });
      },
    );
    const client = new OidcClient(config, {
      fetch: fetcher as typeof fetch,
      replaceUrl: vi.fn(),
      requestTimeoutMs: 25,
    });
    const authorization = new URL(await client.prepareLogin());
    const callback = new URL(config.redirectUri);
    callback.searchParams.set("code", "authorization-code");
    callback.searchParams.set(
      "state",
      authorization.searchParams.get("state")!,
    );
    const login = client.completeLogin(callback.toString());
    const timeout = expect(login).rejects.toThrow("Snaplink 请求超时");

    await vi.advanceTimersByTimeAsync(25);
    await timeout;
    expect(tokenSignal?.aborted).toBe(true);
    resolveToken(new Response('{"access_token":"late","token_type":"Bearer"}'));
  });

  it.each([
    { value: 0, label: "zero" },
    { value: -1, label: "negative" },
    { value: "invalid", label: "non-numeric" },
    { value: "5", label: "string" },
    { value: null, label: "null" },
  ])("rejects a $label token lifetime", async ({ value }) => {
    await expect(exchangeToken({ expires_in: value })).rejects.toThrow(
      "有效期无效",
    );
  });

  it("rejects a state mismatch before calling the token endpoint", async () => {
    const fetcher = vi.fn(
      async () => new Response(JSON.stringify(discovery), { status: 200 }),
    );
    const client = new OidcClient(config, {
      fetch: fetcher as typeof fetch,
      replaceUrl: vi.fn(),
    });
    await client.prepareLogin();

    await expect(
      client.completeLogin(`${config.redirectUri}?code=x&state=attacker-state`),
    ).rejects.toThrow("state 校验失败");
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(sessionStorage.length).toBe(0);
  });

  it("navigates to the discovered RP-initiated logout endpoint", async () => {
    const fetcher = vi.fn(
      async () => new Response(JSON.stringify(discovery), { status: 200 }),
    );
    const navigate = vi.fn();
    const client = new OidcClient(config, {
      fetch: fetcher as typeof fetch,
      navigate,
    });

    await client.logout("signed-id-token");

    const target = new URL(navigate.mock.calls[0][0]);
    expect(target.origin + target.pathname).toBe(
      discovery.end_session_endpoint,
    );
    expect(target.searchParams.get("client_id")).toBe(config.snaplinkClientId);
    expect(target.searchParams.get("id_token_hint")).toBe("signed-id-token");
    expect(target.searchParams.get("post_logout_redirect_uri")).toBe(
      config.redirectUri,
    );
  });
});
