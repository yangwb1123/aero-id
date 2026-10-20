import { vi } from "vitest";
import type { AeroIdClient } from "../api/aeroId";
import type { CallbackRegistration } from "../api/callback";
import type { CallbackDelivery } from "../api/callbackDelivery";
import { commandTestReport, commandTestClient } from "./commandTestFixtures";

export const callbackSubscriptionID = "01800000-0000-7000-8000-0000000000a1";
export const callbackOtherSubscriptionID =
  "01800000-0000-7000-8000-0000000000a2";
export const callbackDeliveryID = "01800000-0000-7000-8000-000000000009";
export function callbackTestReport() {
  const report = commandTestReport();
  report.capabilities.push(
    ...["callback.read", "callback.manage"].map((name) => ({
      name,
      registered: true,
      enabled: true,
      available: true,
      authorization: "allowed" as const,
      authorization_scope: "global" as const,
    })),
  );
  return report;
}
export function callbackTestDelivery(): CallbackDelivery {
  const now = new Date().toISOString();
  return {
    id: callbackDeliveryID,
    subscription_id: callbackSubscriptionID,
    event_id: "01800000-0000-7000-8000-0000000000a3",
    tenant_id: "tenant-a",
    action: "account.provisioned",
    status: "dead_letter",
    attempt: 8,
    revision: 7,
    lease_generation: 8,
    status_code: 503,
    error_code: "callback.delivery_failed",
    next_attempt_at: now,
    created_at: now,
    updated_at: now,
  };
}
export function callbackTestClient() {
  const callbacks = {
    list: vi.fn(async (_cursor?: string, _signal?: AbortSignal) => ({
      items: [
        {
          id: callbackSubscriptionID,
          tenant_id: "tenant-a",
          owner_uid: "caller",
          endpoint: "https://callback.example/events",
          verified: true,
          enabled: true,
          event_types: ["account.provisioned"],
        },
      ],
      nextCursor: undefined as string | undefined,
    })),
    listDeliveries: vi.fn(
      async (
        _id: string,
        _options?: { signal?: AbortSignal; cursor?: string; status?: string },
      ) => ({
        items: [callbackTestDelivery()],
        nextCursor: undefined as string | undefined,
      }),
    ),
    getDelivery: vi.fn(
      async (_sub: string, _id: string, _signal?: AbortSignal) =>
        callbackTestDelivery(),
    ),
    replayDelivery: vi.fn(async (_id: string, _key: string) => ({
      status: "queued",
    })),
    deleteSubscription: vi.fn(async () => undefined),
    create: vi.fn(
      async (_input: unknown, _key: string): Promise<CallbackRegistration> => ({
        subscription: {
          id: callbackSubscriptionID,
          tenant_id: "tenant-a",
          endpoint: "https://callback.example/events",
          event_types: ["account.provisioned"],
          verified: true,
          enabled: true,
        },
        secret: undefined,
      }),
    ),
  };
  const commands = commandTestClient().commands;
  return {
    client: { callbacks, commands } as unknown as AeroIdClient,
    callbacks,
    commands,
  };
}
