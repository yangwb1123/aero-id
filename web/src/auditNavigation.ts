// Navigation carries only an explicit local filter and an original task pointer.
// It is neither a grant nor evidence that the filtered history is complete.
export interface AuditNavigation {
  taskPage: "jobs" | "operations";
  taskID: string;
  filter: "operation" | "request";
  value: string;
}
const uuid = /^[\da-f]{8}-(?:[\da-f]{4}-){3}[\da-f]{12}$/i;
const validID = (value: string) =>
  uuid.test(value) && value !== "00000000-0000-0000-0000-000000000000";
function validRequestID(value: string): boolean {
  return (
    value.length > 0 &&
    value.length <= 512 &&
    value.trim() === value &&
    !/\p{Cc}|\p{Cs}/u.test(value)
  );
}
export function parseAuditNavigation(
  route: string,
): AuditNavigation | undefined {
  if (!route.startsWith("audit?") || route.length > 2400) return undefined;
  const query = new URLSearchParams(route.slice(6));
  const keys = [...query.keys()];
  if (keys.length !== 3 || new Set(keys).size !== 3) return undefined;
  const taskPage = query.get("from");
  const taskID = query.get("task_id") ?? "";
  if ((taskPage !== "jobs" && taskPage !== "operations") || !validID(taskID))
    return undefined;
  if (query.has("operation_id")) {
    const value = query.get("operation_id")!;
    return taskPage === "operations" && value === taskID
      ? { taskPage, taskID, filter: "operation", value }
      : undefined;
  }
  const value = query.get("request_id") ?? "";
  return query.has("request_id") && validRequestID(value)
    ? { taskPage, taskID, filter: "request", value }
    : undefined;
}
export function taskAuditHref(
  taskPage: "jobs" | "operations",
  taskID: unknown,
  filter: "operation" | "request",
  value: unknown,
): string | undefined {
  if (typeof taskID !== "string" || typeof value !== "string") return undefined;
  const query = new URLSearchParams({
    [filter === "operation" ? "operation_id" : "request_id"]: value,
    from: taskPage,
    task_id: taskID,
  });
  const route = `audit?${query}`;
  return parseAuditNavigation(route) ? `#/${route}` : undefined;
}
