import type { JsonRecord } from "../types";
import { AeroIdTransport, requireDataEnvelope as dataOf } from "./transport";

export interface RegionPromotionInput {
  accountID: string;
  targetRegion: string;
  expectedEpoch: number;
  idempotencyKey: string;
}

export class RegionClient {
  constructor(private readonly transport: AeroIdTransport) {}

  async getDirectory(accountID: string): Promise<JsonRecord> {
    return dataOf(
      await this.transport.request(
        "GET",
        `/admin/regions/directory/${encodeURIComponent(accountID)}`,
      ),
    );
  }

  async promote(input: RegionPromotionInput): Promise<JsonRecord> {
    const result = dataOf(
      await this.transport.request("POST", "/admin/regions/promote", {
        idempotencyKey: input.idempotencyKey,
        body: {
          account_id: input.accountID,
          target_region: input.targetRegion,
          expected_epoch: input.expectedEpoch,
        },
      }),
    );
    if (
      result.account_id !== input.accountID ||
      result.target_region !== input.targetRegion ||
      typeof result.fenced_epoch !== "number"
    )
      throw new Error("区域提升响应与请求不匹配");
    this.transport.pinWriteEpoch(input.targetRegion, result.fenced_epoch);
    return result;
  }
}
