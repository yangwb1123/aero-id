// Root task states from the existing HTTP/domain families, not step states,
// transition proofs, current authorization or remote-completion receipts.
const taskStatuses = {
  job_id: [
    "pending",
    "running",
    "completed",
    "partially_completed",
    "failed",
    "unknown",
  ],
  operation_id: [
    "requested",
    "running",
    "waiting",
    "unknown",
    "partially_completed",
    "completed",
    "failed",
    "compensating",
  ],
};

export function isTaskStatus(
  value: unknown,
  field: "job_id" | "operation_id",
): value is string {
  return typeof value === "string" && taskStatuses[field].includes(value);
}
