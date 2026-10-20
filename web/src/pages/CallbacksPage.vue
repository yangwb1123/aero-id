<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from "vue";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisInput,
  IrisTextarea,
} from "@iris-ui-kit/vue";
import type { CallbackCreateInput } from "../api/callback";
import { callbackReadID, type CallbackDelivery } from "../api/callbackDelivery";
import type { AeroIdClient } from "../api/aeroId";
import CallbackCommandDialog from "../components/CallbackCommandDialog.vue";
import CallbackDeliveries from "../components/CallbackDeliveries.vue";
import CallbackReplayPreview from "../components/CallbackReplayPreview.vue";
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
import {
  callbackActionAvailable,
  callbackReadAvailable,
  callbackReplayAvailable,
} from "../components/callbackEligibility";
import PagedRecordTable from "../components/PagedRecordTable.vue";
import PageError from "../components/PageError.vue";
import PageHeader from "../components/PageHeader.vue";
import PageLoading from "../components/PageLoading.vue";
import { useAsyncResource } from "../hooks/useAsyncResource";
import { useAccountScope } from "../hooks/accountContext";
import {
  useCapabilities,
  useRefreshCapabilities,
  refreshAfterAuthorizationError,
} from "../hooks/capabilityContext";
import type { JsonRecord } from "../types";
import { callbackRegistrationMatches } from "../components/callbackRegistrationReceipt";

const { client } = defineProps<{ client: AeroIdClient }>();
const scope = useAccountScope();
const capabilities = useCapabilities();
const refreshCapabilities = useRefreshCapabilities();
const eligible = computed(() =>
  callbackActionAvailable(capabilities?.(), scope?.()),
);
const readEligible = computed(() =>
  callbackReadAvailable(capabilities?.(), scope?.()),
);
const resource = useAsyncResource((signal) =>
  loadSubscriptions(undefined, signal),
);
async function loadSubscriptions(cursor?: string, signal?: AbortSignal) {
  if (!readEligible.value)
    throw new Error("当前 Callback 读取能力尚未确认；不会读取新订阅页。");
  const originalScope = scope?.();
  try {
    const page = await client.callbacks.list(cursor, signal);
    if (!currentScope(originalScope))
      throw new Error("订阅读取身份已变化；不会采用迟到回应。");
    return page;
  } catch (reason) {
    refreshAfterAuthorizationError(reason, refreshCapabilities);
    throw reason;
  }
}
const endpoint = ref("");
const eventTypeText = ref("");
const creating = ref(false);
const createError = ref<Error>();
const createIntent = ref<{ signature: string; idempotencyKey: string }>();
const secret = ref<CallbackSecret>();
const selectedSubscription = ref<string>();
const deliveryRevision = ref(0);
const command = ref<CallbackCommand>();
const commandRunning = ref(false);
const commandError = ref<Error>();
const message = ref<string>();
const replayPreview = ref<InstanceType<typeof CallbackReplayPreview>>();
const replayBlocked = ref(false);
let active = true;
onBeforeUnmount(() => {
  active = false;
  secret.value = undefined;
});
function currentScope(original: string | undefined): boolean {
  return active && Boolean(original) && scope?.() === original;
}
function callbackID(record: JsonRecord): string | undefined {
  const value = record.id ?? record.subscription_id;
  if (typeof value !== "string") return undefined;
  try {
    return callbackReadID(value);
  } catch {
    return undefined;
  }
}
function startReplay(delivery: CallbackDelivery): void {
  if (
    commandRunning.value ||
    creating.value ||
    Boolean(command.value) ||
    replayBlocked.value ||
    !callbackReplayAvailable(delivery, capabilities?.(), scope?.()) ||
    delivery.subscription_id !== selectedSubscription.value
  )
    return;
  void replayPreview.value?.start(delivery);
}
function createKey(input: CallbackCreateInput): string {
  if (
    createIntent.value &&
    createIntent.value.signature !== JSON.stringify(input)
  )
    throw new Error(
      "原注册结果尚未确认；保留原表单和键，不会更改端点创建第二个订阅。",
    );
  createIntent.value = callbackCreateIntent(input, createIntent.value);
  return createIntent.value.idempotencyKey;
}
async function create(): Promise<void> {
  if (creating.value || !eligible.value) return;
  const originalScope = scope?.();
  let input: CallbackCreateInput;
  let key: string;
  try {
    input = {
      endpoint: normalizeCallbackEndpoint(endpoint.value),
      eventTypes: parseCallbackEventTypes(eventTypeText.value),
    };
    key = createKey(input);
  } catch (reason) {
    createError.value =
      reason instanceof Error ? reason : new Error("Callback 输入无效");
    return;
  }
  creating.value = true;
  createError.value = undefined;
  message.value = undefined;
  try {
    const result = await client.callbacks.create(input, key);
    if (!currentScope(originalScope)) return;
    const id = callbackID(result.subscription);
    if (
      !id ||
      !callbackRegistrationMatches(result.subscription, input, originalScope)
    )
      throw new Error(
        "注册回执与原端点/范围/租户不一致或格式无效；保留原表单和键，请核实原订阅",
      );
    secret.value = result.secret
      ? { subscriptionID: id, value: result.secret }
      : undefined;
    if (!result.secret)
      message.value =
        "订阅已创建，但安全重试响应不含一次性 Secret。若之前未保存，请先核实接收端配置；不要换键重复创建。";
    createIntent.value = undefined;
    endpoint.value = "";
    eventTypeText.value = "";
    resource.replace({
      items: [result.subscription, ...(resource.data?.items ?? [])],
      nextCursor: resource.data?.nextCursor,
    });
  } catch (reason) {
    if (!currentScope(originalScope)) return;
    createError.value =
      reason instanceof Error ? reason : new Error("Callback 注册失败");
    refreshAfterAuthorizationError(reason, refreshCapabilities);
  } finally {
    if (currentScope(originalScope)) creating.value = false;
  }
}
function beginDelete(record: JsonRecord): void {
  const id = callbackID(record);
  if (!id || !eligible.value || commandRunning.value || replayBlocked.value)
    return;
  commandError.value = undefined;
  command.value = {
    kind: "delete",
    id,
    endpoint: String(record.endpoint ?? ""),
  };
}
async function runCommand(): Promise<void> {
  const current = command.value;
  if (!current || commandRunning.value || !eligible.value) return;
  const originalScope = scope?.();
  commandRunning.value = true;
  commandError.value = undefined;
  message.value = undefined;
  try {
    await client.callbacks.deleteSubscription(current.id);
    if (!currentScope(originalScope)) return;
    message.value = "订阅已删除，并已写入审计事实；不能撤回已发送的投递。";
    if (selectedSubscription.value === current.id)
      selectedSubscription.value = undefined;
    resource.replace({
      items: (resource.data?.items ?? []).filter(
        (item) => callbackID(item) !== current.id,
      ),
      nextCursor: resource.data?.nextCursor,
    });
    command.value = undefined;
  } catch (reason) {
    if (!currentScope(originalScope)) return;
    commandError.value =
      reason instanceof Error
        ? reason
        : new Error("Callback 命令结果未确认；请核实原投递");
    refreshAfterAuthorizationError(reason, refreshCapabilities);
  } finally {
    if (currentScope(originalScope)) commandRunning.value = false;
  }
}
function selectSubscription(record: JsonRecord): void {
  if (readEligible.value && !commandRunning.value && !command.value)
    selectedSubscription.value = callbackID(record);
}
</script>

