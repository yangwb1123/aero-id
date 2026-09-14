<script setup lang="ts">
import { ref } from "vue";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisInput,
} from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import type { JsonRecord } from "../types";
import OperationDetails from "./OperationDetails.vue";
import PageError from "./PageError.vue";
import PageLoading from "./PageLoading.vue";
import RecordTable from "./RecordTable.vue";

const props = defineProps<{ client: AeroIdClient }>();
const replayID = ref("");
const result = ref<JsonRecord>();
const loading = ref(false);
const error = ref<Error>();
const showTimeline = ref(false);
function jsonPreview(value: unknown): string {
  return JSON.stringify(value ?? {}, null, 2);
}
async function load(): Promise<void> {
  if (!replayID.value.trim()) {
    error.value = new Error("Replay ID 不能为空");
    return;
  }
  loading.value = true;
  error.value = undefined;
  showTimeline.value = false;
  try {
    result.value = await props.client.replays.get(replayID.value.trim());
  } catch (reason) {
    error.value =
      reason instanceof Error ? reason : new Error("Replay 详情读取失败");
  } finally {
    loading.value = false;
  }
}
</script>

<template>
  <IrisCard variant="outline">
    <template #header>Replay 详情 / 恢复查看</template>
    <IrisAlert tone="info">
      仅按明确的 Replay ID 读取已持久化 Controlled Replay。Forensic 与 Dry-run
      是有界同步结果，不会在这里被扫描或恢复。
    </IrisAlert>
    <form class="replay-detail-row" @submit.prevent="void load()">
      <IrisFormField label="Replay ID" required>
        <IrisInput v-model="replayID" required placeholder="UUID" />
      </IrisFormField>
      <IrisButton type="submit" :loading="loading">读取详情</IrisButton>
    </form>
    <PageLoading v-if="loading" />
    <PageError v-else-if="error" :error="error" :retry="load" />
    <template v-else-if="result">
      <RecordTable
        :records="[result]"
        :columns="[
          {
            key: 'operation_id',
            label: 'Operation ID',
            aliases: ['replay_id'],
          },
          { key: 'operation_type', label: '类型' },
          { key: 'status', label: '状态', kind: 'status' },
          { key: 'created_at', label: '创建时间', kind: 'date' },
          { key: 'updated_at', label: '更新时间', kind: 'date' },
        ]"
      />
      <IrisAlert
        v-if="result.status === 'unknown'"
        tone="warning"
        title="结果未知"
      >
        不要因为网络超时重复创建新意图；使用原 Replay/Operation ID 对账。
      </IrisAlert>
      <pre class="json-preview">{{ jsonPreview(result) }}</pre>
      <div class="detail-actions">
        <IrisButton
          v-if="result.operation_id || result.replay_id"
          variant="outline"
          @click="showTimeline = true"
          >查看持久化 Operation 时间线</IrisButton
        >
      </div>
    </template>
  </IrisCard>
  <OperationDetails
    v-if="showTimeline && (result?.operation_id || result?.replay_id)"
    :client="client"
    :operation-id="String(result.operation_id ?? result.replay_id)"
    @close="showTimeline = false"
  />
</template>
