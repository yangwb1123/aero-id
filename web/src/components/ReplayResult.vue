<script setup lang="ts">
import { ref, computed } from "vue";
import { IrisAlert, IrisButton, IrisCard } from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import type { JsonRecord } from "../types";
import OperationDetails from "./OperationDetails.vue";
import RecordTable from "./RecordTable.vue";
import { records } from "./replayHelpers";
import { safeJsonPreview } from "./safeJsonPreview";
const props = defineProps<{
  client: AeroIdClient;
  result: JsonRecord;
  continuing: boolean;
}>();
const emit = defineEmits<{ clear: []; continueWindow: [cursor: string] }>();
const controlled = computed(() => props.result.mode === "controlled");
const showOperation = ref(controlled.value);
const events = computed(() => records(props.result.events));
</script>

<template>
  <IrisCard variant="outline">
    <template #header
      >Replay 结果 · {{ String(result.replay_id ?? "") }}</template
    >
    <template #footer>
      <div class="detail-actions">
        <IrisButton size="sm" variant="ghost" @click="emit('clear')"
          >清除结果</IrisButton
        >
        <IrisButton
          v-if="controlled && result.replay_id"
          size="sm"
          variant="outline"
          @click="showOperation = true"
          >查看 Operation 时间线</IrisButton
        >
        <IrisButton
          v-if="!controlled && result.next_cursor"
          size="sm"
          variant="outline"
          :loading="continuing"
          @click="emit('continueWindow', String(result.next_cursor))"
          >手动读取下一窗口</IrisButton
        >
      </div>
    </template>
    <RecordTable
      :records="[result]"
      :columns="[
        { key: 'mode', label: '模式' },
        { key: 'status', label: '状态', kind: 'status' },
        { key: 'replay_id', label: 'Replay ID' },
        { key: 'next_cursor', label: '下一游标' },
      ]"
    />
    <IrisAlert
      v-if="result.next_cursor"
      tone="warning"
      title="重建结果仍有后续事实"
      >当前仅展示一个有界窗口。继续时会原样回传游标、重新鉴权并写入新的审计读取事实；页面不会自动全量扫描。</IrisAlert
    >
    <template v-if="result.simulation"
      ><h3 class="detail-heading">Dry-run 模拟结果</h3>
      <pre class="json-preview">{{ safeJsonPreview(result.simulation) }}</pre>
    </template>
    <template v-if="events.length"
      ><h3 class="detail-heading">重建的脱敏事实</h3>
      <RecordTable
        :records="events"
        :columns="[
          { key: 'occurred_at', label: '发生时间', kind: 'date' },
          { key: 'action', label: '动作' },
          { key: 'source', label: '来源' },
          { key: 'resource_type', label: '资源类型' },
          { key: 'resource_id', label: '资源 ID' },
          { key: 'event_id', label: 'Event ID' },
        ]"
    /></template>
  </IrisCard>
  <OperationDetails
    v-if="controlled && result.replay_id && showOperation"
    :client="client"
    :operation-id="String(result.replay_id)"
    @close="showOperation = false"
  />
</template>
