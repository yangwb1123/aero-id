import type { JsonRecord, PageData } from "../types";
import { AeroIdTransport, requireDataEnvelope as dataOf } from "./transport";

export interface CallbackCreateInput {
  endpoint: string;
  eventTypes: string[];
}

export interface CallbackRegistration {
  subscription: JsonRecord;
  secret?: string;
}

function isRecord(value: unknown): value is JsonRecord {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function subscriptions(value: unknown): JsonRecord[] {
  return Array.isArray(value) ? value.filter(isRecord) : [];
}

export class CallbackClient {
  constructor(private readonly transport: AeroIdTransport) {}

  async list(cursor?: string): Promise<PageData<JsonRecord>> {
    const data = dataOf(
      await this.transport.request("GET", "/admin/callback-subscriptions", {
        query: { cursor, limit: 50 },
      }),
    );
    return {
      items: subscriptions(data.subscriptions),
      nextCursor:
        typeof data.next_cursor === "string" ? data.next_cursor : undefined,
    };
  }

  async create(
    input: CallbackCreateInput,
    idempotencyKey: string,
  ): Promise<CallbackRegistration> {
    const key = idempotencyKey.trim();
    if (!key) throw new Error("Callback 注册需要幂等键");
    const data = dataOf(
      await this.transport.request("POST", "/admin/callback-subscriptions", {
        body: { endpoint: input.endpoint, event_types: input.eventTypes },
        idempotencyKey: key,
        timeoutMs: 30_000,
      }),
    );
    return {
      subscription: isRecord(data.subscription) ? data.subscription : {},
      secret:
        typeof data.secret === "string" && data.secret
          ? data.secret
          : undefined,
    };
  }

  async deleteSubscription(id: string): Promise<void> {
    await this.transport.request(
      "DELETE",
      `/admin/callback-subscriptions/${encodeURIComponent(id)}`,
    );
  }

  async replayDelivery(
    id: string,
    idempotencyKey: string,
  ): Promise<JsonRecord> {
    const key = idempotencyKey.trim();
    if (!key) throw new Error("Callback 重放需要幂等键");
    return dataOf(
      await this.transport.request(
        "POST",
        `/admin/callback-subscriptions/deliveries/${encodeURIComponent(id)}/replay`,
        { idempotencyKey: key },
      ),
    );
  }
}
