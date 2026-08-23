import { describe, expect, it } from "vitest";
import { normalizeHoldExpiry } from "./CompliancePage";

describe("normalizeHoldExpiry", () => {
  it("normalizes a future RFC3339 timestamp and preserves an indefinite hold", () => {
    expect(normalizeHoldExpiry("", Date.parse("2026-01-01T00:00:00Z"))).toBe(
      undefined,
    );
    expect(
      normalizeHoldExpiry(
        "2027-01-02T03:04:05+02:00",
        Date.parse("2026-01-01T00:00:00Z"),
      ),
    ).toBe("2027-01-02T01:04:05.000Z");
  });

  it("rejects timezone-free and expired timestamps", () => {
    expect(() =>
      normalizeHoldExpiry(
        "2027-01-02T03:04:05",
        Date.parse("2026-01-01T00:00:00Z"),
      ),
    ).toThrow("RFC3339");
    expect(() =>
      normalizeHoldExpiry(
        "2025-01-02T03:04:05Z",
        Date.parse("2026-01-01T00:00:00Z"),
      ),
    ).toThrow("晚于当前时间");
    expect(() =>
      normalizeHoldExpiry(
        "2027-02-30T03:04:05Z",
        Date.parse("2026-01-01T00:00:00Z"),
      ),
    ).toThrow("到期时间无效");
  });
});
