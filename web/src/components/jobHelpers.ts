import type { JsonRecord } from "../types";

export interface JobCreateIntent {
  signature: string;
  idempotencyKey: string;
}

export function nextJobCreateIntent(
  command: { jobType: string; accountID?: string; datasets: string[] },
  current: JobCreateIntent | undefined,
  makeKey: () => string = () => crypto.randomUUID(),
): JobCreateIntent {
  const signature = JSON.stringify({
    ...command,
    datasets: [...command.datasets].sort(),
  });
  return current?.signature === signature
    ? current
    : { signature, idempotencyKey: makeKey() };
}

export function syncJobID(record: JsonRecord): string | undefined {
  const value = record.job_id ?? record.id;
  return typeof value === "string" && value ? value : undefined;
}
