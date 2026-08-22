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

function optionalUrl(value: string | undefined): string | undefined {
  const normalized = value?.trim();
  return normalized ? trimSlash(normalized) : undefined;
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

function configuredScopes(runtime: string[] | undefined): string[] {
  return (
    runtime ??
    envList(import.meta.env.VITE_SNAPLINK_SCOPES) ?? [
      "openid",
      "profile",
      "account:read",
    ]
  );
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

  const defaultRedirect = `${window.location.origin}${window.location.pathname}`;
  return {
    snaplinkIssuer: trimSlash(snaplinkIssuer),
    snaplinkHostedLoginUrl: trimSlash(snaplinkHostedLoginUrl),
    snaplinkClientId,
    snaplinkResource: firstValue(
      runtime.snaplinkResource,
      import.meta.env.VITE_SNAPLINK_RESOURCE,
      "aero-id",
    )!,
    snaplinkScopes: configuredScopes(runtime.snaplinkScopes),
    redirectUri: firstValue(
      runtime.redirectUri,
      import.meta.env.VITE_SNAPLINK_REDIRECT_URI,
      defaultRedirect,
    )!,
    aeroIdApiBase: trimSlash(aeroIdApiBase),
    auditConsoleUrl: optionalUrl(
      firstValue(
        runtime.auditConsoleUrl,
        import.meta.env.VITE_AUDIT_CONSOLE_URL,
      ),
    ),
    aeroImConsoleUrl: optionalUrl(
      firstValue(
        runtime.aeroImConsoleUrl,
        import.meta.env.VITE_AERO_IM_CONSOLE_URL,
      ),
    ),
    aeroVaultConsoleUrl: optionalUrl(
      firstValue(
        runtime.aeroVaultConsoleUrl,
        import.meta.env.VITE_AERO_VAULT_CONSOLE_URL,
      ),
    ),
  };
}
