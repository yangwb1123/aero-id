import { computed, onBeforeUnmount, ref, watch } from "vue";
import type { AeroIdClient } from "../api/aeroId";
import {
  parseCommandIntent,
  type CommandIntent,
  type CommandPlan,
} from "../api/commandIntent";
import {
  assertCommandPointerTransition,
  commandPlanSignature,
  commandMetadataSignature,
  commandUUID,
  loadCommandPreviews,
  maxCommandPreviews,
  saveCommandPreviews,
  type StoredCommandPreview,
} from "../components/commandPreviewStorage";
import {
  commandAvailable,
  sameCapabilityContext,
  sameCommandCaller,
} from "../components/commandEligibility";
import { useAccountScope } from "./accountContext";
import {
  useCapabilities,
  useRefreshCapabilities,
  refreshAfterAuthorizationError,
} from "./capabilityContext";
import { verifyCommandDispositions } from "../components/commandDispositionRecovery";
import {
  legacyDirectCommandExists,
  originalPreviewKey,
  resolveOriginalPreviewKey,
} from "../components/legacyCommandPreviews";

interface Options {
  client: () => AeroIdClient;
  channel: string;
  accountID: () => string | undefined;
  onReceipt: (intent: CommandIntent) => void;
  legacyBlocked?: () => boolean;
  legacyAccountCommands?: boolean;
}

