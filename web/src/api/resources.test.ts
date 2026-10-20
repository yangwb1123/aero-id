import { describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "./aeroId";
function clientFor(data: unknown) {
  const fetcher = vi.fn<typeof fetch>(
    async () =>
      new Response(JSON.stringify({ data }), {
        headers: { "Content-Type": "application/json" },
      }),
  );
  return {
    client: new AeroIdClient(
      "https://id.example/v1",
      () => "test-only",
      fetcher,
    ),
    fetcher,
  };
}
describe("resources aggregate API shape and original GET", () => {
  it.each([
    {},
    { snapshots: null, partial: false },
    { snapshots: [null], partial: false },
    { snapshots: [], partial: "false" },
    {
      snapshots: [],
      partial: false,
      consistency: { secret: "must-not-render" },
    },
    { snapshots: [], partial: false, source_errors: null },
    { snapshots: [], partial: false, source_errors: { "aero-vault": {} } },
    { snapshots: [], partial: false, stale_datasets: "aero-vault.usage" },
    { snapshots: Array.from({ length: 65 }, () => ({})), partial: false },
  ])(
    "rejects malformed aggregate instead of rendering a successful empty inventory: %j",
    async (data) => {
      const { client, fetcher } = clientFor(data);
      await expect(client.getResources()).rejects.toMatchObject({
        code: "response.invalid",
        message: "资源摘要响应无效；请显式重读，不会触发来源同步",
      });
      expect(fetcher).toHaveBeenCalledOnce();
    },
  );
  it("accepts the HTTP nil stale_datasets and preserves eventual datasets and per-read cancellation", async () => {
    const { client, fetcher } = clientFor({
      snapshots: [],
      partial: true,
      source_errors: { "aero-vault": "source.not_configured" },
      stale_datasets: null,
    });
    const controller = new AbortController();
    await expect(
      client.getResources(["aero-vault.usage"], controller.signal),
    ).resolves.toMatchObject({ snapshots: [], partial: true });
    const [input, init] = fetcher.mock.calls[0]!;
    const url = new URL(String(input));
    expect(url.pathname).toBe("/v1/me/resources");
    expect(url.searchParams.getAll("dataset")).toEqual(["aero-vault.usage"]);
    expect(url.searchParams.get("consistency")).toBe("eventual");
    expect(init?.method).toBe("GET");
    expect(init?.body).toBeUndefined();
    expect(new Headers(init?.headers).has("Idempotency-Key")).toBe(false);
  });
  it("does not start a resource fetch after external cancellation", async () => {
    const { client, fetcher } = clientFor({ snapshots: [], partial: false });
    const controller = new AbortController();
    controller.abort();
    await expect(
      client.getResources([], controller.signal),
    ).rejects.toMatchObject({ code: "request.canceled" });
    expect(fetcher).not.toHaveBeenCalled();
  });
});
