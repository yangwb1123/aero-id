const maxRememberedIntents = 32;

export interface IdempotentIntent {
  signature: string;
  idempotencyKey: string;
}

export function rememberIdempotencyKey(
  intent: Record<string, unknown>,
  keys: Map<string, string>,
  makeKey: () => string = () => crypto.randomUUID(),
): IdempotentIntent {
  const signature = JSON.stringify(intent);
  const current = keys.get(signature);
  if (current) {
    keys.delete(signature);
    keys.set(signature, current);
    return { signature, idempotencyKey: current };
  }

  const idempotencyKey = makeKey();
  keys.set(signature, idempotencyKey);
  while (keys.size > maxRememberedIntents) {
    const oldest = keys.keys().next().value;
    if (oldest === undefined) break;
    keys.delete(oldest);
  }
  return { signature, idempotencyKey };
}
