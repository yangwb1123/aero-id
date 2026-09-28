export interface TokenResponse {
  access_token?: string;
  token_type?: string;
  expires_in?: number;
  id_token?: string;
  scope?: string;
  error?: string;
  error_description?: string;
}

export class OidcError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "OidcError";
  }
}

export async function readBoundedResponseText(
  response: Response,
  maxBytes: number,
  tooLargeMessage: string,
): Promise<string> {
  const declaredLength = Number(response.headers.get("Content-Length"));
  if (Number.isFinite(declaredLength) && declaredLength > maxBytes) {
    try {
      await response.body?.cancel();
    } catch {
      // The response is rejected regardless of cancellation support.
    }
    throw new OidcError(tooLargeMessage);
  }
  if (!response.body) return "";

  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      total += value.byteLength;
      if (total > maxBytes) {
        try {
          await reader.cancel();
        } catch {
          // The response is rejected regardless of cancellation support.
        }
        throw new OidcError(tooLargeMessage);
      }
      chunks.push(value);
    }
  } finally {
    reader.releaseLock();
  }

  const bytes = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return new TextDecoder().decode(bytes);
}

export function parseTokenResponse(text: string): TokenResponse {
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch {
    throw new OidcError("Snaplink token 响应无效");
  }
  if (!value || typeof value !== "object" || Array.isArray(value))
    throw new OidcError("Snaplink token 响应无效");
  return value as TokenResponse;
}

export function tokenResponseError(token: TokenResponse): OidcError {
  if (typeof token.error_description === "string" && token.error_description)
    return new OidcError(token.error_description);
  if (typeof token.error === "string" && token.error)
    return new OidcError(token.error);
  return new OidcError("Snaplink 拒绝了授权码交换");
}

export function hasBearerAccessToken(token: TokenResponse): boolean {
  return Boolean(
    token.access_token &&
    typeof token.access_token === "string" &&
    typeof token.token_type === "string" &&
    token.token_type.toLowerCase() === "bearer" &&
    (token.id_token === undefined || typeof token.id_token === "string") &&
    (token.scope === undefined || typeof token.scope === "string"),
  );
}
