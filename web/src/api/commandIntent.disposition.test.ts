import { describe, expect, it, vi } from "vitest";
import { AeroIdTransport, AeroIdError } from "./transport";
import {
  CommandIntentClient,
  commandLookupKeyDigest,
  parseCommandIntent,
} from "./commandIntent";
import {
  abandonedTestIntent,
  commandTestIntent,
  executedTestIntent,
} from "../components/commandTestFixtures";

function client(fetcher: typeof fetch) {
  return new CommandIntentClient(
    new AeroIdTransport("https://id.example/v1", () => "memory-token", fetcher),
  );
}
describe("original-key lookup and explicit admission disposition", () => {
  it("looks up using only GET and verifies the original key digest without a URL key", async () => {
    const original = commandTestIntent();
    const key = "original-key";
    const digest = await commandLookupKeyDigest(key);
    const fetcher = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({ data: { intent: original, key_digest: digest } }),
        ),
      );
    expect(await client(fetcher).findByKey(key)).toEqual(original);
    expect(String(fetcher.mock.calls[0][0])).toBe(
      "https://id.example/v1/command-intents/by-key",
    );
    expect(fetcher.mock.calls[0][1]?.method).toBe("GET");
    expect(
      new Headers(fetcher.mock.calls[0][1]?.headers).get("Idempotency-Key"),
    ).toBe(key);
    expect(fetcher.mock.calls[0][1]?.body).toBeUndefined();
    fetcher.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          data: { intent: original, key_digest: `v1:${"0".repeat(64)}` },
        }),
      ),
    );
    await expect(client(fetcher).findByKey(key)).rejects.toThrow(
      "不会推断执行成功",
    );
  });
  it("does not prepare after a missing/failed lookup and rejects invalid keys before HTTP", async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(
      new Response(
        JSON.stringify({
          error: { code: "command.not_found", message: "not found" },
        }),
        { status: 404 },
      ),
    );
    const commands = client(fetcher);
    await expect(commands.findByKey("original-key")).rejects.toBeInstanceOf(
      AeroIdError,
    );
    await expect(commands.findByKey(" ")).rejects.toThrow("不会推断执行成功");
    expect(fetcher).toHaveBeenCalledOnce();
  });
  it("posts only original version/digest for abandonment and never renews or executes", async () => {
    const original = commandTestIntent();
    const closed = abandonedTestIntent(original);
    const fetcher = vi
      .fn<typeof fetch>()
      .mockResolvedValue(new Response(JSON.stringify({ data: closed })));
    expect(await client(fetcher).abandon(original)).toEqual(closed);
    expect(String(fetcher.mock.calls[0][0])).toContain(
      `/${original.intent_id}/abandon`,
    );
    expect(JSON.parse(fetcher.mock.calls[0][1]?.body as string)).toEqual({
      version: 1,
      confirmation_digest: original.confirmation_digest,
    });
    expect(fetcher).toHaveBeenCalledOnce();
    await expect(client(fetcher).execute(closed)).rejects.toThrow(
      "不会推断执行成功",
    );
    expect(fetcher).toHaveBeenCalledOnce();
  });
  it("returns an already admitted receipt without claiming it was canceled", async () => {
    const original = commandTestIntent();
    const receipt = executedTestIntent(original);
    const fetcher = vi
      .fn<typeof fetch>()
      .mockResolvedValue(new Response(JSON.stringify({ data: receipt })));
    expect((await client(fetcher).abandon(original)).receipt_id).toBe(
      receipt.receipt_id,
    );
  });
  it("rejects purported closure with a receipt, bad timestamp, changed lifetime or prepared status", async () => {
    const original = commandTestIntent();
    const closed = abandonedTestIntent(original);
    for (const patch of [
      { receipt_id: executedTestIntent(original).receipt_id },
      { abandoned_at: undefined },
      { abandoned_at: "2001-01-01T00:00:00Z" },
      { version: 3 },
      {
        expires_at: new Date(Date.parse(original.expires_at) + 1).toISOString(),
      },
      { status: "prepared", version: 1, abandoned_at: undefined },
    ]) {
      const fetcher = vi
        .fn<typeof fetch>()
        .mockResolvedValue(
          new Response(JSON.stringify({ data: { ...closed, ...patch } })),
        );
      await expect(client(fetcher).abandon(original)).rejects.toThrow(
        "不会推断执行成功",
      );
      expect(fetcher).toHaveBeenCalledOnce();
    }
    expect(() =>
      parseCommandIntent({ ...original, abandoned_at: closed.abandoned_at }),
    ).toThrow();
  });
});
