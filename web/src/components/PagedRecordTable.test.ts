import { describe, expect, it } from "vitest";
import { mergePage } from "./PagedRecordTable";

describe("mergePage", () => {
  it("appends a keyset page and advances its cursor", () => {
    expect(
      mergePage(
        { items: [{ id: "3" }], nextCursor: "3" },
        { items: [{ id: "2" }], nextCursor: "2" },
        "3",
      ),
    ).toEqual({ items: [{ id: "3" }, { id: "2" }], nextCursor: "2" });
  });

  it("rejects a cursor that does not advance", () => {
    expect(() =>
      mergePage(
        { items: [{ id: "3" }], nextCursor: "3" },
        { items: [{ id: "3" }], nextCursor: "3" },
        "3",
      ),
    ).toThrow("分页游标没有前进");
  });
});
