<script setup lang="ts">
import { ref } from "vue";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisInput,
  IrisTextarea,
} from "@iris-ui-kit/vue";
import type { CallbackCreateInput } from "../api/callback";
import type { AeroIdClient } from "../api/aeroId";
import CallbackCommandDialog from "../components/CallbackCommandDialog.vue";
import CallbackSecretDialog from "../components/CallbackSecretDialog.vue";
import type {
  CallbackCommand,
  CallbackSecret,
} from "../components/dialogTypes";
import {
  callbackCreateIntent,
  normalizeCallbackEndpoint,
  parseCallbackEventTypes,
} from "../components/callbackHelpers";
import PagedRecordTable from "../components/PagedRecordTable.vue";
import PageError from "../components/PageError.vue";
import PageHeader from "../components/PageHeader.vue";
import PageLoading from "../components/PageLoading.vue";
import { useAsyncResource } from "../hooks/useAsyncResource";
import type { JsonRecord } from "../types";
import { rememberIdempotencyKey } from "../components/idempotencyHelpers";

const { client } = defineProps<{ client: AeroIdClient }>();
const resource = useAsyncResource(() => client.callbacks.list());
const endpoint = ref("");
const eventTypeText = ref("");
const creating = ref(false);
const createError = ref<Error>();
const createIntent = ref<{ signature: string; idempotencyKey: string }>();
const secret = ref<CallbackSecret>();
const deliveryID = ref("");
const command = ref<CallbackCommand>();
const commandRunning = ref(false);
const commandError = ref<Error>();
const message = ref<string>();
const intentKeys = new Map<string, string>();
function callbackID(record: JsonRecord): string | undefined {
  const value = record.id ?? record.subscription_id;
  return typeof value === "string" && value ? value : undefined;
}
function startReplay(): void {
  if (commandRunning.value) return;
  const id = deliveryID.value.trim();
  if (!id) return;
  const intent = rememberIdempotencyKey({ kind: "replay", id }, intentKeys);
  command.value = {
    kind: "replay",
    id,
    signature: intent.signature,
    idempotencyKey: intent.idempotencyKey,
  };
}
function createKey(input: CallbackCreateInput): string {
  createIntent.value = callbackCreateIntent(input, createIntent.value);
  return createIntent.value.idempotencyKey;
}
async function create(): Promise<void> {
  if (creating.value) return;
  let input: CallbackCreateInput;
  try {
    input = {
      endpoint: normalizeCallbackEndpoint(endpoint.value),
      eventTypes: parseCallbackEventTypes(eventTypeText.value),
    };
  } catch (reason) {
    createError.value =
      reason instanceof Error ? reason : new Error("Callback 输入无效");
    return;
  }
  const idempotencyKey = createKey(input);
  creating.value = true;
  createError.value = undefined;
  message.value = undefined;
  try {
    const result = await client.callbacks.create(input, idempotencyKey);
    const id = callbackID(result.subscription);
    if (!id) throw new Error("注册响应缺少订阅 ID；请刷新列表核实后再继续");
    secret.value = result.secret
      ? { subscriptionID: id, value: result.secret }
      : undefined;
    if (!result.secret) {
      message.value =
        "订阅已创建，但此安全重试响应不含一次性 Secret。若之前未保存 Secret，请先核实投递方配置；不要用新幂等键重复创建。";
    }
    createIntent.value = undefined;
    endpoint.value = "";
    eventTypeText.value = "";
    resource.replace({
      items: [result.subscription, ...(resource.data?.items ?? [])],
      nextCursor: resource.data?.nextCursor,
    });
  } catch (reason) {
    createError.value =
      reason instanceof Error ? reason : new Error("Callback 注册失败");
  } finally {
    creating.value = false;
  }
}
async function runCommand(): Promise<void> {
  const current = command.value;
  if (!current || commandRunning.value) return;
  commandRunning.value = true;
  commandError.value = undefined;
  message.value = undefined;
  try {
    if (current.kind === "delete") {
      await client.callbacks.deleteSubscription(current.id);
      message.value = "Callback 订阅已删除，并已写入审计事实。";
      resource.replace({
        items: (resource.data?.items ?? []).filter(
          (item) => callbackID(item) !== current.id,
        ),
        nextCursor: resource.data?.nextCursor,
      });
    } else {
      await client.callbacks.replayDelivery(current.id, current.idempotencyKey);
      intentKeys.delete(current.signature);
      message.value = `Delivery ${current.id} 已进入重试队列。`;
      deliveryID.value = "";
    }
    command.value = undefined;
  } catch (reason) {
    commandError.value =
      reason instanceof Error ? reason : new Error("Callback 命令失败");
    if (current.kind === "delete") {
      command.value = undefined;
      resource.reload();
    }
  } finally {
    commandRunning.value = false;
  }
}
</script>

