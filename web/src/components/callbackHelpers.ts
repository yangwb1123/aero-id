const utf8Encoder = new TextEncoder();

export function normalizeCallbackEndpoint(value: string): string {
  let endpoint: URL;
  try {
    endpoint = new URL(value.trim());
  } catch {
    throw new Error("Callback Endpoint 必须是有效的 HTTPS URL");
  }
  if (
    endpoint.protocol !== "https:" ||
    !endpoint.hostname ||
    endpoint.username ||
    endpoint.password
  ) {
    throw new Error("Callback Endpoint 必须是 HTTPS URL，且不能包含用户信息");
  }
  const normalized = endpoint.toString();
  if (utf8Encoder.encode(normalized).byteLength > 2048)
    throw new Error("Callback Endpoint 不能超过 2048 个 UTF-8 字节");
  return normalized;
}

export interface CallbackCreateIntent {
  signature: string;
  idempotencyKey: string;
}

export function callbackCreateIntent(
  input: { endpoint: string; eventTypes: string[] },
  current: CallbackCreateIntent | undefined,
  makeKey: () => string = () => crypto.randomUUID(),
): CallbackCreateIntent {
  const signature = JSON.stringify(input);
  return current?.signature === signature
    ? current
    : { signature, idempotencyKey: makeKey() };
}

export function parseCallbackEventTypes(value: string): string[] {
  const eventTypes = [
    ...new Set(
      value
        .split(/[\n,]/)
        .map((item) => item.trim())
        .filter(Boolean),
    ),
  ];
  if (eventTypes.length === 0)
    throw new Error("至少填写一个允许投递的事件类型");
  if (eventTypes.length > 64) throw new Error("最多允许 64 个事件类型");
  if (
    eventTypes.some(
      (eventType) => utf8Encoder.encode(eventType).byteLength > 128,
    )
  ) {
    throw new Error("单个事件类型不能超过 128 个 UTF-8 字节");
  }
  return eventTypes;
}
