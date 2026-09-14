<script setup lang="ts">
import {
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisInput,
  IrisSelect,
  IrisTextarea,
} from "@iris-ui-kit/vue";
import type { OperationStepInput } from "../api/aeroId";
import type { Dataset } from "../types";
import {
  operationActions,
  operationSources,
  type OperationStepDraft,
} from "./operationFormHelpers";

const props = defineProps<{
  steps: OperationStepDraft[];
  datasets: Dataset[];
}>();
const emit = defineEmits<{ change: [steps: OperationStepDraft[]] }>();
const compensationItems = [
  { value: "", label: "无补偿动作" },
  ...operationActions,
];
function update(id: string, patch: Partial<OperationStepDraft>): void {
  emit(
    "change",
    props.steps.map((step) => (step.id === id ? { ...step, ...patch } : step)),
  );
}
function remove(id: string): void {
  emit(
    "change",
    props.steps.filter((step) => step.id !== id),
  );
}
function add(): void {
  const defaultDataset = props.datasets.find(
    (dataset) => dataset.source === "aero-im",
  )?.name;
  emit("change", [
    ...props.steps,
    {
      id: crypto.randomUUID(),
      stepKey: `step-${props.steps.length + 1}`,
      source: "aero-im",
      action: "summary",
      compensationAction: "",
      payload: JSON.stringify(
        defaultDataset ? { datasets: [defaultDataset] } : {},
        null,
        2,
      ),
    },
  ]);
}
function sourceItems(
  source: OperationStepInput["source"],
): Array<{ value: string; label: string }> {
  return props.datasets
    .filter((dataset) => dataset.source === source)
    .map((dataset) => ({ value: dataset.name, label: dataset.name }));
}
</script>

<template>
  <div class="operation-step-list">
    <IrisCard v-for="(step, index) in steps" :key="step.id" variant="outline">
      <template #header>步骤 {{ index + 1 }}</template>
      <template #footer>
        <div class="detail-actions">
          <IrisButton
            type="button"
            size="sm"
            variant="ghost"
            @click="remove(step.id)"
            >移除步骤</IrisButton
          >
        </div>
      </template>
      <div class="operation-step-grid">
        <IrisFormField label="Step Key" required>
          <IrisInput
            :model-value="step.stepKey"
            maxlength="128"
            @update:model-value="update(step.id, { stepKey: $event })"
          />
        </IrisFormField>
        <IrisFormField label="来源" required>
          <IrisSelect
            :model-value="step.source"
            :items="operationSources"
            @update:model-value="
              update(step.id, {
                source: String($event) as OperationStepInput['source'],
              })
            "
          />
        </IrisFormField>
        <IrisFormField label="动作" required>
          <IrisSelect
            :model-value="step.action"
            :items="operationActions"
            @update:model-value="
              update(step.id, {
                action: String($event) as OperationStepInput['action'],
              })
            "
          />
        </IrisFormField>
        <IrisFormField
          label="补偿动作"
          hint="仅在服务端允许的 Saga 补偿策略中使用"
        >
          <IrisSelect
            :model-value="step.compensationAction"
            :items="compensationItems"
            @update:model-value="
              update(step.id, { compensationAction: String($event) })
            "
          />
        </IrisFormField>
        <IrisFormField
          label="Request Payload JSON"
          hint="涉及 IM/Vault 的摘要、同步、删除动作时，datasets 必须是当前 allow-list 中的非空数组"
        >
          <IrisTextarea
            :model-value="step.payload"
            autosize
            :max-rows="10"
            @update:model-value="update(step.id, { payload: $event })"
          />
        </IrisFormField>
        <div
          v-if="sourceItems(step.source).length"
          class="operation-dataset-hint"
        >
          可用 {{ step.source }} datasets：{{
            sourceItems(step.source)
              .map((item) => item.value)
              .join("、")
          }}
        </div>
      </div>
    </IrisCard>
    <IrisButton
      type="button"
      variant="outline"
      :disabled="steps.length >= 32"
      @click="add"
      >添加步骤（{{ steps.length }}/32）</IrisButton
    >
  </div>
</template>
