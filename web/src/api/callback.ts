import type { JsonRecord, PageData } from "../types";
import { AeroIdTransport } from "./transport";

export interface CallbackCreateInput {
  endpoint: string;
  eventTypes: string[];
}

export interface CallbackRegistration {
  subscription: JsonRecord;
  secret: string;
}

function isRecord(value: unknown): value is JsonRecord {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function dataOf(value: unknown): JsonRecord {
  if (!isRecord(value) || !isRecord(value.data)) return {};
  return value.data;
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

  async create(input: CallbackCreateInput): Promise<CallbackRegistration> {
    const data = dataOf(
      await this.transport.request("POST", "/admin/callback-subscriptions", {
        body: { endpoint: input.endpoint, event_types: input.eventTypes },
        timeoutMs: 30_000,
      }),
    );
    return {
      subscription: isRecord(data.subscription) ? data.subscription : {},
      secret: typeof data.secret === "string" ? data.secret : "",
    };
  }

  async deleteSubscription(id: string): Promise<void> {
    await this.transport.request(
      "DELETE",
      `/admin/callback-subscriptions/${encodeURIComponent(id)}`,
    );
  }

  async replayDelivery(id: string): Promise<JsonRecord> {
    return dataOf(
      await this.transport.request(
        "POST",
        `/admin/callback-subscriptions/deliveries/${encodeURIComponent(id)}/replay`,
      ),
    );
  }
}
