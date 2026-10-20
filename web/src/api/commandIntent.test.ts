import { describe, expect, it, vi } from "vitest";
import { AeroIdTransport } from "./transport";
import {
  CommandIntentClient,
  parseCommandIntent,
  type CommandIntent,
  type CommandPlan,
} from "./commandIntent";
import { commandTestIntent } from "../components/commandTestFixtures";

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

  it("canonicalizes an explicit UUID target and rejects invalid IDs before POST", async () => {
    const intent = {
      ...preparedIntent(),
      account_id: "01800000-0000-7000-8000-0000000000ab",
    };
    const fetcher = vi.fn<typeof fetch>(
      async () => new Response(JSON.stringify({ data: intent })),
    );
    const client = new CommandIntentClient(
      new AeroIdTransport("https://id.example/v1", () => "token", fetcher),
    );
    await expect(
      client.prepare(
        intent.plan,
        "original-key",
        intent.account_id.toUpperCase(),
      ),
    ).resolves.toEqual(intent);
    expect(
      JSON.parse(fetcher.mock.calls[0][1]!.body as string).account_id,
    ).toBe(intent.account_id);
    await expect(
      client.prepare(intent.plan, "original-key", "not-a-uuid"),
    ).rejects.toThrow("不会推断执行成功");
    expect(fetcher).toHaveBeenCalledOnce();
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

  it("rejects changed immutable plan/lifetime and wrong engine kind under the same confirmation", async () => {
    const intent = preparedIntent();
    for (const patch of [
      { plan: { kind: "export", datasets: ["aero-im.workspaces"] } },
      {
        expires_at: new Date(
          Date.parse(intent.expires_at) + 300000,
        ).toISOString(),
      },
      { receipt_kind: "operation" },
      { version: 3 },
    ]) {
      const fetcher = vi.fn(
        async () =>
          new Response(
            JSON.stringify({
              data: {
                ...intent,
                status: "executed",
                version: 2,
                receipt_kind: "sync_job",
                receipt_id: "01800000-0000-7000-8000-000000000003",
                ...patch,
              },
            }),
          ),
      );
      const client = new CommandIntentClient(
        new AeroIdTransport("https://id.example/v1", () => "token", fetcher),
      );
      await expect(client.execute(intent)).rejects.toThrow("不会推断执行成功");
      expect(fetcher).toHaveBeenCalledOnce();
    }
  });
  it("accepts Go-to-PostgreSQL timestamp precision without dropping microseconds or extending expiry", async () => {
    const intent = {
      ...preparedIntent(),
      created_at: "2030-01-01T12:00:00.123456789Z",
      expires_at: "2030-01-01T12:05:00.123456789Z",
    };
    const result: CommandIntent = {
      ...intent,
      version: 2,
      status: "executed",
      receipt_kind: "sync_job",
      receipt_id: "01800000-0000-7000-8000-000000000003",
      created_at: "2030-01-01T12:00:00.123456Z",
      expires_at: "2030-01-01T20:05:00.123456+08:00",
    };
    const fetcher = vi.fn(
      async () => new Response(JSON.stringify({ data: result })),
    );
    const client = new CommandIntentClient(
      new AeroIdTransport("https://id.example/v1", () => "token", fetcher),
    );
    expect(await client.execute(intent)).toEqual(result);
    // Same JavaScript millisecond, but a different PostgreSQL microsecond.
    fetcher.mockImplementation(
      async () =>
        new Response(
          JSON.stringify({
            data: { ...result, expires_at: "2030-01-01T12:05:00.123457Z" },
          }),
        ),
    );
    await expect(client.execute(intent)).rejects.toThrow("不会推断执行成功");
  });

  it("binds Operation preview to root type, step order, source scope and compensation", async () => {
    const plan: CommandPlan = {
      kind: "operation",
      operation_type: "sync",
      datasets: ["aero-im.workspaces", "aero-vault.usage"],
      steps: [
        {
          step_key: "im",
          source: "aero-im",
          action: "summary",
          request_payload: { datasets: ["aero-im.workspaces"] },
        },
        {
          step_key: "vault",
          source: "aero-vault",
          action: "summary",
          request_payload: { datasets: ["aero-vault.usage"] },
        },
      ],
    };
    for (const changed of [
      { ...plan, steps: [...plan.steps!].reverse() },
      { ...plan, operation_type: "source.command" as const },
      {
        ...plan,
        steps: [
          { ...plan.steps![0], compensation_action: "erase" },
          plan.steps![1],
        ],
      },
    ]) {
      const intent = commandTestIntent(changed);
      const fetcher = vi.fn(
        async () => new Response(JSON.stringify({ data: intent })),
      );
      const client = new CommandIntentClient(
        new AeroIdTransport("https://id.example/v1", () => "token", fetcher),
      );
      await expect(
        client.prepare(plan, "original-key", intent.account_id),
      ).rejects.toThrow("不会推断执行成功");
    }
    expect(() =>
      parseCommandIntent(
        commandTestIntent({
          ...plan,
          steps: [{ ...plan.steps![0], request_payload: { datasets: [] } }],
        }),
      ),
    ).toThrow();
    expect(() =>
      parseCommandIntent(
        commandTestIntent({
          ...plan,
          request_payload: [] as unknown as Record<string, unknown>,
        }),
      ),
    ).toThrow();
  });
});
