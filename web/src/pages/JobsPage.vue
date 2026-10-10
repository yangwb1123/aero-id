<script setup lang="ts">
import { computed, ref, watch } from "vue";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisCheckbox,
} from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import JobDetails from "../components/JobDetails.vue";
import CommandPreviewDialog from "../components/CommandPreviewDialog.vue";
import type { CommandIntent } from "../api/commandIntent";
import PagedRecordTable from "../components/PagedRecordTable.vue";
import PageError from "../components/PageError.vue";
import PageHeader from "../components/PageHeader.vue";
import PageLoading from "../components/PageLoading.vue";
import { useAsyncResource } from "../hooks/useAsyncResource";
import { useAccountScope } from "../hooks/accountContext";
import { useCapabilities } from "../hooks/capabilityContext";
import { capabilityAvailable } from "../api/capabilities";
import {
  loadJobIntents,
  saveJobIntents,
  maxStoredJobIntents,
} from "../components/jobIntentStorage";
import {
  nextJobCreateIntent,
  syncJobID,
  type JobCreateIntent,
} from "../components/jobHelpers";
const props = defineProps<{ client: AeroIdClient; jobId?: string }>();
const { client } = props;
const resource = useAsyncResource(async (signal) => {
  const [jobs, datasets] = await Promise.all([
    client.listSyncJobs(undefined, signal),
    client.listDatasets(signal),
  ]);
  return { jobs, datasets };
});
const action = ref<"sync" | "export">();
const actionError = ref<Error>();
const selectedJob = ref(props.jobId);
watch(
  () => props.jobId,
  (value) => {
    selectedJob.value = value;
  },
);
const selectedDatasets = ref<string[]>([]);
function selectDataset(name: string, selected: boolean): void {
  selectedDatasets.value = selected
    ? [...new Set([...selectedDatasets.value, name])]
    : selectedDatasets.value.filter((value) => value !== name);
}
const capabilities = useCapabilities();
function canCreate(kind: "sync" | "export"): boolean {
  return (
    !capabilities ||
    capabilityAvailable(capabilities(), [`account.self.${kind}`])
  );
}
function datasetEnabled(name: string): boolean {
  return (
    !capabilities ||
    Boolean(
      capabilities()?.datasets.find((item) => item.name === name)?.enabled,
    )
  );
}
const accountScope = useAccountScope();
const scopeReady = computed(() => !accountScope || Boolean(accountScope()));
const createIntents = ref<Record<string, JobCreateIntent>>({});
const legacyIntents = ref<Record<string, JobCreateIntent>>({});
const preview = ref<CommandIntent>();
const previewSignature = ref<string>();
const intentScope = () => {
  const scope = accountScope?.();
  return scope ? `${scope}:server-preview-v1` : undefined;
};
watch(
  () => accountScope?.(),
  (scope) => {
    try {
      legacyIntents.value = loadJobIntents(window.sessionStorage, scope);
      createIntents.value = loadJobIntents(
        window.sessionStorage,
        intentScope(),
      );
      preview.value = undefined;
    } catch {
      createIntents.value = {};
      legacyIntents.value = {};
    }
  },
  { immediate: true },
);
function persistIntents(): void {
  try {
    saveJobIntents(window.sessionStorage, intentScope(), createIntents.value);
  } catch {
    /* Memory-only fallback when browser storage is blocked. */
  }
}
function keyForCreate(
  kind: "sync" | "export",
  datasets: string[],
): JobCreateIntent {
  const candidate = nextJobCreateIntent({ jobType: kind, datasets }, undefined);
  const previous = createIntents.value[candidate.signature];
  if (previous) return previous;
  if (Object.keys(createIntents.value).length >= maxStoredJobIntents) {
    throw new Error("存在过多未确认创建意图；请先核查已有任务，未发送新命令。");
  }
  createIntents.value[candidate.signature] = candidate;
  persistIntents();
  return candidate;
}
async function create(kind: "sync" | "export"): Promise<void> {
  if (action.value) return;
  const datasets =
    resource.data?.datasets
      .filter((dataset) => selectedDatasets.value.includes(dataset.name))
      .map((dataset) => dataset.name) ?? [];
  if (
    !canCreate(kind) ||
    (kind === "sync" && datasets.some((name) => !datasetEnabled(name)))
  ) {
    actionError.value = new Error(
      "当前命令或来源未启用/尚未获确认；未发送创建请求。",
    );
    return;
  }
  if (!scopeReady.value) {
    actionError.value = new Error("账户上下文尚未确认；未发送创建请求。");
    return;
  }
  if (datasets.length === 0) {
    actionError.value = new Error("请显式选择数据集；未发送任务创建请求。");
    return;
  }
  if (Object.keys(legacyIntents.value).length > 0) {
    actionError.value = new Error(
      "旧接口存在未确认创建请求；请先核实旧任务，不会将原键转换成第二个预览命令。",
    );
    return;
  }
  action.value = kind;
  actionError.value = undefined;
  try {
    const intent = keyForCreate(kind, datasets);
    const prepared = await client.commands.prepare(
      { kind, datasets },
      intent.idempotencyKey,
    );
    const report = capabilities?.();
    if (
      report &&
      (prepared.account_id !== report.account_id ||
        prepared.tenant_id !== report.tenant_id ||
        prepared.issuer !== report.issuer ||
        prepared.client_id !== report.client_id)
    ) {
      throw new Error("命令预览与当前身份/账户不一致；未发送执行请求。");
    }
    previewSignature.value = intent.signature;
    if (prepared.status === "executed") confirmed(prepared);
    else preview.value = prepared;
  } catch (reason) {
    actionError.value =
      reason instanceof Error ? reason : new Error("任务创建失败");
  } finally {
    action.value = undefined;
  }
}
function confirmed(intent: CommandIntent): void {
  if (
    intent.status !== "executed" ||
    intent.receipt_kind !== "sync_job" ||
    !intent.receipt_id
  ) {
    actionError.value = new Error(
      "回执未确认；保留原意图，不会生成第二个命令。",
    );
    return;
  }
  if (previewSignature.value)
    delete createIntents.value[previewSignature.value];
  persistIntents();
  preview.value = undefined;
  selectedJob.value = intent.receipt_id;
  resource.reload();
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
      title="同步与导出"
      description="先获取服务端预览，再确认创建任务；unknown 需要对账，不会自动重试写入。"
    >
      <template #actions>
        <IrisButton
          :loading="action === 'sync'"
          :disabled="
            Boolean(action) ||
            selectedDatasets.length === 0 ||
            !scopeReady ||
            !canCreate('sync')
          "
          @click="void create('sync')"
          >同步所选数据集</IrisButton
        >
        <IrisButton
          variant="outline"
          :loading="action === 'export'"
          :disabled="
            Boolean(action) ||
            selectedDatasets.length === 0 ||
            !scopeReady ||
            !canCreate('export')
          "
          @click="void create('export')"
          >创建加密导出</IrisButton
        >
      </template>
    </PageHeader>
    <IrisAlert v-if="actionError" tone="danger">{{
      actionError.message
    }}</IrisAlert>
    <IrisAlert v-if="!scopeReady" tone="warning" title="账户上下文未确认">
      创建命令暂不可用；不会使用占位账户或仅替换 tenant_id 发起请求。
    </IrisAlert>
    <IrisAlert
      v-if="Object.keys(createIntents).length > 0"
      tone="warning"
      title="存在未确认创建请求"
    >
      相同范围的手动预览保留原键；执行回应未确认时只能重试原预览。刷新按账户、租户和客户端隔离恢复，不会自动重发写入。
    </IrisAlert>
    <IrisAlert v-if="Object.keys(legacyIntents).length > 0" tone="warning"
      >旧接口存在未确认任务；暂停新预览，请先在任务列表核实。</IrisAlert
    >
    <CommandPreviewDialog
      v-if="preview"
      :key="preview.intent_id"
      :client="client"
      :intent="preview"
      @close="preview = undefined"
      @executed="confirmed"
    />
    <IrisCard variant="outline">
      <fieldset :disabled="Boolean(action)" class="dataset-selection">
        <legend>显式选择数据集（{{ selectedDatasets.length }} 项）</legend>
        <p>
          已注册不等于已启用、有权限或已同步。导出包含核心账户/资料和所选来源引用，不包含
          Vault 文件或完整 IM 消息。
        </p>
        <IrisCheckbox
          v-for="dataset in resource.data.datasets"
          :key="dataset.name"
          :disabled="Boolean(action)"
          :model-value="selectedDatasets.includes(dataset.name)"
          @update:model-value="selectDataset(dataset.name, $event)"
        >
          {{ dataset.name }} · {{ dataset.source
          }}{{ dataset.pii ? " · PII" : ""
          }}{{
            datasetEnabled(dataset.name) ? "" : " · 源未启用（只导出已有投影）"
          }}
        </IrisCheckbox>
      </fieldset>
      <p>
        创建成功仅表示任务已持久接受；最终完成、unknown
        与投影可见性以详情和审计证据为准。
      </p>
    </IrisCard>
    <IrisCard variant="outline">
      <PagedRecordTable
        :page="resource.data.jobs"
        :load-page="(cursor) => client.listSyncJobs(cursor)"
        :columns="[
          { key: 'id', label: '任务 ID', aliases: ['job_id'] },
          { key: 'job_type', label: '类型', aliases: ['type'] },
          { key: 'status', label: '状态', kind: 'status' },
          { key: 'created_at', label: '创建时间', kind: 'date' },
          { key: 'completed_at', label: '完成时间', kind: 'date' },
        ]"
        empty="暂无同步或导出任务"
      >
        <template #actions="{ record }">
          <IrisButton
            v-if="syncJobID(record)"
            size="sm"
            variant="ghost"
            @click="selectedJob = syncJobID(record)"
            >查看详情</IrisButton
          >
        </template>
      </PagedRecordTable>
    </IrisCard>
    <JobDetails
      v-if="selectedJob"
      :key="selectedJob"
      :client="client"
      :job-id="selectedJob"
      @close="selectedJob = undefined"
      @changed="resource.reload"
    />
  </section>
</template>
