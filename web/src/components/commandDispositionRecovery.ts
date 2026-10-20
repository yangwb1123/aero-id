import type { AeroIdClient } from "../api/aeroId";
import { parseCommandIntent, type CommandIntent } from "../api/commandIntent";
import { sameCommandCaller } from "./commandEligibility";
import {
  commandMetadataSignature,
  maxCommandPreviews,
  type StoredCommandPreview,
} from "./commandPreviewStorage";

// Browser enum/hash pointers are not disposition proof. Before a new key,
// re-read EVERY relevant retained closure at the authority, with one overall
// deadline and no parallel fan-out. Callback acknowledged receipts prove local
// queue admission only, not receiver completion. NotFound/prepared never permits it.
export async function verifyCommandDispositions(
  client: AeroIdClient,
  scope: string,
  records: StoredCommandPreview[],
  signal: AbortSignal,
): Promise<void> {
  if (records.length > maxCommandPreviews)
    throw new Error("处置恢复指针超出预算；未生成新键。");
  for (const record of records) {
    if (signal.aborted || !record.intentID)
      throw new Error("原意图处置未核实；未生成新键。");
    const intent = parseCommandIntent(
      await client.commands.get(record.intentID, signal),
    );
    if (
      !matchingCommandClosure(intent, record) ||
      intent.intent_id !== record.intentID ||
      intent.account_id !== record.accountID ||
      intent.plan.kind !== record.kind ||
      !sameCommandCaller(intent, scope) ||
      (await commandMetadataSignature(intent)) !== record.metadataSignature
    )
      throw new Error(
        "原意图尚无绑定的服务端关闭证明；未生成新键，请核实原回执。",
      );
  }
  if (signal.aborted) throw new Error("原意图处置查询超出预算；未生成新键。");
}
function matchingCommandClosure(
  intent: CommandIntent,
  record: StoredCommandPreview,
): boolean {
  if (record.abandoned) return intent.status === "abandoned";
  return Boolean(
    record.acknowledged &&
    record.kind === "callback_replay" &&
    intent.status === "executed" &&
    intent.receipt_kind === "callback_replay" &&
    intent.receipt_id === record.receiptID &&
    intent.receipt_id === intent.intent_id,
  );
}
