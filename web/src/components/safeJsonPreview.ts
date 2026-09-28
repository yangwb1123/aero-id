const maxPreviewBytes = 64 * 1024;
const maxPreviewDepth = 6;

const sensitiveKeyParts = [
  "password",
  "token",
  "secret",
  "cookie",
  "mfa",
  "recovery",
  "credential",
  "private_key",
  "access_key",
  "refresh",
  "authorization",
  "object_bytes",
  "blob",
  "message_body",
  "api_key",
  "signing_key",
  "jwt",
  "passcode",
  "otp",
  "verification_code",
  "message_text",
  "latest_message",
  "recent_message",
  "message_preview",
  "chat_history",
  "conversation",
].map(canonicalKey);

const opaqueReferenceKeys = new Set([
  "id",
  "accountid",
  "actorid",
  "channelid",
  "commentid",
  "conversationid",
  "fileid",
  "messageid",
  "notificationid",
  "objectid",
  "resourceid",
  "subjectid",
  "threadid",
  "workspaceid",
]);

function canonicalKey(key: string): string {
  return key.toLowerCase().replace(/[-_. ]/g, "");
}

function isSensitiveKey(key: string): boolean {
  const canonical = canonicalKey(key);
  if (opaqueReferenceKeys.has(canonical)) return false;
  if (["body", "content", "raw", "messages", "message"].includes(canonical))
    return true;
  return sensitiveKeyParts.some((part) => canonical.includes(part));
}

function redact(value: unknown, depth: number): unknown {
  if (depth >= maxPreviewDepth) return { truncated: true };
  if (Array.isArray(value)) return value.map((item) => redact(item, depth + 1));
  if (value && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value).flatMap(([key, item]) =>
        isSensitiveKey(key) ? [] : [[key, redact(item, depth + 1)]],
      ),
    );
  }
  return value;
}

/** Render untrusted API JSON without exposing credential or source-content fields. */
export function safeJsonPreview(value: unknown, space = 2): string {
  let serialized: string;
  try {
    serialized = JSON.stringify(redact(value ?? {}, 0), null, space);
  } catch {
    return "无法安全显示此响应";
  }
  if (new TextEncoder().encode(serialized).byteLength > maxPreviewBytes) {
    return JSON.stringify({ truncated: true }, null, space);
  }
  return serialized;
}
