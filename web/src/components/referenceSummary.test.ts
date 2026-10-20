import { describe, expect, it } from "vitest";
import {
  referenceInvalid,
  referenceMissing,
  referenceSummary,
} from "./referenceSummary";

describe("approved reference fields", () => {
  it("keeps opaque IDs and roles exact without deriving tenant, ACL or aliases", () => {
    const value = referenceSummary(
      {
        source: "aero-im",
        scope_type: "workspace",
        scope_id: " /opaque:7 ",
        source_member_id: "native",
        role: "owner",
        status: "active",
        version: 0,
        last_synced_at: "2026-10-16T00:00:00Z",
        data: { tenant_id: "forbidden", access_token: "secret" },
      },
      "memberships",
    );
    expect(value.scope_label).toBe("IM 工作空间");
    expect(value.scope_id).toBe(" /opaque:7 ");
    expect(value.role).toBe("owner");
    expect(value.version).toBe("0");
    expect(JSON.stringify(value)).not.toMatch(
      /forbidden|secret|tenant_id|access_token/,
    );
    expect(value.generated_at).toBe(referenceMissing);
  });
  it.each([undefined, null, ""])(
    "does not fill missing field %j from alternate or nested fields",
    (missing) => {
      const value = referenceSummary(
        {
          source: "aero-vault",
          scope_type: missing,
          scope_id: missing,
          source_account_id: missing,
          external_id: "not-used",
          tenant_id: "not-used",
          data: { scope_id: "not-used" },
        },
        "sources",
      );
      expect(value.native_id).toBe(referenceMissing);
      expect(value.scope_id).toBe(referenceMissing);
      expect(value.scope_label).toBe("Vault 作用域类型未识别");
      expect(value.status_label).toBe("状态未确认");
      expect(JSON.stringify(value)).not.toContain("not-used");
    },
  );
  it.each([[], {}, true, 2, "x".repeat(513), "line\nbreak"])(
    "does not expand or coerce invalid ID %j",
    (bad) => {
      expect(
        referenceSummary({ source_account_id: bad }, "sources").native_id,
      ).toBe(referenceInvalid);
    },
  );
  it.each([-1, 0.5, "42", Number.MAX_SAFE_INTEGER + 1])(
    "rejects unsafe version %j",
    (bad) => {
      expect(referenceSummary({ version: bad }, "sources").version).toBe(
        referenceInvalid,
      );
    },
  );
  it("does not promote unknown source/type/status or unzoned time", () => {
    const value = referenceSummary(
      {
        source: "unregistered",
        scope_type: "organization",
        status: "allowed",
        last_synced_at: "2026-10-16T00:00:00",
      },
      "memberships",
    );
    expect(value.scope_label).toBe("来源未识别 作用域类型未识别");
    expect(value.status_label).toBe("状态未确认");
    expect(value.last_synced_at).toBe(referenceInvalid);
  });
});
