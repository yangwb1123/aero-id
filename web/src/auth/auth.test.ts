import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { PlatformConfig } from "../config";
import { createAuthState } from "./auth";
import { OidcClient, OidcError } from "./oidc";

const config: PlatformConfig = {
  snaplinkIssuer: "https://identity.example.test",
  snaplinkHostedLoginUrl: "https://identity.example.test/login/",
  snaplinkClientId: "aero-account-console",
  snaplinkResource: "aero-id",
  snaplinkScopes: ["openid", "profile", "account:read"],
  redirectUri: "https://accounts.example.test/",
  aeroIdApiBase: "https://accounts-api.example.test/v1",
};

let originalFetch: typeof window.fetch;

describe("createAuthState", () => {
  beforeEach(() => {
    originalFetch = window.fetch;
    window.fetch = vi.fn() as typeof fetch;
  });

  afterEach(() => {
    vi.restoreAllMocks();
    if (originalFetch) window.fetch = originalFetch;
    else Reflect.deleteProperty(window, "fetch");
  });

  it("prevents duplicate login attempts and exposes a retry after failure", async () => {
    let rejectLogin!: (reason: unknown) => void;
    const beginLogin = vi
      .spyOn(OidcClient.prototype, "beginLogin")
      .mockImplementation(
        () =>
          new Promise<void>((_resolve, reject) => {
            rejectLogin = reject;
          }),
      );
    const auth = createAuthState(config);

    const firstAttempt = auth.login();
    const duplicateAttempt = auth.login();
    expect(auth.status).toBe("loading");
    expect(beginLogin).toHaveBeenCalledTimes(1);

    rejectLogin(new OidcError("Snaplink 请求超时，请检查网络后重试"));
    await Promise.all([firstAttempt, duplicateAttempt]);

    expect(auth.status).toBe("error");
    expect(auth.error).toBe("Snaplink 请求超时，请检查网络后重试");

    beginLogin.mockResolvedValue(undefined);
    await auth.login();

    expect(beginLogin).toHaveBeenCalledTimes(2);
    expect(auth.error).toBeUndefined();
    expect(auth.status).toBe("loading");
  });

  it("shows a safe recovery message for unexpected login failures", async () => {
    vi.spyOn(OidcClient.prototype, "beginLogin").mockRejectedValue(
      new TypeError("fetch failed"),
    );
    const auth = createAuthState(config);

    await auth.login();

    expect(auth.status).toBe("error");
    expect(auth.error).toBe("无法连接 Snaplink，请检查网络后重试");
  });
});
