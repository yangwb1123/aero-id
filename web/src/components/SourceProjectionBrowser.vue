<script setup lang="ts">
import { IrisAlert, IrisButton } from "@iris-ui-kit/vue";
import { AeroIdError, type AeroIdClient } from "../api/aeroId";
import type { JsonRecord } from "../types";
import { useAsyncResource } from "../hooks/useAsyncResource";
import AccountDataPanel from "./AccountDataPanel.vue";
import PageError from "./PageError.vue";
import PageLoading from "./PageLoading.vue";
import { isProjectionSource } from "./sourceProjectionScope";
const props = defineProps<{
  client: AeroIdClient;
  reference: JsonRecord;
  canRead: boolean;
}>();
defineEmits<{ close: [] }>();
const resource = useAsyncResource((signal) => {
  if (!props.canRead || !isProjectionSource(props.reference.source))
    throw new AeroIdError(
      0,
      "capability.unavailable",
      "当前来源读取资格未确认",
    );
  return props.client.listDatasets(signal);
});
function reload(): void {
  if (props.canRead && isProjectionSource(props.reference.source))
    resource.reload();
}
</script>

<template>
  <section
    data-source-data-browser
    aria-label="按来源选择账户投影"
    tabindex="-1"
  >
    <h3>按来源选择账户投影</h3>
    <IrisButton type="button" variant="outline" @click="$emit('close')"
      >返回来源范围</IrisButton
    >
    <p>所选来源：{{ reference.source_label }} · {{ reference.source }}</p>
    <p>
      原引用（仅返回上下文）：{{ reference.scope_label }} ·
      {{ reference.scope_id }}
    </p>
    <IrisAlert tone="warning" title="账户级投影，不是原生资源范围">
      来源引用仅帮助选择同源数据集；不是按原生租户、工作空间或桶 ID 过滤。
      下方读取当前账户的所选本地投影，不会把原生 ID
      或来源角色传作查询范围或授权。
      连接未启用仍可能有历史投影；不会刷新来源、创建同步或导出任务。
    </IrisAlert>
    <PageLoading v-if="resource.loading" />
    <PageError
      v-else-if="resource.error"
      :error="resource.error"
      :retry="reload"
    />
    <AccountDataPanel
      v-else-if="
        resource.data && canRead && isProjectionSource(reference.source)
      "
      :client="client"
      :datasets="resource.data"
      :source="reference.source"
    />
  </section>
</template>

<style scoped>
section {
  margin-block: 16px;
  min-width: 0;
}
p {
  overflow-wrap: anywhere;
  line-height: 1.7;
}
[data-iris-alert] {
  margin-block: 16px;
}
</style>