export function useCommandPreview(options: Options) {
  const scope = useAccountScope();
  const capabilities = useCapabilities();
  const refreshCapabilities = useRefreshCapabilities();
  const preview = ref<CommandIntent>();
  const busy = ref(false);
  const error = ref<Error>();
  const records = ref<StoredCommandPreview[]>([]);
  const storageError = ref<Error>();
  const unresolved = computed(() =>
    records.value.filter(
      (record) =>
        record.channel === options.channel &&
        !record.abandoned &&
        !record.acknowledged,
    ),
  );
  const abandoned = computed(() =>
    records.value.filter(
      (record) =>
        record.channel === options.channel &&
        record.accountID === options.accountID()?.toLowerCase() &&
        record.abandoned,
    ),
  );
  const acknowledged = computed(() =>
    records.value.filter(
      (record) =>
        record.channel === options.channel &&
        record.accountID === options.accountID()?.toLowerCase() &&
        record.acknowledged,
    ),
  );
  const pending = computed(() =>
    unresolved.value.find(
      (record) => record.accountID === options.accountID()?.toLowerCase(),
    ),
  );
  let active = true;
  let generation = 0;
  let readController: AbortController | undefined;
  let storage: Storage | undefined;
  try {
    storage = window.sessionStorage;
  } catch {
    /* Memory-only, never automatic writes. */
  }

  function loadRecords(key: string | undefined): StoredCommandPreview[] {
    if (!storage) return records.value;
    try {
      return loadCommandPreviews(storage, key);
    } catch (reason) {
      if (reason instanceof DOMException) {
        storage = undefined;
        return records.value;
      }
      throw reason;
    }
  }
  watch(
    () => scope?.(),
    (key) => {
      generation += 1;
      readController?.abort();
      preview.value = undefined;
      busy.value = false;
      error.value = undefined;
      storageError.value = undefined;
      records.value = [];
      try {
        records.value = loadRecords(key);
      } catch (reason) {
        storageError.value = asError(
          reason,
          "未确认意图记录不可用；请先核实原回执。",
        );
        error.value = storageError.value;
      }
    },
    { immediate: true },
  );
  onBeforeUnmount(() => {
    active = false;
    generation += 1;
    readController?.abort();
  });
  const current = (key: string | undefined, token: number) =>
    active && key === scope?.() && token === generation;
  function legacyBlocked(accountID: string): boolean {
    try {
      return Boolean(
        options.legacyBlocked?.() ||
        (options.legacyAccountCommands !== false &&
          storage &&
          scope?.() &&
          legacyDirectCommandExists(storage, scope()!, accountID)),
      );
    } catch (reason) {
      if (reason instanceof DOMException) {
        storage = undefined;
        return Boolean(options.legacyBlocked?.());
      }
      return true;
    }
  }
  function allowed(
    plan: CommandPlan,
    accountID = options.accountID(),
  ): boolean {
    return Boolean(
      accountID &&
      !storageError.value &&
      !legacyBlocked(accountID.toLowerCase()) &&
      sameCapabilityContext(capabilities?.(), scope?.()) &&
      commandAvailable(capabilities?.(), plan, accountID.toLowerCase()),
    );
  }
  function persist(
    key: string,
    record: StoredCommandPreview,
    remove = false,
  ): void {
    const latest = loadRecords(key);
    const occupied = latest.find(
      (item) =>
        item.channel === record.channel &&
        item.accountID === record.accountID &&
        !item.abandoned &&
        !item.acknowledged,
    );
    if (occupied && occupied.idempotencyKey !== record.idempotencyKey)
      throw new Error(
        "当前恢复指针已属于另一原意图；不会替换或清除其键，请核实原回执。",
      );
    assertCommandPointerTransition(
      latest.find((item) => item.idempotencyKey === record.idempotencyKey),
      record,
    );
    const values = latest.filter(
      (item) => item.idempotencyKey !== record.idempotencyKey,
    );
    if (!remove) values.push(record);
    if (values.length > maxCommandPreviews)
      throw new Error("存在过多未确认意图；请先核查原回执。");
    if (storage) {
      try {
        saveCommandPreviews(storage, key, values);
      } catch (reason) {
        if (reason instanceof DOMException) storage = undefined;
        else throw reason;
      }
    }
    records.value = values;
  }
  function validate(
    intent: CommandIntent,
    record: StoredCommandPreview,
    key: string,
  ): void {
    parseCommandIntent(intent);
    if (
      !sameCommandCaller(intent, key) ||
      intent.account_id !== record.accountID ||
      intent.plan.kind !== record.kind ||
      (record.intentID && record.intentID !== intent.intent_id) ||
      (record.receiptID &&
        (intent.status !== "executed" ||
          record.receiptID !== intent.receipt_id)) ||
      (record.abandoned && intent.status !== "abandoned")
    )
      throw new Error("命令预览与原身份/目标/意图不一致；保留原键，不会执行。");
  }
  async function receive(
    intent: CommandIntent,
    record: StoredCommandPreview,
    key: string,
    token: number,
  ): Promise<void> {
    validate(intent, record, key);
    if (
      intent.plan.kind === "callback_replay" &&
      (await commandPlanSignature(intent.account_id, intent.plan)) !==
        record.signature
    )
      throw new Error(
        "重放目标/版本与原意图签名不一致；保留原键，不会执行或换键。",
      );
    const metadataSignature = await commandMetadataSignature(intent);
    if (!current(key, token)) return;
    if (
      record.metadataSignature &&
      record.metadataSignature !== metadataSignature
    )
      throw new Error("原意图计划/身份/有效期发生变化；不会续期、执行或换键。");
    const updated = {
      ...record,
      intentID: intent.intent_id,
      metadataSignature,
    };
    if (intent.status === "abandoned") {
      persist(key, { ...updated, abandoned: true });
      resolveLegacy(intent, key, record.idempotencyKey);
      preview.value = undefined;
      return;
    }
    if (intent.status === "prepared") {
      persist(key, updated);
      preview.value = intent;
      return;
    }
    const kind =
      intent.plan.kind === "callback_replay"
        ? "callback_replay"
        : intent.plan.kind === "operation"
          ? "operation"
          : "sync_job";
    if (intent.receipt_kind !== kind || !commandUUID(intent.receipt_id))
      throw new Error("回执未确认；保留原意图，不会生成第二个命令。");
    persist(
      key,
      { ...updated, receiptID: intent.receipt_id },
      !["erase", "callback_replay"].includes(intent.plan.kind),
    );
    resolveLegacy(intent, key, record.idempotencyKey);
    preview.value = undefined;
    options.onReceipt(intent);
  }
  function resolveLegacy(
    intent: CommandIntent,
    key: string,
    idempotencyKey: string,
  ): void {
    if (storage && options.legacyAccountCommands !== false)
      resolveOriginalPreviewKey(storage, key, intent.plan, idempotencyKey);
  }
  async function verifyPriorClosures(
    key: string,
    accountID: string,
  ): Promise<void> {
    records.value = loadRecords(key);
    const closed = records.value.filter(
      (item) =>
        item.channel === options.channel &&
        item.accountID === accountID &&
        (item.abandoned || item.acknowledged),
    );
    if (!closed.length || pending.value) return;
    const controller = new AbortController();
    readController = controller;
    const timer = setTimeout(() => controller.abort(), 15_000);
    try {
      await verifyCommandDispositions(
        options.client(),
        key,
        closed,
        controller.signal,
      );
    } finally {
      clearTimeout(timer);
    }
  }
  function prepareRecord(
    key: string,
    accountID: string,
    plan: CommandPlan,
    signature: string,
  ): StoredCommandPreview {
    // Reload before choosing a key: another mounted view may have an intent.
    records.value = loadRecords(key);
    const previous = pending.value;
    if (previous) {
      if (previous.signature !== signature || previous.kind !== plan.kind)
        throw new Error(
          "原意图尚未核实；不能更改范围或换键创建第二个命令，请查询原意图。",
        );
      return previous;
    }
    const record = {
      channel: options.channel,
      accountID,
      kind: plan.kind,
      signature,
      idempotencyKey:
        (storage && options.legacyAccountCommands !== false
          ? originalPreviewKey(storage, key, accountID, plan)
          : undefined) ?? crypto.randomUUID(),
    };
    persist(key, record); // Before the first POST, including an ambiguous prepare.
    return record;
  }
  function prepareContext(plan: CommandPlan) {
    const key = scope?.();
    const accountID = options.accountID()?.toLowerCase();
    if (key && commandUUID(accountID) && allowed(plan, accountID))
      return { key, accountID };
    error.value =
      storageError.value ??
      new Error(
        accountID && legacyBlocked(accountID)
          ? "旧接口存在未确认请求或回执；请先核实旧任务，不会换接口创建第二个命令。"
          : "当前命令能力、来源或账户上下文尚未确认；未发送预览请求。",
      );
    return undefined;
  }
  async function prepare(plan: CommandPlan): Promise<void> {
    if (busy.value) return;
    error.value = undefined;
    const target = prepareContext(plan);
    if (!target) return;
    const { key, accountID } = target;
    const token = generation;
    busy.value = true;
    try {
      const signature = await commandPlanSignature(accountID, plan);
      if (!current(key, token)) return;
      await verifyPriorClosures(key, accountID);
      if (!current(key, token)) return;
      if (!allowed(plan, accountID))
        throw new Error("当前动作能力已变化；未发送预览请求，保留原意图。");
      const record = prepareRecord(key, accountID, plan, signature);
      if (record.intentID) {
        await recover(record);
        return;
      }
      const intent = await options
        .client()
        .commands.prepare(plan, record.idempotencyKey, accountID);
      if (current(key, token)) await receive(intent, record, key, token);
    } catch (reason) {
      if (current(key, token)) {
        error.value = asError(reason, "预览回应未确认；保留原计划和键。");
        refreshAfterAuthorizationError(reason, refreshCapabilities);
      }
    } finally {
      if (current(key, token)) busy.value = false;
    }
  }
  async function recover(record = pending.value): Promise<void> {
    if (!record || !scope?.()) {
      error.value = new Error(
        "尚无原键或作用域；请先核实原任务列表，不能换键。",
      );
      return;
    }
    const key = scope()!;
    const token = generation;
    busy.value = true;
    error.value = undefined;
    readController?.abort();
    const controller = new AbortController();
    readController = controller;
    try {
      const commands = options.client().commands;
      const intent = record.intentID
        ? await commands.get(record.intentID, controller.signal)
        : await commands.findByKey(record.idempotencyKey, controller.signal);
      if (current(key, token)) await receive(intent, record, key, token);
    } catch (reason) {
      if (current(key, token))
        error.value = asError(reason, "原意图查询未确认；保留原键。");
    } finally {
      if (current(key, token)) busy.value = false;
    }
  }
  async function confirmed(intent: CommandIntent): Promise<void> {
    const key = scope?.();
    const record = unresolved.value.find(
      (item) => item.intentID === intent.intent_id,
    );
    const token = generation;
    busy.value = true;
    try {
      if (!key || !record || !["executed", "abandoned"].includes(intent.status))
        throw new Error("原回执绑定尚未确认；不会丢弃原意图。");
      await receive(intent, record, key, token);
    } catch (reason) {
      if (current(key, token))
        error.value = asError(reason, "回执未确认；请查询原意图。");
    } finally {
      if (current(key, token)) busy.value = false;
    }
  }
  async function acknowledgeReceipt(): Promise<void> {
    const record = pending.value;
    const key = scope?.();
    if (
      busy.value ||
      !key ||
      record?.kind !== "callback_replay" ||
      !record.receiptID
    )
      return;
    const token = generation;
    busy.value = true;
    error.value = undefined;
    const controller = new AbortController();
    readController?.abort();
    readController = controller;
    const timer = setTimeout(() => controller.abort(), 15_000);
    try {
      await verifyCommandDispositions(
        options.client(),
        key,
        [{ ...record, acknowledged: true }],
        controller.signal,
      );
      if (current(key, token)) persist(key, { ...record, acknowledged: true });
    } catch (reason) {
      if (current(key, token))
        error.value = asError(reason, "原排队回执未核实；不允许新意图。");
    } finally {
      clearTimeout(timer);
      if (current(key, token)) busy.value = false;
    }
  }
  return {
    preview,
    busy,
    error,
    pending,
    unresolved,
    abandoned,
    acknowledged,
    acknowledgeReceipt,
    allowed,
    prepare,
    recover,
    confirmed,
  };
}
function asError(reason: unknown, message: string): Error {
  return reason instanceof Error ? reason : new Error(message);
}
