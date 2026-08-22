import { beforeEach, describe, expect, it, vi } from "vitest";
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

describe("OidcClient", () => {
  beforeEach(() => sessionStorage.clear());

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
