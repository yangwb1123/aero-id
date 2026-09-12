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
import MigrationCommandDialog from "./MigrationCommandDialog.vue";
import type { MigrationCommand } from "./dialogTypes";
import RecordTable from "./RecordTable.vue";
import type { JsonRecord } from "../types";
const { client } = defineProps<{ client: AeroIdClient }>();
const limit = ref("500");
const command = ref<MigrationCommand>();
const confirmation = ref("");
const running = ref(false);
const error = ref<Error>();
const result = ref<JsonRecord>();
function prepare(): void {
  error.value = undefined;
  const batchLimit = Number(limit.value);
  if (!Number.isInteger(batchLimit) || batchLimit < 1 || batchLimit > 1000) {
    error.value = new Error("Backfill batch 必须是 1–1000 的整数");
    return;
  }
  confirmation.value = "";
  result.value = undefined;
  command.value = {
    kind: "backfill",
    limit: batchLimit,
    idempotencyKey: crypto.randomUUID(),
  };
}
async function execute(): Promise<void> {
  if (command.value?.kind !== "backfill") return;
  running.value = true;
  error.value = undefined;
  try {
    result.value = await client.migration.backfillLegacyIdentities(
      command.value.limit,
      command.value.idempotencyKey,
    );
    command.value = undefined;
    confirmation.value = "";
  } catch (reason) {
    error.value =
      reason instanceof Error ? reason : new Error("Backfill 执行失败");
  } finally {
    running.value = false;
  }
}
</script>

<template>
  <IrisCard variant="outline">
    <template #header>Legacy Identity Shadow Backfill</template>
    <IrisAlert tone="info" title="只建立控制面账户引用"
      >Backfill 将 legacy UID 映射为 sentinel tenant 下的 shadow account
      identity；不会迁移文件、消息或评论内容，也不会替代 Snaplink
      人类身份权威。</IrisAlert
    >
    <div class="migration-command-row">
      <IrisFormField
        label="单批 UID 上限"
        hint="默认 500，服务端硬上限 1000；未完成时以新的管理意图执行下一批"
      >
        <IrisInput v-model="limit" type="number" min="1" max="1000" />
      </IrisFormField>
      <IrisButton @click="prepare">检查并确认 Backfill</IrisButton>
    </div>
    <IrisAlert v-if="error && !command" tone="danger">{{
      error.message
    }}</IrisAlert>
    <template v-if="result">
      <RecordTable
        :records="[result]"
        :columns="[
          { key: 'scanned', label: '已扫描' },
          { key: 'created', label: '已创建' },
          { key: 'existing', label: '已存在' },
          { key: 'skipped', label: '已跳过' },
          { key: 'total', label: '估算总数' },
          { key: 'tenant', label: 'Shadow Tenant' },
          { key: 'issuer', label: 'Issuer' },
          { key: 'completed', label: '本轮是否完成' },
        ]"
      />
      <IrisAlert v-if="result.completed !== true" tone="warning"
        >仍有 UID 待处理。核对 skipped
        与审计事实后，再以新的确认和幂等键执行下一批。</IrisAlert
      >
    </template>
  </IrisCard>
  <MigrationCommandDialog
    :command="command"
    :confirmation="confirmation"
    :running="running"
    :error="error"
    @update:confirmation="confirmation = $event"
    @cancel="command = undefined"
    @confirm="void execute()"
  />
</template>
