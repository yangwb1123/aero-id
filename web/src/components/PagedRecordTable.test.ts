import { describe, expect, it } from "vitest";
import { mergePage } from "./table";

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

  it("rejects a cursor cycle across already-consumed pages", () => {
    const consumed = new Set(["cursor-a", "cursor-b"]);

    expect(() =>
      mergePage(
        { items: [{ id: "1" }], nextCursor: "cursor-c" },
        { items: [{ id: "2" }], nextCursor: "cursor-a" },
        "cursor-c",
        consumed,
      ),
    ).toThrow("分页游标出现循环");
  });

  it("rejects a cursor that does not advance", () => {
    expect(() =>
      mergePage(
        { items: [{ id: "3" }], nextCursor: "3" },
        { items: [{ id: "3" }], nextCursor: "3" },
        "3",
      ),
    ).toThrow("分页游标出现循环");
  });
});
