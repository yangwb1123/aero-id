import { describe, expect, it, vi } from "vitest";
import { AeroIdTransport } from "./transport";
import {
  CommandIntentClient,
  parseCommandIntent,
  type CommandIntent,
} from "./commandIntent";

function preparedIntent(): CommandIntent {
  return {
    intent_id: "01800000-0000-7000-8000-000000000001",
    schema_version: 1,
    account_id: "01800000-0000-7000-8000-000000000002",
    tenant_id: "tenant-a",
    client_id: "console",
    issuer: "https://snaplink.example",
    version: 1,
    status: "prepared",
    plan: { kind: "export", datasets: ["aero-vault.usage"] },
    preconditions: {
      account_status: "active",
      home_region: "local",
      writer_epoch: 0,
      resolver_version: "v1",
      snapshots: [],
    },
    confirmation_digest: `v1:${"a".repeat(64)}`,
    destructive: false,
    authorization_version: "unsupported",
    resource_authorization: "unknown",
    created_at: new Date(Date.now() - 1000).toISOString(),
    expires_at: new Date(Date.now() + 300_000).toISOString(),
  };
}

describe("server-owned command intent contract", () => {
  it("prepares without executing and posts only the confirmation on execute", async () => {
    const intent = preparedIntent();
    const executed = {
      ...intent,
      version: 2,
      status: "executed",
      receipt_kind: "sync_job",
      receipt_id: "01800000-0000-7000-8000-000000000003",
    };
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ data: intent })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ data: executed })));
    const client = new CommandIntentClient(
      new AeroIdTransport(
        "https://id.example/v1",
        () => "memory-token",
        fetcher,
      ),
    );
    const result = await client.prepare(intent.plan, "stable-key");
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(fetcher.mock.calls[0][1].headers.get("Idempotency-Key")).toBe(
      "stable-key",
    );
    await client.execute(result);
    expect(JSON.parse(fetcher.mock.calls[1][1].body)).toEqual({
      version: 1,
      confirmation_digest: intent.confirmation_digest,
    });
    expect(String(fetcher.mock.calls[1][0])).toContain(
      `/command-intents/${intent.intent_id}/execute`,
    );
  });

  it("rejects malformed, unsafe or purported authorization grants", () => {
    expect(parseCommandIntent(preparedIntent()).status).toBe("prepared");
    for (const patch of [
      { schema_version: 2 },
      { resource_authorization: "allowed" },
      { authorization_version: "invented" },
      { receipt_id: "01800000-0000-7000-8000-000000000003" },
      { version: Number.MAX_SAFE_INTEGER + 1 },
      { plan: { kind: "erase", datasets: [] } },
      { status: "unknown" },
    ]) {
      expect(() =>
        parseCommandIntent({ ...preparedIntent(), ...patch }),
      ).toThrow("不会推断执行成功");
    }
  });

  it("rejects a receipt for a different target and never auto-retries", async () => {
    const intent = preparedIntent();
    const fetcher = vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            data: {
              ...intent,
              account_id: "01800000-0000-7000-8000-000000000099",
              version: 2,
              status: "executed",
              receipt_kind: "sync_job",
              receipt_id: "01800000-0000-7000-8000-000000000003",
            },
          }),
        ),
    );
    const client = new CommandIntentClient(
      new AeroIdTransport("https://id.example/v1", () => "token", fetcher),
    );
    await expect(client.execute(intent)).rejects.toThrow("不会推断执行成功");
    expect(fetcher).toHaveBeenCalledTimes(1);
  });
});