<template>
  <PageLoading v-if="resource.loading" />
  <PageError
    v-else-if="resource.error"
    :error="resource.error"
    :retry="readEligible ? resource.reload : undefined"
  />
  <section v-else-if="resource.data">
    <PageHeader
      title="Callback 治理"
      description="端点注册由服务端执行 HTTPS challenge、DNS/私网策略和受控 egress；Secret 仅显示一次。选择订阅后可读取脱敏投递列表与详情。"
    />
    <IrisAlert v-if="message" tone="success">{{ message }}</IrisAlert>
    <IrisAlert v-if="!eligible" tone="warning"
      >当前 Callback
      动作能力未确认、拒绝或不可用；保留原输入和回执，不允许注册、删除或重放。能力报告不是执行凭据。</IrisAlert
    >
    <IrisAlert tone="warning"
      >注册仍使用原有限时 HTTP
      幂等缓存，不是持久、版本绑定的业务意图；内存键不能保证刷新恢复。删除仍是兼容管理命令。页面重放改用持久预览与
      CAS 排队，旧
      API/旧未知结果不自动升级；关闭页面不取消任何已提交命令。</IrisAlert
    >
    <IrisCard variant="outline">
      <template #header>注册 Callback 端点</template>
      <IrisAlert v-if="createError" tone="danger"
        >{{ createError.message }}
        <p v-if="createIntent">
          保留原表单与键；仅显式同意图重试，不自动重发。
        </p></IrisAlert
      >
      <form class="callback-create-grid" @submit.prevent="void create()">
        <IrisFormField
          label="HTTPS Endpoint"
          hint="端点必须回显服务端发送的 challenge"
          ><IrisInput
            v-model="endpoint"
            type="url"
            required
            :disabled="creating || Boolean(createIntent)"
            placeholder="https://callback.example.com/aero/events"
        /></IrisFormField>
        <IrisFormField
          label="允许的事件类型"
          hint="使用逗号或换行分隔；最多 64 个"
          ><IrisTextarea
            v-model="eventTypeText"
            required
            :disabled="creating || Boolean(createIntent)"
            placeholder="operation.completed, export.ready"
        /></IrisFormField>
        <div class="admin-filter-actions">
          <IrisButton
            type="submit"
            :loading="creating"
            :disabled="creating || !eligible"
            >验证并注册</IrisButton
          >
        </div>
      </form>
    </IrisCard>
    <IrisCard variant="outline">
      <template #header>当前订阅</template>
      <PagedRecordTable
        :page="resource.data"
        :load-page="loadSubscriptions"
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
            variant="outline"
            :disabled="!readEligible || commandRunning || Boolean(command)"
            @click="selectSubscription(record)"
            >查看此订阅投递</IrisButton
          >
          <IrisButton
            v-if="callbackID(record)"
            size="sm"
            variant="danger"
            :disabled="
              !eligible || commandRunning || Boolean(command) || replayBlocked
            "
            @click="beginDelete(record)"
            >删除</IrisButton
          >
        </template>
      </PagedRecordTable>
    </IrisCard>
    <CallbackDeliveries
      v-if="selectedSubscription"
      :key="`${selectedSubscription}:${deliveryRevision}`"
      :client="client"
      :subscription-i-d="selectedSubscription"
      :busy="commandRunning || creating || Boolean(command) || replayBlocked"
      @replay="startReplay"
    />
    <CallbackSecretDialog :secret="secret" @discard="secret = undefined" />
    <CallbackCommandDialog
      :command="command"
      :running="commandRunning"
      :error="commandError"
      :eligible="eligible"
      @cancel="command = undefined"
      @confirm="void runCommand()"
    />
  </section>
  <CallbackReplayPreview
    ref="replayPreview"
    :client="client"
    :disabled="commandRunning || creating || Boolean(command)"
    @busy="replayBlocked = $event"
    @queued="deliveryRevision += 1"
  />
</template>
