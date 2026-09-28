<script setup lang="ts">
import { ref } from "vue";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisDialog,
  IrisDialogContent,
  IrisDialogDescription,
  IrisDialogTitle,
  IrisFormField,
  IrisInput,
} from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import type { JsonRecord } from "../types";
import PagedRecordTable from "./PagedRecordTable.vue";
import PageError from "./PageError.vue";
import PageLoading from "./PageLoading.vue";
import { useAsyncResource } from "../hooks/useAsyncResource";
import { rememberIdempotencyKey } from "./idempotencyHelpers";

const props = defineProps<{ client: AeroIdClient }>();
const draftUID = ref("");
const draftReason = ref("");
const draftDuration = ref("");
const pending = ref<
  | {
      action: "ban";
      uid: string;
      reason: string;
      duration?: string;
      signature: string;
      idempotencyKey: string;
    }
  | { action: "unban"; uid: string }
>();
const intentKeys = new Map<string, string>();
const confirmation = ref("");
const running = ref(false);
const error = ref<Error>();
const resource = useAsyncResource(() => props.client.listBanned());
const confirmationPhrase = () =>
  pending.value?.action === "ban" ? "CONFIRM BAN" : "CONFIRM UNBAN";
function reload(): void {
  resource.reload();
}
function prepareBan(): void {
  if (running.value) return;
  const uid = draftUID.value.trim();
  const reason = draftReason.value.trim();
  if (!uid || !reason) {
    error.value = new Error("UID 和封禁原因不能为空");
    return;
  }
  error.value = undefined;
  const duration = draftDuration.value.trim() || undefined;
  const intent = rememberIdempotencyKey(
    { action: "ban", uid, reason, duration },
    intentKeys,
  );
  pending.value = {
    action: "ban",
    uid,
    reason,
    duration,
    signature: intent.signature,
    idempotencyKey: intent.idempotencyKey,
  };
  confirmation.value = "";
}
function prepareUnban(record: JsonRecord): void {
  if (running.value) return;
  const value = record.uid;
  if (typeof value !== "string" || !value) return;
  error.value = undefined;
  pending.value = { action: "unban", uid: value };
  confirmation.value = "";
}
async function confirm(): Promise<void> {
  const command = pending.value;
  if (
    !command ||
    confirmation.value !== confirmationPhrase() ||
    running.value
  ) {
    return;
  }
  running.value = true;
  error.value = undefined;
  try {
    if (command.action === "ban") {
      await props.client.banUser(
        command.uid,
        command.reason,
        command.duration,
        command.idempotencyKey,
      );
      intentKeys.delete(command.signature);
    } else {
      await props.client.unbanUser(command.uid);
    }
    pending.value = undefined;
    await reload();
  } catch (reason) {
    error.value =
      reason instanceof Error ? reason : new Error("封禁状态变更失败");
  } finally {
    running.value = false;
  }
}
function cancel(): void {
  if (!running.value) pending.value = undefined;
}
</script>

<template>
  <IrisCard variant="outline">
    <template #header>用户封禁</template>
    <IrisAlert tone="warning">
      封禁/解封会立即改变访问控制并断开在线连接。封禁 POST
      使用独立持久化幂等键并二次确认；解封 DELETE
      当前无幂等键字段，因此超时后先刷新列表确认状态，不自动重试。
    </IrisAlert>
    <div class="admin-ban-grid">
      <IrisFormField label="目标 UID" required
        ><IrisInput v-model="draftUID" placeholder="Snaplink UID"
      /></IrisFormField>
      <IrisFormField label="原因" required
        ><IrisInput v-model="draftReason" placeholder="必须提供可审计原因"
      /></IrisFormField>
      <IrisFormField label="持续时间" hint="Go duration，例如 24h；留空为永久"
        ><IrisInput v-model="draftDuration" placeholder="24h"
      /></IrisFormField>
      <IrisButton
        variant="danger"
        :disabled="running"
        @click="prepareBan"
        >准备封禁</IrisButton
      >
    </div>
    <IrisAlert v-if="error" tone="danger">{{ error.message }}</IrisAlert>
    <PageLoading v-if="resource.loading && !resource.data" />
    <PageError
      v-else-if="resource.error && !resource.data"
      :error="resource.error"
      :retry="reload"
    />
    <template v-else-if="resource.data">
      <PageError
        v-if="resource.error"
        :error="resource.error"
        :retry="reload"
      />
      <PagedRecordTable
        :page="resource.data"
        :load-page="(cursor) => client.listBanned(cursor)"
        :columns="[
          { key: 'uid', label: 'UID' },
          { key: 'actor_uid', label: '执行人' },
          { key: 'reason', label: '原因' },
          { key: 'expires_at', label: '到期时间', kind: 'date' },
          { key: 'created_at', label: '创建时间', kind: 'date' },
        ]"
        empty="当前没有有效封禁"
      >
        <template #actions="{ record }">
          <IrisButton
            size="sm"
            variant="outline"
            :disabled="running"
            @click="prepareUnban(record)"
            >准备解封</IrisButton
          >
        </template>
      </PagedRecordTable>
    </template>
  </IrisCard>
  <IrisDialog
    :open="Boolean(pending)"
    @update:open="(open) => !open && cancel()"
  >
    <IrisDialogContent>
      <IrisDialogTitle
        >确认{{ pending?.action === "ban" ? "封禁" : "解封" }}</IrisDialogTitle
      >
      <IrisDialogDescription>
        UID：{{ pending?.uid
        }}<template v-if="pending?.reason"
          >；原因：{{ pending.reason }}</template
        >。请确认这是经过授权的管理员意图。
      </IrisDialogDescription>
      <IrisFormField label="确认短语" :hint="confirmationPhrase()"
        ><IrisInput v-model="confirmation" autocomplete="off"
      /></IrisFormField>
      <IrisAlert v-if="error" tone="danger">{{ error.message }}</IrisAlert>
      <div class="dialog-actions">
        <IrisButton variant="ghost" :disabled="running" @click="cancel"
          >取消</IrisButton
        >
        <IrisButton
          variant="danger"
          :loading="running"
          :disabled="confirmation !== confirmationPhrase()"
          @click="void confirm()"
          >确认提交</IrisButton
        >
      </div>
    </IrisDialogContent>
  </IrisDialog>
</template>
