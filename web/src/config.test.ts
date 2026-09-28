import { afterEach, describe, expect, it } from "vitest";
import { loadPlatformConfig } from "./config";

function configure(overrides: Partial<AeroPlatformRuntimeConfig> = {}): void {
  window.__AERO_PLATFORM_CONFIG__ = {
    snaplinkIssuer: "https://identity.example.test",
    snaplinkHostedLoginUrl: "https://identity.example.test/login/",
    snaplinkClientId: "aero-account-console",
    snaplinkScopes: ["openid", "profile", "account:read"],
    redirectUri: `${window.location.origin}/oauth/callback`,
    aeroIdApiBase: "https://accounts.example.test/v1",
    ...overrides,
  };
}

afterEach(() => {
  delete window.__AERO_PLATFORM_CONFIG__;
});

describe("loadPlatformConfig", () => {
  it("preserves exact OAuth redirect URI and validates a /v1 API base", () => {
    const redirectUri = `${window.location.origin}/oauth/callback`;
    configure({ redirectUri });

    const config = loadPlatformConfig();

    expect(config.redirectUri).toBe(redirectUri);
    expect(config.aeroIdApiBase).toBe("https://accounts.example.test/v1");
    expect(config.snaplinkScopes).toContain("openid");
  });

  it("rejects an insecure external API URL before bearer requests can start", () => {
    configure({ aeroIdApiBase: "http://accounts.example.test/v1" });

    expect(() => loadPlatformConfig()).toThrow("aeroIdApiBase");
  });

  it("allows HTTP only for a loopback API during local development", () => {
    configure({ aeroIdApiBase: "http://localhost:8080/v1/" });

    expect(loadPlatformConfig().aeroIdApiBase).toBe("http://localhost:8080/v1");
  });

  it("rejects credentials or query parameters in the API base", () => {
    configure({
      aeroIdApiBase: "https://user:secret@accounts.example.test/v1",
    });
    expect(() => loadPlatformConfig()).toThrow("aeroIdApiBase");

    configure({
      aeroIdApiBase: "https://accounts.example.test/v1?token=secret",
    });
    expect(() => loadPlatformConfig()).toThrow("不允许 query 或 fragment");
  });

  it("rejects a base URL that does not target the versioned API", () => {
    configure({ aeroIdApiBase: "https://accounts.example.test/api" });

    expect(() => loadPlatformConfig()).toThrow("必须指向 aero-id /v1 API");
  });

  it("rejects a redirect on a different origin", () => {
    configure({ redirectUri: "https://other.example.test/callback" });

    expect(() => loadPlatformConfig()).toThrow("必须与当前前端同源");
  });

  it("rejects malformed or incomplete OIDC scopes", () => {
    configure({ snaplinkScopes: ["profile", "account:read"] });

    expect(() => loadPlatformConfig()).toThrow("包含 openid");
  });

  it("rejects credentials embedded in an optional console URL", () => {
    configure({ auditConsoleUrl: "https://user:secret@audit.example.test" });

    expect(() => loadPlatformConfig()).toThrow("auditConsoleUrl");
  });
});
