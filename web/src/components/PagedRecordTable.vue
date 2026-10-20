<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from "vue";
import { IrisAlert, IrisButton } from "@iris-ui-kit/vue";
import type { JsonRecord, PageData } from "../types";
import RecordTable from "./RecordTable.vue";
import { mergePage, type RecordColumn } from "./table";

type LoadPage = (
  cursor: string,
  signal: AbortSignal,
) => Promise<PageData<JsonRecord>>;
const props = defineProps<{
  page: PageData<JsonRecord>;
  loadPage: LoadPage;
  columns: RecordColumn[];
  empty?: string;
  actionsLabel?: string;
}>();

const current = ref<PageData<JsonRecord>>(props.page);
const loadingMore = ref(false);
const loadError = ref<Error>();
let generation = 0;
let active = true;
let request: AbortController | undefined;
onBeforeUnmount(() => {
  active = false;
  generation += 1;
  request?.abort();
});
const consumedCursors = new Set<string>();

watch(
  () => props.page,
  (page) => {
    generation += 1;
    request?.abort();
    request = undefined;
    consumedCursors.clear();
    current.value = page;
    loadingMore.value = false;
    loadError.value = undefined;
  },
);

async function loadMore(): Promise<void> {
  const cursor = current.value.nextCursor;
  if (!active || !cursor || loadingMore.value) return;
  const activeGeneration = generation;
  const controller = new AbortController();
  request = controller;
  loadingMore.value = true;
  loadError.value = undefined;
  try {
    const next = await props.loadPage(cursor, controller.signal);
    if (generation !== activeGeneration) return;
    const merged = mergePage(current.value, next, cursor, consumedCursors);
    consumedCursors.add(cursor);
    current.value = merged;
  } catch (reason) {
    if (generation !== activeGeneration) return;
    loadError.value =
      reason instanceof Error ? reason : new Error("下一页读取失败");
  } finally {
    if (active && generation === activeGeneration) {
      loadingMore.value = false;
      request = undefined;
    }
  }
}
</script>

<template>
  <RecordTable
    :records="current.items"
    :columns="columns"
    :empty="empty"
    :actions-label="actionsLabel"
  >
    <template v-if="$slots.actions" #actions="slotProps">
      <slot name="actions" v-bind="slotProps" />
    </template>
  </RecordTable>
  <IrisAlert v-if="loadError" tone="danger">
    <div class="alert-actions">
      <span>{{ loadError.message }}</span>
      <IrisButton size="sm" variant="outline" @click="void loadMore()"
        >重试</IrisButton
      >
    </div>
  </IrisAlert>
  <div v-if="current.nextCursor && !loadError" class="pagination-actions">
    <IrisButton
      size="sm"
      variant="outline"
      :loading="loadingMore"
      @click="void loadMore()"
    >
      加载更多
    </IrisButton>
  </div>
</template>
