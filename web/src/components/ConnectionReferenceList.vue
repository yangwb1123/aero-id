<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from "vue";
import { IrisAlert, IrisButton, IrisCard } from "@iris-ui-kit/vue";
import { AeroIdError, type AeroIdClient } from "../api/aeroId";
import type { JsonRecord, PageData } from "../types";
import { useAsyncResource } from "../hooks/useAsyncResource";
import PageError from "./PageError.vue";
import PageLoading from "./PageLoading.vue";
import PagedRecordTable from "./PagedRecordTable.vue";
import ConnectionReferenceDetails from "./ConnectionReferenceDetails.vue";
import SourceProjectionBrowser from "./SourceProjectionBrowser.vue";
import { isProjectionSource } from "./sourceProjectionScope";
import {
  referenceColumns,
  referenceSummary,
  type ReferenceKind,
} from "./referenceSummary";

const props = defineProps<{
  client: AeroIdClient;
  kind: ReferenceKind;
  canRead: boolean;
}>();
const title = computed(() =>
  props.kind === "sources" ? "来源账户" : "成员关系",
);
const selected = ref<JsonRecord>();
const browsingSource = ref(false);
const container = ref<HTMLElement>();
let active = true;
onBeforeUnmount(() => {
  active = false;
});
async function read(
  cursor?: string,
  signal?: AbortSignal,
): Promise<PageData<JsonRecord>> {
  if (!props.canRead)
    throw new AeroIdError(
      0,
      "capability.unavailable",
      "当前读取资格未确认；不会使用历史角色或引用授权",
    );
  const page =
    props.kind === "sources"
      ? await props.client.listSources(cursor, signal)
      : await props.client.listMemberships(cursor, signal);
  return {
    ...page,
    items: page.items.map((row) => referenceSummary(row, props.kind)),
  };
}
const resource = useAsyncResource((signal) => read(undefined, signal));
function reload(): void {
  if (!active || !props.canRead) return;
  selected.value = undefined;
  browsingSource.value = false;
  resource.reload();
}
watch(
  () => props.canRead,
  (allowed) => {
    if (!allowed) {
      selected.value = undefined;
      browsingSource.value = false;
    }
  },
);
async function select(row: JsonRecord): Promise<void> {
  if (!active || !props.canRead || resource.loading || resource.error) return;
  browsingSource.value = false;
  selected.value = row;
  await nextTick();
  if (active)
    container.value
      ?.querySelector<HTMLElement>("[data-reference-details]")
      ?.focus();
}
async function close(): Promise<void> {
  browsingSource.value = false;
  selected.value = undefined;
  await nextTick();
  if (active)
    container.value
      ?.querySelector<HTMLElement>("[data-reference-table]")
      ?.focus();
}
async function browseSource(): Promise<void> {
  if (
    !active ||
    !props.canRead ||
    props.kind !== "sources" ||
    resource.loading ||
    resource.error ||
    !isProjectionSource(selected.value?.source)
  )
    return;
  browsingSource.value = true;
  await nextTick();
  if (active)
    container.value
      ?.querySelector<HTMLElement>("[data-source-data-browser]")
      ?.focus();
}
async function closeBrowser(): Promise<void> {
  browsingSource.value = false;
  await nextTick();
  if (active)
    container.value
      ?.querySelector<HTMLElement>("[data-reference-details]")
      ?.focus();
}
</script>

<template>
  <section ref="container" :data-reference-list="kind">
    <IrisCard variant="outline">
      <template #header>{{ title }}</template>
      <IrisButton
        type="button"
        variant="outline"
        :disabled="!canRead || resource.loading"
        @click="reload"
        >刷新{{ title }}引用</IrisButton
      >
      <IrisAlert v-if="!canRead" tone="warning" title="当前读取资格未确认"
        >不会继续查询或借用来源角色授权；已返回的引用不代表当前许可。</IrisAlert
      >
      <PageLoading v-if="resource.loading" />
      <PageError
        v-else-if="resource.error"
        :error="resource.error"
        :retry="reload"
      />
      <template v-else-if="resource.data && canRead">
        <SourceProjectionBrowser
          v-if="selected && browsingSource"
          :client="client"
          :reference="selected"
          :can-read="canRead"
          @close="void closeBrowser()"
        />
        <ConnectionReferenceDetails
          v-else-if="selected"
          :reference="selected"
          :kind="kind"
          @close="void close()"
        >
          <template #actions>
            <IrisButton
              v-if="kind === 'sources' && isProjectionSource(selected.source)"
              type="button"
              variant="outline"
              @click="void browseSource()"
              >选择此来源的数据集</IrisButton
            >
          </template>
        </ConnectionReferenceDetails>
        <div v-show="!selected" data-reference-table tabindex="-1">
          <p>
            仅为已查看的引用页；可见条数不是来源总数。加载更多只读取原游标，不扫描全部。
          </p>
          <p v-if="resource.data.items.length === 0">
            {{
              kind === "sources"
                ? "本次未返回来源引用"
                : "本次未返回成员关系引用"
            }}；不证明来源没有账户或成员关系。
          </p>
          <div
            class="reference-table"
            role="region"
            :aria-label="`${title}引用表格，可横向滚动`"
            tabindex="0"
          >
            <PagedRecordTable
              :page="resource.data"
              :load-page="(cursor, signal) => read(cursor, signal)"
              :columns="referenceColumns(kind)"
              :empty="
                kind === 'sources'
                  ? '本次未返回来源引用'
                  : '本次未返回成员关系引用'
              "
              actions-label="查看原引用"
            >
              <template #actions="{ record }"
                ><IrisButton
                  type="button"
                  size="sm"
                  variant="outline"
                  @click="void select(record)"
                  >{{
                    kind === "sources" ? "查看来源范围" : "查看成员范围"
                  }}</IrisButton
                ></template
              >
            </PagedRecordTable>
          </div>
        </div>
      </template>
    </IrisCard>
  </section>
</template>

<style scoped>
section {
  margin-block: 20px;
  min-width: 0;
}
p {
  line-height: 1.7;
}
.reference-table {
  overflow-x: auto;
}
.reference-table :deep(.table-scroll) {
  overflow-x: visible;
}
.reference-table :deep(table) {
  min-width: 720px;
}
.reference-table :deep(th:last-child),
.reference-table :deep(td:last-child) {
  position: sticky;
  right: 0;
  background: var(--iris-background, white);
}
</style>
