import { describe, expect, it, vi } from "vitest";
import {
  CommandIntentClient,
  parseCommandIntent,
  type CommandIntent,
} from "./commandIntent";
import { AeroIdTransport } from "./transport";
import { commandTestIntent } from "../components/commandTestFixtures";
import { callbackTestDelivery } from "../components/callbackTestFixtures";

function callbackIntent(): CommandIntent {
  const intent = commandTestIntent();
  const delivery = callbackTestDelivery();
  return {
    ...intent,
    destructive: true,
    plan: {
      kind: "callback_replay",
      datasets: [],
      callback: {
        subscription_id: delivery.subscription_id,
        delivery_id: delivery.id,
        event_id: delivery.event_id,
        revision: delivery.revision,
        lease_generation: delivery.lease_generation,
      },
    },
    preconditions: { ...intent.preconditions, snapshots: [] },
  };
}
function response(data: unknown) {
  return new Response(JSON.stringify({ data }), {
    headers: { "Content-Type": "application/json" },
  });
}

describe("durable callback preview client", () => {
  it("binds exact original target/revision and accepts only the intent's own queue receipt", async () => {
    const intent = callbackIntent();
    const executed = {
      ...intent,
      status: "executed",
      version: 2,
      receipt_kind: "callback_replay",
      receipt_id: intent.intent_id,
    };
    const fetcher = vi.fn(async () => response(intent));
    const client = new CommandIntentClient(
      new AeroIdTransport(
        "https://id.example/v1",
        () => "memory-token",
        fetcher,
      ),
    );
    expect(
      (await client.prepare(intent.plan, "original-key", intent.account_id))
        .plan.callback,
    ).toEqual(intent.plan.callback);
    fetcher.mockResolvedValueOnce(response(executed));
    expect((await client.execute(intent)).receipt_id).toBe(intent.intent_id);
    fetcher.mockResolvedValueOnce(
      response({ ...executed, receipt_id: intent.plan.callback!.delivery_id }),
    );
    await expect(client.execute(intent)).rejects.toMatchObject({
      code: "response.invalid",
    });
  });
  it("rejects changed replay scope and never accepts empty wildcard datasets for ordinary commands", async () => {
    const intent = callbackIntent();
    expect(() => parseCommandIntent(intent)).not.toThrow();
    for (const plan of [
      { ...intent.plan, callback: undefined },
      { ...intent.plan, datasets: ["aero-vault.usage"] },
      { ...intent.plan, callback: { ...intent.plan.callback!, revision: 0 } },
      {
        ...intent.plan,
        callback: { ...intent.plan.callback!, lease_generation: -1 },
      },
      { kind: "sync", datasets: [], callback: intent.plan.callback },
    ])
      expect(() => parseCommandIntent({ ...intent, plan })).toThrow();
    const changed = {
      ...intent,
      plan: {
        ...intent.plan,
        callback: {
          ...intent.plan.callback!,
          revision: intent.plan.callback!.revision + 1,
        },
      },
    };
    const fetcher = vi.fn(async () => response(changed));
    const client = new CommandIntentClient(
      new AeroIdTransport(
        "https://id.example/v1",
        () => "memory-token",
        fetcher,
      ),
    );
    await expect(
      client.prepare(intent.plan, "original-key", intent.account_id),
    ).rejects.toMatchObject({ code: "response.invalid" });
  });
});
