<script setup lang="ts">
import { ref } from "vue";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisInput,
  IrisSelect,
} from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import type { ReplayCreateInput, ReplayMode } from "../api/replay";
import PageHeader from "../components/PageHeader.vue";
import ProjectionReplayPanel from "../components/ProjectionReplayPanel.vue";
import ReplayCommandEditor from "../components/ReplayCommandEditor.vue";
import ReplayConfirmationDialog from "../components/ReplayConfirmationDialog.vue";
import ReplayDetailsPanel from "../components/ReplayDetailsPanel.vue";
import ReplayResult from "../components/ReplayResult.vue";
import {
  controlledReplaySignature,
  newReplayCommand,
  nextReplayIdempotencyKey,
  parseReplayCommands,
  replayContinuation,
  replayTimeWindow,
  type ReplayCommandDraft,
} from "../components/replayHelpers";
import type { JsonRecord } from "../types";

const modeItems = [
  { value: "forensic", label: "Forensic（仅重建事实）" },
  { value: "dry-run", label: "Dry-run（模拟、不执行）" },
  { value: "controlled", label: "Controlled（真实命令）" },
];
const { client } = defineProps<{ client: AeroIdClient }>();
const mode = ref<ReplayMode>("forensic");
const accountID = ref("");
const operationID = ref("");
const fromTime = ref("");
const toTime = ref("");
const limit = ref("100");
const idempotencyKey = ref("");
const commands = ref<ReplayCommandDraft[]>([]);
const pending = ref<ReplayCreateInput>();
const confirmation = ref("");
const running = ref(false);
const error = ref<Error>();
const result = ref<JsonRecord>();
const lastInput = ref<ReplayCreateInput>();
const issuedControlledIntent = ref<{ key: string; signature: string }>();
function newID(): string {
  return crypto.randomUUID();
}
function actionLabel(value: ReplayMode): string {
  return value === "controlled"
    ? "检查并确认 Controlled Replay"
    : value === "dry-run"
      ? "运行 Dry-run"
      : "执行 Forensic 重建";
}
function changeMode(value: unknown): void {
  const next = String(value) as ReplayMode;
  mode.value = next;
  error.value = undefined;
  if (next === "controlled") {
    if (!idempotencyKey.value) idempotencyKey.value = crypto.randomUUID();
    if (!commands.value.length) commands.value = [newReplayCommand()];
  } else if (next === "forensic") commands.value = [];
}
function validateScope(): void {
  if (!accountID.value.trim() && !operationID.value.trim())
    throw new Error("Forensic 和 Dry-run 至少需要 Account ID 或 Operation ID");
}
function buildInput(): ReplayCreateInput {
  const normalizedAccountID = accountID.value.trim();
  const normalizedOperationID = operationID.value.trim();
  const normalizedKey = idempotencyKey.value.trim();
  validateScope();
  const count = Number(limit.value);
  if (!Number.isInteger(count) || count < 1 || count > 100)
    throw new Error("单次重建事实数量必须在 1–100 之间");
  const parsedCommands = parseReplayCommands(commands.value);
  if (mode.value === "controlled") {
    if (!normalizedAccountID)
      throw new Error("Controlled Replay 必须指定 Account ID");
    if (!parsedCommands.length)
      throw new Error("Controlled Replay 至少需要一个命令");
    if (!normalizedKey) throw new Error("Controlled Replay 必须使用新的幂等键");
  }
  const window =
    mode.value === "controlled"
      ? {}
      : replayTimeWindow(fromTime.value, toTime.value);
  const input: ReplayCreateInput = {
    mode: mode.value,
    accountID: normalizedAccountID || undefined,
    operationID: normalizedOperationID || undefined,
    ...window,
    limit: count,
    idempotencyKey: mode.value === "controlled" ? normalizedKey : undefined,
    confirmed: false,
    commands: mode.value === "forensic" ? [] : parsedCommands,
  };
  if (
    input.mode === "controlled" &&
    issuedControlledIntent.value?.key === normalizedKey &&
    issuedControlledIntent.value.signature !== controlledReplaySignature(input)
  ) {
    throw new Error(
      "上一次 Controlled Replay 结果尚未确认，不能用旧幂等键提交不同命令；请先用原请求重试或核实状态，再生成新键开始新意图。",
    );
  }
  return input;
}
function generateReplayKey(): void {
  idempotencyKey.value = newID();
  issuedControlledIntent.value = undefined;
}
async function execute(input: ReplayCreateInput): Promise<void> {
  if (input.mode === "controlled" && input.idempotencyKey) {
    issuedControlledIntent.value = {
      key: input.idempotencyKey,
      signature: controlledReplaySignature(input),
    };
  }
  running.value = true;
  error.value = undefined;
  try {
    result.value = await client.replays.create(input);
    lastInput.value = input;
    issuedControlledIntent.value = undefined;
    idempotencyKey.value = nextReplayIdempotencyKey(
      input,
      idempotencyKey.value,
    );
    pending.value = undefined;
    confirmation.value = "";
  } catch (reason) {
    error.value =
      reason instanceof Error ? reason : new Error("Replay 执行失败");
  } finally {
    running.value = false;
  }
}
function prepare(): void {
  error.value = undefined;
  try {
    const input = buildInput();
    if (input.mode === "controlled") {
      confirmation.value = "";
      pending.value = input;
    } else void execute(input);
  } catch (reason) {
    error.value =
      reason instanceof Error ? reason : new Error("Replay 输入无效");
  }
}
function continueWindow(cursor: string): void {
  try {
    void execute(replayContinuation(lastInput.value, cursor));
  } catch (reason) {
    error.value =
      reason instanceof Error ? reason : new Error("Replay 游标无效");
  }
}
</script>