<template>
  <PageLoading v-if="resource.loading" />
  <PageError
    v-else-if="resource.error"
    :error="resource.error"
    :retry="resource.reload"
  />
  <section v-else-if="resource.data">
    <PageHeader
      title="Callback 治理"
      description="端点注册由服务端执行 HTTPS challenge、DNS/私网策略和受控 egress；Secret 仅在创建成功后显示一次。"
    />
    <IrisAlert v-if="message" tone="success">{{ message }}</IrisAlert>
    <IrisCard variant="outline">
      <template #header>注册 Callback 端点</template>
      <IrisAlert v-if="createError" tone="danger">
        {{ createError.message }}
        <p v-if="createIntent">
          若请求结果不确定，使用相同表单重试会复用本次幂等键；不要为同一意图生成新键。
        </p>
      </IrisAlert>
      <form class="callback-create-grid" @submit.prevent="void create()">
        <IrisFormField
          label="HTTPS Endpoint"
          hint="端点必须回显服务端发送的 challenge"
          ><IrisInput
            v-model="endpoint"
            type="url"
            required
            :disabled="creating"
            placeholder="https://callback.example.com/aero/events"
        /></IrisFormField>
        <IrisFormField
          label="允许的事件类型"
          hint="使用逗号或换行分隔；最多 64 个"
          ><IrisTextarea
            v-model="eventTypeText"
            required
            :disabled="creating"
            placeholder="operation.completed, export.ready"
        /></IrisFormField>
        <div class="admin-filter-actions">
          <IrisButton type="submit" :loading="creating" :disabled="creating"
            >验证并注册</IrisButton
          >
        </div>
      </form>
    </IrisCard>
    <IrisCard variant="outline">
      <template #header>当前订阅</template>
      <PagedRecordTable
        :page="resource.data"
        :load-page="(cursor) => client.callbacks.list(cursor)"
        :columns="[
          { key: 'id', label: '订阅 ID', aliases: ['subscription_id'] },
          { key: 'endpoint', label: 'Endpoint' },
          { key: 'event_types', label: '事件类型' },
          { key: 'verified', label: '已验证' },
          { key: 'enabled', label: '已启用' },
        ]"
        empty="暂无 Callback 订阅"
      >
        <template #actions="{ record }">
          <IrisButton
            v-if="callbackID(record)"
            size="sm"
            variant="danger"
            :disabled="
              commandRunning || resource.loading || Boolean(resource.error)
            "
            @click="
              commandError = undefined;
              command = {
                kind: 'delete',
                id: callbackID(record)!,
                endpoint: String(record.endpoint ?? ''),
              };
            "
            >删除</IrisButton
          >
        </template>
      </PagedRecordTable>
    </IrisCard>
    <IrisCard variant="outline">
      <template #header>人工重放投递</template>
      <IrisAlert tone="warning" title="只重放明确识别的失败投递"
        >当前服务端没有投递列表 API。请从脱敏审计事实或运行手册取得 Delivery
        ID，确认远端幂等后再重放。</IrisAlert
      >
      <div class="callback-replay-row">
        <IrisFormField label="Delivery ID"
          ><IrisInput v-model="deliveryID"
        /></IrisFormField>
        <IrisButton
          variant="outline"
          :disabled="!deliveryID.trim() || commandRunning"
          @click="startReplay"
          >重放投递</IrisButton
        >
      </div>
    </IrisCard>
    <CallbackSecretDialog :secret="secret" @discard="secret = undefined" />
    <CallbackCommandDialog
      :command="command"
      :running="commandRunning"
      :error="commandError"
      @cancel="command = undefined"
      @confirm="void runCommand()"
    />
  </section>
</template>
