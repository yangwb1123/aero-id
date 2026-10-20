import { afterEach, describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "../api/aeroId";
import ConnectionsPage from "./ConnectionsPage.vue";
import {
  commandButton,
  flushCommandUI,
  mountCommandTest,
  unmountCommandTests,
} from "../components/commandTestFixtures";

vi.mock("@iris-ui-kit/vue", async () =>
  (await import("../components/commandTestFixtures")).irisCommandTestStubs(),
);
const source = {
  id: "01800000-0000-7000-8000-000000000011",
  source: "aero-vault",
  source_account_id: "native-account",
  scope_type: "tenant",
  scope_id: "same-opaque-scope",
  status: "active",
  source_region: "local",
  version: 7,
  generated_at: "2026-10-16T00:00:00Z",
  last_synced_at: "2026-10-16T00:00:01Z",
  data: { secret: "UNAPPROVED-REFERENCE-CONTENT" },
};
const membership = {
  ...source,
  id: "01800000-0000-7000-8000-000000000012",
  source: "aero-im",
  scope_type: "workspace",
  source_member_id: "native-member",
  role: "owner",
};
const clients: AeroIdClient[] = [];
afterEach(() => {
  unmountCommandTests();
  clients.splice(0).forEach((client) => client.cancelPendingReads());
  vi.restoreAllMocks();
});
function mountFlow(
  options: {
    sourceError?: boolean;
    empty?: boolean;
    nextCursor?: string;
    reply?: (
      isSource: boolean,
      cursor: string | null,
    ) => Promise<Response> | undefined;
  } = {},
) {
  let fail = options.sourceError;
  const requests: {
    path: string;
    query: string;
    signal?: AbortSignal | null;
  }[] = [];
  const client = new AeroIdClient(
    "https://id.example/v1",
    () => "fixture-marker",
    async (input, init) => {
      expect(init?.method).toBe("GET");
      expect(new Headers(init?.headers).has("Idempotency-Key")).toBe(false);
      const url = new URL(String(input));
      requests.push({
        path: url.pathname,
        query: url.search,
        signal: init?.signal,
      });
      const isSource = url.pathname === "/v1/me/sources";
      const reply = options.reply?.(isSource, url.searchParams.get("cursor"));
      if (reply) return reply;
      if (isSource && fail)
        return new Response(
          JSON.stringify({
            error: {
              code: "account.sources_failed",
              message: "受控来源引用读取故障",
            },
          }),
          { status: 503 },
        );
      return new Response(
        JSON.stringify({
          data: {
            [isSource ? "sources" : "memberships"]: options.empty
              ? []
              : [isSource ? source : membership],
            next_cursor: options.nextCursor ?? "",
          },
        }),
      );
    },
  );
  clients.push(client);
  return {
    ...mountCommandTest(ConnectionsPage, { client }),
    requests,
    recover: () => {
      fail = false;
    },
  };
}

describe("readable source and membership references", () => {
  it("opens the original source scope without another request, returns, then reads the independent membership scope", async () => {
    const { root, requests } = mountFlow();
    await flushCommandUI();
    expect(root.textContent).toContain("tenant-a");
    const sources = root.querySelector<HTMLElement>(
      '[data-reference-list="sources"]',
    )!;
    commandButton(sources, "查看来源范围").click();
    await flushCommandUI();
    const detail = sources.querySelector<HTMLElement>(
      "[data-reference-details]",
    )!;
    expect(detail.textContent).toContain("Vault 租户");
    expect(detail.textContent).toContain("native-account");
    expect(detail.textContent).toContain("same-opaque-scope");
    expect(detail.textContent).toContain("2026-10-16T00:00:00Z");
    expect(detail.textContent).toContain("不是全局权限");
    expect(root.innerHTML).not.toContain("UNAPPROVED-REFERENCE-CONTENT");
    expect(requests).toHaveLength(2);
    commandButton(sources, "返回来源列表").click();
    await flushCommandUI();
    expect(sources.querySelector("[data-reference-details]")).toBeNull();
    const members = root.querySelector<HTMLElement>(
      '[data-reference-list="memberships"]',
    )!;
    commandButton(members, "查看成员范围").click();
    await flushCommandUI();
    const memberDetail = members.querySelector<HTMLElement>(
      "[data-reference-details]",
    )!;
    expect(memberDetail.textContent).toContain("IM 工作空间");
    expect(memberDetail.textContent).toContain("native-member");
    expect(memberDetail.textContent).toContain("owner");
    expect(memberDetail.textContent).toContain("不是全局权限");
    expect(requests).toHaveLength(2);
  });

  it("keeps membership results readable when source references fail and refreshes only the requested collection", async () => {
    const flow = mountFlow({ sourceError: true });
    await flushCommandUI();
    const members = flow.root.querySelector<HTMLElement>(
      '[data-reference-list="memberships"]',
    )!;
    commandButton(members, "查看成员范围").click();
    await flushCommandUI();
    expect(members.textContent).toContain("native-member");
    flow.recover();
    const sources = flow.root.querySelector<HTMLElement>(
      '[data-reference-list="sources"]',
    )!;
    commandButton(sources, "重试").click();
    await flushCommandUI();
    expect(flow.requests.map((item) => item.path)).toEqual([
      "/v1/me/sources",
      "/v1/me/memberships",
      "/v1/me/sources",
    ]);
    expect(members.textContent).toContain("native-member");
  });

  it("describes empty responses as query-scoped, not evidence of no source account or no membership", async () => {
    const { root, requests } = mountFlow({ empty: true });
    await flushCommandUI();
    expect(root.textContent).toContain("本次未返回来源引用");
    expect(root.textContent).toContain("本次未返回成员关系引用");
    expect(root.textContent).toContain("不证明来源没有账户或成员关系");
    expect(root.textContent).toContain("未提供 coverage/complete");
    expect(requests).toHaveLength(2);
  });

  it("keeps already viewed members and the original cursor on a rejected page, then explicitly retries it", async () => {
    let bad = true;
    const flow = mountFlow({
      nextCursor: "original-cursor",
      reply: (isSource, cursor) => {
        if (isSource || !cursor) return;
        return Promise.resolve(
          new Response(
            JSON.stringify({
              data: {
                memberships: bad
                  ? [null]
                  : [
                      {
                        ...membership,
                        id: "01800000-0000-7000-8000-000000000010",
                        scope_id: "next-scope",
                      },
                    ],
                next_cursor: "",
              },
            }),
          ),
        );
      },
    });
    await flushCommandUI();
    const members = flow.root.querySelector<HTMLElement>(
      '[data-reference-list="memberships"]',
    )!;
    commandButton(members, "加载更多").click();
    await flushCommandUI();
    expect(members.textContent).toContain("same-opaque-scope");
    expect(members.textContent).not.toContain("next-scope");
    const query = flow.requests.at(-1)?.query;
    bad = false;
    commandButton(members, "重试").click();
    await flushCommandUI();
    expect(flow.requests.at(-1)?.query).toBe(query);
    expect(members.textContent).toContain("next-scope");
    expect(members.textContent).toContain("same-opaque-scope");
    expect(
      [...members.querySelectorAll("button")].some(
        (button) => button.textContent === "加载更多",
      ),
    ).toBe(false);
  });

  it("does not let a slow source response hide members, and cancels its own fetch on unmount", async () => {
    let release!: (value: Response) => void;
    const held = new Promise<Response>((resolve) => {
      release = resolve;
    });
    const flow = mountFlow({
      reply: (isSource) => (isSource ? held : undefined),
    });
    try {
      await flushCommandUI();
      const members = flow.root.querySelector<HTMLElement>(
        '[data-reference-list="memberships"]',
      )!;
      commandButton(members, "查看成员范围").click();
      await flushCommandUI();
      expect(members.textContent).toContain("native-member");
      expect(flow.requests[0]?.signal?.aborted).toBe(false);
      unmountCommandTests();
      expect(flow.requests[0]?.signal?.aborted).toBe(true);
    } finally {
      release(
        new Response(
          JSON.stringify({ data: { sources: [source], next_cursor: "" } }),
        ),
      );
      await flushCommandUI();
    }
    expect(flow.requests).toHaveLength(2);
    expect(flow.root.textContent).not.toContain("native-account");
  });

  it("does not use a previous membership/owner record as current read eligibility", async () => {
    const flow = mountFlow();
    await flushCommandUI();
    const members = flow.root.querySelector<HTMLElement>(
      '[data-reference-list="memberships"]',
    )!;
    commandButton(members, "查看成员范围").click();
    await flushCommandUI();
    expect(members.textContent).toContain("owner");
    flow.report.value = undefined;
    await flushCommandUI();
    expect(members.querySelector("[data-reference-details]")).toBeNull();
    expect(members.textContent).not.toContain("native-member");
    expect(flow.requests).toHaveLength(2);
  });
});
