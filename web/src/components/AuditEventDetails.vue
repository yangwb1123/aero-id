<script setup lang="ts">
import { ref } from "vue";
import { IrisAlert, IrisButton, IrisCard } from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import type { JsonRecord } from "../types";
import RecordTable from "./RecordTable.vue";
import { safeJsonPreview } from "./safeJsonPreview";
const props = defineProps<{ client: AeroIdClient; event: JsonRecord }>();
const emit = defineEmits<{ close: [] }>();
const verifying = ref(false);
const verifyError = ref<Error>();
const verified = ref(false);
async function verify(): Promise<void> {
  verifying.value = true;
  verifyError.value = undefined;
  verified.value = false;
  const source =
    typeof props.event.source === "string" ? props.event.source : "";
  const partition =
    typeof props.event.partition_key === "string"
      ? props.event.partition_key
      : "";
  try {
    const result = await props.client.verifyAuditChain(source, partition);
    verified.value = result.verified === true;
    if (!verified.value) throw new Error("服务端未确认审计链完整");
  } catch (reason) {
    verifyError.value =
      reason instanceof Error ? reason : new Error("审计链验证失败");
  } finally {
    verifying.value = false;
  }
}
</script>

<template>
  <IrisCard variant="outline">
    <template #header
      >审计事实 · {{ String(event.event_id ?? event.id ?? "") }}</template
    >
    <template #footer>
      <div class="detail-actions">
        <IrisButton size="sm" variant="ghost" @click="emit('close')"
          >关闭</IrisButton
        >
        <IrisButton
          size="sm"
          variant="outline"
          :loading="verifying"
          :disabled="!event.source || !event.partition_key"
          @click="void verify()"
          >验证所属分区链</IrisButton
        >
      </div>
    </template>
    <IrisAlert v-if="verified" tone="success" title="审计链验证通过"
      >aero-id 已重新计算并验证 {{ event.source }} /
      {{ event.partition_key }} 分区的版本化哈希链。</IrisAlert
    >
    <IrisAlert v-if="verifyError" tone="danger" title="审计链验证失败">{{
      verifyError.message
    }}</IrisAlert>
    <RecordTable
      :records="[event]"
      :columns="[
        { key: 'action', label: '动作' },
        { key: 'source', label: '来源' },
        { key: 'source_region', label: '来源区域' },
        { key: 'partition_key', label: '分区' },
        { key: 'chain_sequence', label: '链序号' },
        { key: 'occurred_at', label: '发生时间', kind: 'date' },
      ]"
    />
    <h3 class="detail-heading">关联标识</h3>
    <RecordTable
      :records="[event]"
      :columns="[
        { key: 'operation_id', label: 'Operation ID' },
        { key: 'event_id', label: 'Event ID' },
        { key: 'causation_id', label: 'Causation ID' },
        { key: 'request_id', label: 'Request ID' },
        { key: 'trace_id', label: 'Trace ID' },
        { key: 'account_id', label: 'Account ID' },
      ]"
    />
    <h3 class="detail-heading">防篡改元数据</h3>
    <RecordTable
      :records="[event]"
      :columns="[
        { key: 'hash_version', label: 'Hash 版本' },
        { key: 'payload_hash', label: 'Payload Hash' },
        { key: 'previous_hash', label: 'Previous Hash' },
        { key: 'record_hash', label: 'Record Hash' },
        { key: 'signature_version', label: '签名版本' },
        { key: 'signature', label: '签名' },
      ]"
    />
    <h3 class="detail-heading">脱敏 Payload</h3>
    <pre class="json-preview">{{ safeJsonPreview(event.payload) }}</pre>
  </IrisCard>
</template>
