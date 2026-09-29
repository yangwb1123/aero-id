import type { JsonRecord } from "../types";
export interface PendingRegionPromotion {
  targetRegion: string;
  expectedEpoch: number;
  signature: string;
  idempotencyKey: string;
}

export function regionEpoch(directory: JsonRecord): number {
  const value = directory.fenced_epoch;
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0)
    throw new Error("区域目录缺少有效的 Fenced Epoch");
  return value;
}

export function verifyDirectory(
  directory: JsonRecord,
  accountID: string,
): JsonRecord {
  if (directory.account_id !== accountID)
    throw new Error("区域目录响应的 Account ID 不匹配");
  regionEpoch(directory);
  if (typeof directory.home_region !== "string" || !directory.home_region)
    throw new Error("区域目录缺少 Home Region");
  return directory;
}

export function verifyPromotion(
  result: JsonRecord,
  accountID: string,
  pending: PendingRegionPromotion,
): void {
  if (
    result.account_id !== accountID ||
    result.target_region !== pending.targetRegion ||
    regionEpoch(result) <= pending.expectedEpoch
  )
    throw new Error("区域提升响应与请求的 CAS 结果不一致");
}
