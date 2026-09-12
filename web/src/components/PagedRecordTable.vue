<script setup lang="ts">
import { ref, watch } from "vue";
import { IrisAlert, IrisButton } from "@iris-ui-kit/vue";
import type { JsonRecord, PageData } from "../types";
import RecordTable from "./RecordTable.vue";
import { mergePage, type RecordColumn } from "./table";

type LoadPage = (cursor: string) => Promise<PageData<JsonRecord>>;
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

watch(
  () => props.page,
  (page) => {
    generation += 1;
    current.value = page;
    loadingMore.value = false;
    loadError.value = undefined;
  },
);

async function loadMore(): Promise<void> {
  const cursor = current.value.nextCursor;
  if (!cursor || loadingMore.value) return;
  const activeGeneration = generation;
  loadingMore.value = true;
  loadError.value = undefined;
  try {
    const next = await props.loadPage(cursor);
    if (generation !== activeGeneration) return;
    current.value = mergePage(current.value, next, cursor);
  } catch (reason) {
    if (generation !== activeGeneration) return;
    loadError.value =
      reason instanceof Error ? reason : new Error("下一页读取失败");
  } finally {
    if (generation === activeGeneration) loadingMore.value = false;
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
