import type { PlatformConfig } from "../config";

const pendingLifetimeMs = 10 * 60 * 1000;
const maxTokenResponseBytes = 64 * 1024;

interface DiscoveryDocument {
  issuer: string;
  authorization_endpoint: string;
  token_endpoint: string;
  end_session_endpoint?: string;
  code_challenge_methods_supported?: string[];
}

interface PendingAuthorization {
  version: 1;
  state: string;
  nonce: string;
  verifier: string;
  issuer: string;
  redirectUri: string;
  returnTo: string;
  createdAt: number;
}

interface TokenResponse {
  access_token?: string;
  token_type?: string;
  expires_in?: number;
  id_token?: string;
  scope?: string;
  error?: string;
  error_description?: string;
}

export interface OidcSession {
  accessToken: string;
  idToken?: string;
  expiresAt: number;
  returnTo: string;
  scope: string[];
}

export interface OidcClientOptions {
  fetch?: typeof fetch;
  storage?: Storage;
  navigate?: (url: string) => void;
  replaceUrl?: (url: string) => void;
  now?: () => number;
}

export class OidcError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "OidcError";
  }
}

function base64Url(bytes: Uint8Array): string {
  let raw = "";
  for (const byte of bytes) raw += String.fromCharCode(byte);
  return btoa(raw).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function randomValue(): string {
  const bytes = new Uint8Array(32);
  crypto.getRandomValues(bytes);
  return base64Url(bytes);
}

async function codeChallenge(verifier: string): Promise<string> {
  const digest = await crypto.subtle.digest(
    "SHA-256",
    new TextEncoder().encode(verifier),
  );
  return base64Url(new Uint8Array(digest));
}

function normalizeIssuer(value: string): string {
  return value.replace(/\/+$/, "");
}

function requireSafeEndpoint(value: string, label: string): URL {
  let endpoint: URL;
  try {
    endpoint = new URL(value);
  } catch {
    throw new OidcError(`${label} 不是有效 URL`);
  }
  const loopback = ["localhost", "127.0.0.1", "[::1]", "::1"].includes(
    endpoint.hostname,
  );
  if (
    endpoint.protocol !== "https:" &&
    !(endpoint.protocol === "http:" && loopback)
  ) {
    throw new OidcError(`${label} 必须使用 HTTPS（仅回环地址允许 HTTP）`);
  }
  return endpoint;
}

function cleanCallbackUrl(source: URL): string {
  return `${source.origin}${source.pathname}${source.hash}`;
}

export class OidcClient {
  private readonly fetcher: typeof fetch;
  private readonly storage: Storage;
  private readonly navigate: (url: string) => void;
  private readonly replaceUrl: (url: string) => void;
  private readonly now: () => number;
  private discovery?: Promise<DiscoveryDocument>;
  private exchange?: Promise<OidcSession>;

  constructor(
    private readonly config: PlatformConfig,
    options: OidcClientOptions = {},
  ) {
    this.fetcher = options.fetch ?? window.fetch.bind(window);
    this.storage = options.storage ?? window.sessionStorage;
    this.navigate = options.navigate ?? ((url) => window.location.assign(url));
    this.replaceUrl =
      options.replaceUrl ??
      ((url) => window.history.replaceState(null, "", url));
    this.now = options.now ?? Date.now;
    requireSafeEndpoint(config.snaplinkIssuer, "Snaplink issuer");
    requireSafeEndpoint(config.redirectUri, "OAuth redirect URI");
  }

  hasCallback(url = window.location.href): boolean {
    const params = new URL(url).searchParams;
    return params.has("code") || params.has("error");
  }

  async prepareLogin(returnTo = "#/overview"): Promise<string> {
    const discovery = await this.getDiscovery();
    const verifier = randomValue();
    const pending: PendingAuthorization = {
      version: 1,
      state: randomValue(),
      nonce: randomValue(),
      verifier,
      issuer: discovery.issuer,
      redirectUri: this.config.redirectUri,
      returnTo,
      createdAt: this.now(),
    };
    this.storage.setItem(this.pendingKey(), JSON.stringify(pending));

    const authorization = requireSafeEndpoint(
      this.config.snaplinkHostedLoginUrl,
      "Snaplink hosted login URL",
    );
    authorization.searchParams.set("client_id", this.config.snaplinkClientId);
    authorization.searchParams.set("response_type", "code");
    authorization.searchParams.set("redirect_uri", this.config.redirectUri);
    authorization.searchParams.set(
      "scope",
      this.config.snaplinkScopes.join(" "),
    );
    authorization.searchParams.set("resource", this.config.snaplinkResource);
    authorization.searchParams.set("state", pending.state);
    authorization.searchParams.set("nonce", pending.nonce);
    authorization.searchParams.set(
      "code_challenge",
      await codeChallenge(verifier),
    );
    authorization.searchParams.set("code_challenge_method", "S256");
    return authorization.toString();
  }

  async beginLogin(returnTo?: string): Promise<void> {
    this.navigate(await this.prepareLogin(returnTo));
  }

  completeLogin(url = window.location.href): Promise<OidcSession> {
    this.exchange ??= this.exchangeCallback(new URL(url));
    return this.exchange;
  }

  clear(): void {
    this.storage.removeItem(this.pendingKey());
    this.exchange = undefined;
  }

  async logout(idToken?: string): Promise<void> {
    this.clear();
    const discovery = await this.getDiscovery();
    if (!discovery.end_session_endpoint) {
      throw new OidcError("Snaplink 未声明退出端点");
    }
    const endpoint = requireSafeEndpoint(
      discovery.end_session_endpoint,
      "Snaplink logout endpoint",
    );
    endpoint.searchParams.set("client_id", this.config.snaplinkClientId);
    endpoint.searchParams.set(
      "post_logout_redirect_uri",
      this.config.redirectUri,
    );
    if (idToken) endpoint.searchParams.set("id_token_hint", idToken);
    this.navigate(endpoint.toString());
  }

  private pendingKey(): string {
    return `aero-platform-console:pkce:${this.config.snaplinkClientId}`;
  }

  private async getDiscovery(): Promise<DiscoveryDocument> {
    this.discovery ??= this.fetchDiscovery();
    return this.discovery;
  }

  private async fetchDiscovery(): Promise<DiscoveryDocument> {
    const endpoint = `${normalizeIssuer(this.config.snaplinkIssuer)}/.well-known/openid-configuration`;
    const response = await this.fetcher(endpoint, {
      headers: { Accept: "application/json" },
      credentials: "omit",
    });
    if (!response.ok) throw new OidcError("无法读取 Snaplink OIDC 配置");
    const value = (await response.json()) as Partial<DiscoveryDocument>;
    if (
      normalizeIssuer(value.issuer ?? "") !==
        normalizeIssuer(this.config.snaplinkIssuer) ||
      !value.authorization_endpoint ||
      !value.token_endpoint
    ) {
      throw new OidcError("Snaplink OIDC 配置与运行配置不匹配");
    }
    if (!value.code_challenge_methods_supported?.includes("S256")) {
      throw new OidcError("Snaplink 未声明支持 PKCE S256");
    }
    requireSafeEndpoint(
      value.authorization_endpoint,
      "Snaplink authorization endpoint",
    );
    requireSafeEndpoint(value.token_endpoint, "Snaplink token endpoint");
    return value as DiscoveryDocument;
  }

  private readPending(): PendingAuthorization {
    const raw = this.storage.getItem(this.pendingKey());
    if (!raw) throw new OidcError("登录状态已失效，请重新登录");
    let pending: PendingAuthorization;
    try {
      pending = JSON.parse(raw) as PendingAuthorization;
    } catch {
      throw new OidcError("登录状态无效，请重新登录");
    }
    if (
      pending.version !== 1 ||
      !pending.state ||
      !pending.verifier ||
      this.now() - pending.createdAt > pendingLifetimeMs
    ) {
      throw new OidcError("登录状态已过期，请重新登录");
    }
    return pending;
  }

  private async exchangeCallback(callback: URL): Promise<OidcSession> {
    const pending = this.readPending();
    const code = this.validateCallback(callback, pending);
    this.storage.removeItem(this.pendingKey());
    const discovery = await this.getDiscovery();
    const token = await this.exchangeCode(discovery, pending, code);
    const lifetime = Math.max(30, Number(token.expires_in ?? 300));
    return {
      accessToken: token.access_token!,
      idToken: token.id_token,
      expiresAt: this.now() + lifetime * 1000,
      returnTo: pending.returnTo,
      scope:
        token.scope?.split(/\s+/).filter(Boolean) ?? this.config.snaplinkScopes,
    };
  }

  private validateCallback(
    callback: URL,
    pending: PendingAuthorization,
  ): string {
    this.replaceUrl(cleanCallbackUrl(callback));
    const returnedState = callback.searchParams.get("state") ?? "";
    if (returnedState !== pending.state)
      return this.rejectCallback("登录 state 校验失败");
    const returnedIssuer = callback.searchParams.get("iss");
    if (
      returnedIssuer &&
      normalizeIssuer(returnedIssuer) !== normalizeIssuer(pending.issuer)
    ) {
      return this.rejectCallback("登录 issuer 校验失败");
    }
    const oauthError = callback.searchParams.get("error");
    if (oauthError) {
      return this.rejectCallback(
        callback.searchParams.get("error_description") ?? oauthError,
      );
    }
    const code = callback.searchParams.get("code");
    if (!code) return this.rejectCallback("Snaplink 未返回授权码");
    return code;
  }

  private rejectCallback(message: string): never {
    this.storage.removeItem(this.pendingKey());
    throw new OidcError(message);
  }

  private async exchangeCode(
    discovery: DiscoveryDocument,
    pending: PendingAuthorization,
    code: string,
  ): Promise<TokenResponse> {
    const body = new URLSearchParams({
      grant_type: "authorization_code",
      code,
      client_id: this.config.snaplinkClientId,
      redirect_uri: pending.redirectUri,
      code_verifier: pending.verifier,
      resource: this.config.snaplinkResource,
    });
    const response = await this.fetcher(discovery.token_endpoint, {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/x-www-form-urlencoded",
      },
      body,
      credentials: "omit",
    });
    return this.readTokenResponse(response);
  }

  private async readTokenResponse(response: Response): Promise<TokenResponse> {
    const text = await response.text();
    if (text.length > maxTokenResponseBytes)
      throw new OidcError("Snaplink token 响应过大");
    let token: TokenResponse;
    try {
      token = JSON.parse(text) as TokenResponse;
    } catch {
      throw new OidcError("Snaplink token 响应无效");
    }
    if (!response.ok || token.error) {
      throw new OidcError(
        token.error_description ?? token.error ?? "Snaplink 拒绝了授权码交换",
      );
    }
    if (!token.access_token || token.token_type?.toLowerCase() !== "bearer") {
      throw new OidcError("Snaplink 未返回有效 Bearer access token");
    }
    return token;
  }
}
