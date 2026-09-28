import { describe, expect, it } from "vitest";
import { safeJsonPreview } from "./safeJsonPreview";

describe("safeJsonPreview", () => {
  it("redacts credential and content fields recursively, including camelCase names", () => {
    const output = safeJsonPreview({
      display_name: "Ada",
      accessToken: "bearer-secret",
      nested: [
        { mfa_secret: "mfa-secret", notification_id: "notification-1" },
        { latestMessage: "private-message", message_count: 3 },
      ],
    });

    expect(output).toContain('"display_name": "Ada"');
    expect(output).toContain('"notification_id": "notification-1"');
    expect(output).toContain('"message_count": 3');
    expect(output).not.toContain("bearer-secret");
    expect(output).not.toContain("mfa-secret");
    expect(output).not.toContain("private-message");
  });

  it("preserves opaque conversation references but omits source content fields", () => {
    const output = safeJsonPreview({
      conversation_id: "conversation-1",
      conversation: "private transcript",
      body: "message body",
    });

    expect(output).toContain('"conversation_id": "conversation-1"');
    expect(output).not.toContain("private transcript");
    expect(output).not.toContain("message body");
  });

  it("bounds nesting and rendered output size", () => {
    const nested = { a: { b: { c: { d: { e: { f: "deep" } } } } } };
    expect(safeJsonPreview(nested)).toContain('"truncated": true');
    expect(safeJsonPreview({ description: "x".repeat(70 * 1024) })).toBe(
      '{\n  "truncated": true\n}',
    );
  });
});
