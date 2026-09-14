<script setup lang="ts">
import { IrisAlert } from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import AccountDataPanel from "../components/AccountDataPanel.vue";
import PageError from "../components/PageError.vue";
import PageHeader from "../components/PageHeader.vue";
import PageLoading from "../components/PageLoading.vue";
import { useAsyncResource } from "../hooks/useAsyncResource";

const { client } = defineProps<{ client: AeroIdClient }>();
const resource = useAsyncResource(() => client.listDatasets());
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
      title="账户数据浏览"
      description="只读取 aero-id 的 typed dataset registry 和版本化聚合投影；不会代理源系统路径或凭据。"
    />
    <IrisAlert tone="info">
      查询范围由服务端 allow-list 决定。请求可声明 eventual、bounded 或
      strong；结果会明确标记 partial、stale、unavailable 和 unknown。
    </IrisAlert>
    <AccountDataPanel :client="client" :datasets="resource.data" />
  </section>
</template>
