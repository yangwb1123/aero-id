<script setup lang="ts">
import { computed } from "vue";
import { IrisAlert, IrisCheckbox } from "@iris-ui-kit/vue";
import type { Dataset } from "../types";
import type { OperationStepDraft } from "./operationFormHelpers";
import { jobDatasetLabels } from "./jobSummaryFields";
import {
  operationDatasetChoices,
  operationDraftSelection,
  selectOperationDataset,
} from "./operationStepSelection";
const props = defineProps<{ step: OperationStepDraft; datasets: Dataset[] }>();
const emit = defineEmits<{ change: [payload: string] }>();
const choices = computed(() =>
  operationDatasetChoices(props.datasets, props.step.source),
);
const selection = computed(() =>
  operationDraftSelection(props.step, props.datasets),
);
function select(name: string, checked: boolean): void {
  const payload = selectOperationDataset(
    props.step,
    props.datasets,
    name,
    checked,
  );
  if (payload !== undefined) emit("change", payload);
}
</script>
<template>
  <fieldset class="operation-scope-picker">
    <legend>
      本步骤的数据集范围（{{ selection?.selected.length ?? "未确认" }} 项）
    </legend>
    <p>不隐含全部；正向与补偿共用此范围。</p>
    <IrisAlert v-if="!selection" tone="warning"
      >高级参数的范围无法确认；请先在高级参数修正 JSON 和同源
      datasets，不自动修复。</IrisAlert
    >
    <IrisAlert v-else-if="!selection.selected.length" tone="info"
      >请选择本步骤的数据集，再获取服务端预览。</IrisAlert
    >
    <p v-if="!choices.length">
      当前目录没有此来源的批准数据集；不能准备此步骤。
    </p>
    <IrisCheckbox
      v-for="dataset in choices"
      :key="dataset.name"
      :model-value="selection?.selected.includes(dataset.name) ?? false"
      :disabled="!selection"
      @update:model-value="select(dataset.name, $event)"
    >
      {{ jobDatasetLabels[dataset.name]
      }}<span class="dataset-name"
        >{{ dataset.name }}{{ dataset.pii ? " · PII" : "" }}</span
      >
    </IrisCheckbox>
  </fieldset>
</template>
<style scoped>
.operation-scope-picker {
  display: grid;
  gap: 12px;
  min-width: 0;
  margin-block: 16px;
}
.operation-scope-picker p {
  margin: 0;
}
.operation-scope-picker :deep(label),
.dataset-name {
  overflow-wrap: anywhere;
}
.dataset-name {
  display: block;
  font-size: 12px;
  color: var(--iris-muted);
}
</style>
