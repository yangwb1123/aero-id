export interface PlatformConfig {
  snaplinkIssuer: string;
  snaplinkHostedLoginUrl: string;
  snaplinkClientId: string;
  snaplinkResource: string;
  snaplinkScopes: string[];
  redirectUri: string;
  aeroIdApiBase: string;
  auditConsoleUrl?: string;
  aeroImConsoleUrl?: string;
  aeroVaultConsoleUrl?: string;
}

const trimSlash = (value: string): string => value.replace(/\/+$/, "");

function envList(value: string | undefined): string[] | undefined {
  const items = value
    ?.split(/\s+/)
    .map((item) => item.trim())
    .filter(Boolean);
  return items && items.length > 0 ? items : undefined;
}

function optionalUrl(
  name: string,
  value: string | undefined,
): string | undefined {
  const normalized = value?.trim();
  if (!normalized) return undefined;
  validateUrl(name, normalized);
  return trimSlash(normalized);
}

function validateUrl(
  name: string,
  value: string,
  options: { noQueryOrHash?: boolean } = {},
): URL {
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    throw new Error(`运行配置 ${name} 不是有效 URL`);
  }
  const loopback = ["localhost", "127.0.0.1", "[::1]", "::1"].includes(
    url.hostname,
  );
  if (
    (url.protocol !== "https:" && !(url.protocol === "http:" && loopback)) ||
    url.username ||
    url.password
  ) {
    throw new Error(`运行配置 ${name} 必须是安全的 HTTPS URL`);
  }
  if (options.noQueryOrHash && (url.search || url.hash)) {
    throw new Error(`运行配置 ${name} 不允许 query 或 fragment`);
  }
  return url;
}

function firstValue(...values: Array<string | undefined>): string | undefined {
  return values.find((value) => Boolean(value?.trim()))?.trim();
}

function requiredValue(
  name: string,
  ...values: Array<string | undefined>
): string {
  const value = firstValue(...values);
  if (!value) throw new Error(`运行配置缺少：${name}`);
  return value;
}

function hasControlCharacters(value: string): boolean {
  return Array.from(value).some((character) => {
    const code = character.codePointAt(0) ?? 0;
    return code < 32 || (code >= 127 && code <= 159);
  });
}

function configuredScopes(runtime: string[] | undefined): string[] {
  const scopes = runtime ??
    envList(import.meta.env.VITE_SNAPLINK_SCOPES) ?? [
      "openid",
      "profile",
      "account:read",
    ];
  if (
    !Array.isArray(scopes) ||
    scopes.some((scope) => typeof scope !== "string")
  ) {
    throw new Error("运行配置 snaplinkScopes 必须是字符串数组");
  }
  const normalized = scopes.map((scope) => scope.trim());
  if (
    normalized.some(
      (scope) => !scope || /\s/.test(scope) || hasControlCharacters(scope),
    ) ||
    new Set(normalized).size !== normalized.length ||
    !normalized.includes("openid")
  ) {
    throw new Error(
      "运行配置 snaplinkScopes 必须是包含 openid 的有效唯一 scope 列表",
    );
  }
  return normalized;
}

export function loadPlatformConfig(): PlatformConfig {
  const runtime = window.__AERO_PLATFORM_CONFIG__ ?? {};
  const snaplinkIssuer = requiredValue(
    "snaplinkIssuer",
    runtime.snaplinkIssuer,
    import.meta.env.VITE_SNAPLINK_ISSUER,
  );
  const snaplinkClientId = requiredValue(
    "snaplinkClientId",
    runtime.snaplinkClientId,
    import.meta.env.VITE_SNAPLINK_CLIENT_ID,
  );
  const snaplinkHostedLoginUrl = requiredValue(
    "snaplinkHostedLoginUrl",
    runtime.snaplinkHostedLoginUrl,
    import.meta.env.VITE_SNAPLINK_HOSTED_LOGIN_URL,
  );
  const aeroIdApiBase = requiredValue(
    "aeroIdApiBase",
    runtime.aeroIdApiBase,
    import.meta.env.VITE_AERO_ID_API_BASE,
  );
  validateUrl("snaplinkIssuer", snaplinkIssuer, { noQueryOrHash: true });
  validateUrl("snaplinkHostedLoginUrl", snaplinkHostedLoginUrl, {
    noQueryOrHash: true,
  });
  const apiUrl = validateUrl("aeroIdApiBase", aeroIdApiBase, {
    noQueryOrHash: true,
  });
  if (!apiUrl.pathname.replace(/\/+$/, "").endsWith("/v1")) {
    throw new Error("运行配置 aeroIdApiBase 必须指向 aero-id /v1 API");
  }

  const defaultRedirect = `${window.location.origin}${window.location.pathname}`;
  const redirectUri = firstValue(
    runtime.redirectUri,
    import.meta.env.VITE_SNAPLINK_REDIRECT_URI,
    defaultRedirect,
  )!;
  const redirectUrl = validateUrl("redirectUri", redirectUri, {
    noQueryOrHash: true,
  });
  if (redirectUrl.origin !== window.location.origin) {
    throw new Error("运行配置 redirectUri 必须与当前前端同源");
  }
  const snaplinkResource = firstValue(
    runtime.snaplinkResource,
    import.meta.env.VITE_SNAPLINK_RESOURCE,
    "aero-id",
  )!;

  return {
    snaplinkIssuer: trimSlash(snaplinkIssuer),
    snaplinkHostedLoginUrl: trimSlash(snaplinkHostedLoginUrl),
    snaplinkClientId,
    snaplinkResource,
    snaplinkScopes: configuredScopes(runtime.snaplinkScopes),
    redirectUri,
    aeroIdApiBase: trimSlash(aeroIdApiBase),
    auditConsoleUrl: optionalUrl(
      "auditConsoleUrl",
      firstValue(
        runtime.auditConsoleUrl,
        import.meta.env.VITE_AUDIT_CONSOLE_URL,
      ),
    ),
    aeroImConsoleUrl: optionalUrl(
      "aeroImConsoleUrl",
      firstValue(
        runtime.aeroImConsoleUrl,
        import.meta.env.VITE_AERO_IM_CONSOLE_URL,
      ),
    ),
    aeroVaultConsoleUrl: optionalUrl(
      "aeroVaultConsoleUrl",
      firstValue(
        runtime.aeroVaultConsoleUrl,
        import.meta.env.VITE_AERO_VAULT_CONSOLE_URL,
      ),
    ),
  };
}
