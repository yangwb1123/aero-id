import type { CommandPlan } from "../api/commandIntent";
import { browserCommandContext } from "./commandEligibility";
import { loadJobIntents, saveJobIntents } from "./jobIntentStorage";

export function legacyDirectCommandExists(
  storage: Storage,
  scope: string,
  accountID: string,
): boolean {
  if (storage.getItem(`aero-id:erase-intent:${accountID}`)) return true;
  return (
    accountID === browserCommandContext(scope)?.accountID &&
    Boolean(
      storage.getItem(`aero-id:job-intents:v1:${encodeURIComponent(scope)}`),
    )
  );
}
// These keys already name the same server endpoint and caller scope. They may
// be resumed only for the exact original Sync/Export set. Direct API keys above
// are NEVER converted into server-preview keys.
export function originalPreviewKey(
  storage: Storage,
  scope: string,
  accountID: string,
  plan: CommandPlan,
): string | undefined {
  if (accountID !== browserCommandContext(scope)?.accountID) return undefined;
  const oldScope = `${scope}:server-preview-v1`;
  const raw = storage.getItem(
    `aero-id:job-intents:v1:${encodeURIComponent(oldScope)}`,
  );
  if (!raw) return undefined;
  const intents = loadJobIntents(storage, oldScope);
  let values: unknown;
  try {
    values = JSON.parse(raw);
  } catch {
    throw new Error("旧预览记录无法核实；不能换键创建第二个命令。");
  }
  if (
    !Array.isArray(values) ||
    values.length !== Object.keys(intents).length ||
    !values.length
  )
    throw new Error("旧预览记录无法核实；不能换键创建第二个命令。");
  const intent =
    intents[
      JSON.stringify({
        jobType: plan.kind,
        datasets: [...plan.datasets].sort(),
      })
    ];
  if (!intent)
    throw new Error("旧预览尚未确认；请先以原范围和原键核实，不能更改命令。");
  return intent.idempotencyKey;
}
export function resolveOriginalPreviewKey(
  storage: Storage,
  scope: string,
  plan: CommandPlan,
  idempotencyKey: string,
): void {
  const oldScope = `${scope}:server-preview-v1`;
  const intents = loadJobIntents(storage, oldScope);
  const signature = JSON.stringify({
    jobType: plan.kind,
    datasets: [...plan.datasets].sort(),
  });
  if (intents[signature]?.idempotencyKey === idempotencyKey)
    delete intents[signature];
  saveJobIntents(storage, oldScope, intents);
}
