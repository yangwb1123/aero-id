<script setup lang="ts">
import { computed, ref } from "vue";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisInput,
  IrisSelect,
} from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import type { JsonRecord } from "../types";
import { rememberIdempotencyKey } from "./idempotencyHelpers";
import RecordTable from "./RecordTable.vue";
import {
  projectionReplayOutcome,
  projectionSources,
  records,
  type ProjectionReplayOutcome,
} from "./replayHelpers";
const { client } = defineProps<{ client: AeroIdClient }>();
const accountID = ref("");
const source = ref(projectionSources[2].value);
const limit = ref("1000");
const running = ref(false);
const error = ref<Error>();
const result = ref<JsonRecord>();
const inputCursor = ref<string>();
const intentKeys = new Map<string, string>();
function resetWindow(): void {
  result.value = undefined;
  inputCursor.value = undefined;
  error.value = undefined;
}
async function run(cursor?: string): Promise<void> {
  if (running.value) return;
  const id = accountID.value.trim();
  error.value = undefined;
  if (!id) {
    error.value = new Error("Account ID 不能为空");
    return;
  }
  const replayLimit = Number(limit.value);
  if (
    !Number.isInteger(replayLimit) ||
    replayLimit < 1 ||
    replayLimit > 10000
  ) {
    error.value = new Error("Projection Replay limit 必须是 1–10000 的整数");
    return;
  }
  const input = {
    accountID: id,
    source: source.value,
    cursor,
    limit: replayLimit,
  };
  const intent = rememberIdempotencyKey(input, intentKeys);
  running.value = true;
  try {
    result.value = await client.replays.replayProjection({
      ...input,
      idempotencyKey: intent.idempotencyKey,
    });
    intentKeys.delete(intent.signature);
    inputCursor.value = cursor;
  } catch (reason) {
    error.value =
      reason instanceof Error
        ? reason
        : new Error("Shadow Projection Replay 失败");
  } finally {
    running.value = false;
  }
}
const matched = computed(() => records(result.value?.matched));
const divergent = computed(() => records(result.value?.divergent));
const shadowOnly = computed(() => records(result.value?.shadow_only));
const liveOnly = computed(() => records(result.value?.live_only));
const outcome = computed<ProjectionReplayOutcome | undefined>(() =>
  result.value ? projectionReplayOutcome(result.value) : undefined,
);
const nextCursor = computed(() =>
  typeof result.value?.next_cursor === "string"
    ? result.value.next_cursor
    : undefined,
);
const differences = computed(
  () =>
    divergent.value.length > 0 ||
    shadowOnly.value.length > 0 ||
    liveOnly.value.length > 0,
);
const sections = computed(() => [
  { title: "Matched", rows: matched.value },
  { title: "Divergent", rows: divergent.value },
  { title: "Shadow only", rows: shadowOnly.value },
  { title: "Live only", rows: liveOnly.value },
]);
</script>

<template>
  <IrisCard variant="outline">
    <template #header>Shadow Projection Replay</template>
    <IrisAlert tone="info" title="只读、有界、逐窗口校验"
      >从当前租户的脱敏事件存储重建 shadow projection，并与 live projection
      比较；不会写入或修复 live
      数据。每次读取均独立审计，页面不会自动追赶全部历史。</IrisAlert
    >
    <form class="projection-replay-grid" @submit.prevent="void run()">
      <IrisFormField label="Account ID" required
        ><IrisInput
          v-model="accountID"
          required
          :disabled="running"
          @update:model-value="resetWindow"
      /></IrisFormField>
      <IrisFormField label="Event Source"
        ><IrisSelect
          v-model="source"
          :items="projectionSources"
          :disabled="running"
          @update:model-value="resetWindow"
      /></IrisFormField>
      <IrisFormField label="单窗口事件上限" hint="默认 1000，硬上限 10000"
        ><IrisInput
          v-model="limit"
          type="number"
          min="1"
          max="10000"
          :disabled="running"
          @update:model-value="resetWindow"
      /></IrisFormField>
      <IrisButton type="submit" :loading="running" :disabled="running"
        >重放首个有界窗口</IrisButton
      >
    </form>
    <IrisAlert v-if="error" tone="danger">{{ error.message }}</IrisAlert>
    <template v-if="result">
      <RecordTable
        :records="[{ ...result, input_cursor: inputCursor ?? 'first' }]"
        :columns="[
          { key: 'account_id', label: 'Account ID' },
          { key: 'source', label: 'Source' },
          { key: 'input_cursor', label: '本窗口起点' },
          { key: 'replayed_events', label: '重放事件数' },
          { key: 'completed', label: '已到历史末尾' },
          { key: 'next_cursor', label: '下一游标' },
        ]"
      />
      <IrisAlert
        v-if="outcome === 'partial'"
        tone="warning"
        title="结果只覆盖当前事件窗口"
        >当前分类不是完整账户历史结论。可手动读取下一窗口；禁止自动全量扫描或把窗口内
        matched 当作全局一致。</IrisAlert
      >
      <IrisAlert
        v-if="differences"
        tone="danger"
        title="当前窗口发现 Projection 差异"
        >未完成窗口中的差异不是全局结论。只读 Replay 不会自动修复；请结合 source
        version、后续窗口、事件缺口和审计事实启动受控
        reconciliation。</IrisAlert
      >
      <IrisAlert v-if="outcome === 'matched'" tone="success"
        >本次有界重放已到历史末尾，未发现 shadow/live 差异。</IrisAlert
      >
      <IrisAlert v-if="outcome === 'empty'" tone="warning"
        >当前来源没有可重放事件；这不是 live projection
        正确性的证明。</IrisAlert
      >
      <template v-for="section in sections" :key="section.title">
        <template v-if="section.rows.length"
          ><h3 class="detail-heading">{{ section.title }}</h3>
          <RecordTable
            :records="section.rows"
            :columns="[
              { key: 'dataset', label: 'Dataset' },
              { key: 'status', label: 'Shadow 状态', kind: 'status' },
              { key: 'version', label: 'Shadow Version' },
              { key: 'live_version', label: 'Live Version' },
              { key: 'digest', label: 'Shadow Digest' },
              { key: 'live_digest', label: 'Live Digest' },
              { key: 'generated_at', label: '生成时间', kind: 'date' },
            ]"
        /></template>
      </template>
      <div class="detail-actions">
        <IrisButton variant="ghost" @click="resetWindow"
          >清除窗口结果</IrisButton
        ><IrisButton
          v-if="nextCursor"
          variant="outline"
          :loading="running"
          :disabled="running"
          @click="void run(nextCursor)"
          >手动读取下一窗口</IrisButton
        >
      </div>
    </template>
  </IrisCard>
</template>