<template>
  <section>
    <PageHeader
      title="审计 Replay"
      description="Forensic 只重建事实，Dry-run 只模拟，Controlled 创建全新的持久化 Operation；三种模式绝不复用历史凭据。"
    />
    <IrisCard variant="outline">
      <template #header>Replay 作用域与模式</template>
      <IrisAlert v-if="error && !pending" tone="danger">{{
        error.message
      }}</IrisAlert>
      <form @submit.prevent="prepare">
        <div class="replay-scope-grid">
          <IrisFormField label="模式"
            ><IrisSelect
              :model-value="mode"
              :items="modeItems"
              @update:model-value="changeMode"
          /></IrisFormField>
          <IrisFormField label="Account ID" :required="mode === 'controlled'"
            ><IrisInput v-model="accountID"
          /></IrisFormField>
          <IrisFormField
            label="Operation ID"
            :hint="
              mode === 'controlled'
                ? '可选历史来源；服务端要求它属于同一 Account'
                : '可单独作为审计事实重建作用域'
            "
            ><IrisInput v-model="operationID"
          /></IrisFormField>
          <IrisFormField label="事实上限" hint="服务端单批最大 100"
            ><IrisInput v-model="limit" type="number" min="1" max="100"
          /></IrisFormField>
          <template v-if="mode !== 'controlled'">
            <IrisFormField label="开始时间" hint="包含；输入本地时间或 RFC3339"
              ><IrisInput v-model="fromTime" placeholder="2026-08-20T10:00"
            /></IrisFormField>
            <IrisFormField label="结束时间" hint="不包含；必须晚于开始时间"
              ><IrisInput v-model="toTime" placeholder="2026-08-21T10:00"
            /></IrisFormField>
          </template>
        </div>
        <div v-if="mode === 'controlled'" class="replay-key-row">
          <IrisFormField
            label="本次新幂等键"
            hint="请求超时后使用同一个键安全重试；新意图必须生成新键"
            ><IrisInput v-model="idempotencyKey" readonly
          /></IrisFormField>
          <IrisButton type="button" variant="outline" @click="generateReplayKey"
            >生成新键</IrisButton
          >
        </div>
        <template v-if="mode !== 'forensic'">
          <h3 class="detail-heading">
            {{ mode === "dry-run" ? "模拟命令（可选）" : "真实命令" }}
          </h3>
          <ReplayCommandEditor
            :commands="commands"
            @change="commands = $event"
          />
        </template>
        <div class="replay-submit-row">
          <IrisButton type="submit" :loading="running">{{
            actionLabel(mode)
          }}</IrisButton>
        </div>
      </form>
    </IrisCard>
    <ReplayResult
      v-if="result"
      :client="client"
      :result="result"
      :continuing="running"
      @clear="
        result = undefined;
        lastInput = undefined;
      "
      @continue-window="continueWindow"
    />
    <ProjectionReplayPanel :client="client" />
    <ReplayDetailsPanel :client="client" />
    <ReplayConfirmationDialog
      :input="pending"
      :confirmation="confirmation"
      :running="running"
      :error="error"
      @update:confirmation="confirmation = $event"
      @cancel="
        pending = undefined;
        error = undefined;
      "
      @confirm="pending && void execute({ ...pending, confirmed: true })"
    />
  </section>
</template>
