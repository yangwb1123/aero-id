import type { JsonRecord, PageData } from "../types";
import { AeroIdError, requireDataEnvelope } from "./transport";

export function accountReferencePage(
  value: unknown,
  field: "sources" | "memberships",
): PageData<JsonRecord> {
  const data = requireDataEnvelope(value);
  const items = data[field];
  if (
    !Array.isArray(items) ||
    items.length > 50 ||
    !items.every(
      (item) =>
        item !== null && typeof item === "object" && !Array.isArray(item),
    ) ||
    (data.next_cursor !== undefined && typeof data.next_cursor !== "string")
  )
    throw new AeroIdError(
      0,
      "response.invalid",
      "引用列表响应无效；请显式重读原列表，不会同步来源",
    );
  return { items, nextCursor: data.next_cursor as string | undefined };
}
