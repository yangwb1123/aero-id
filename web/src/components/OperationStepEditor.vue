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
import OperationDatasetPicker from "./OperationDatasetPicker.vue";
import {
  changeOperationSource,
  newSelectionOperationStep,
  operationDraftSelection,
} from "./operationStepSelection";
import { operationAction } from "./operationResultSummary";

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
  if (props.steps.length >= 32) return;
  emit("change", [...props.steps, newSelectionOperationStep(props.steps)]);
}
function changeSource(step: OperationStepDraft, value: unknown): void {
  const patch = changeOperationSource(
    step,
    props.datasets,
    String(value) as OperationStepInput["source"],
  );
  if (patch) update(step.id, patch);
}
function move(index: number, offset: number): void {
  const next = index + offset;
  if (next < 0 || next >= props.steps.length) return;
  const steps = [...props.steps];
  [steps[index], steps[next]] = [steps[next], steps[index]];
  emit("change", steps);
}
</script>

<template>
  <div class="operation-step-list">
    <IrisCard
      v-for="(step, index) in steps"
      :key="step.id"
      :data-operation-draft="step.stepKey"
      variant="outline"
    >
      <template #header
        >步骤 {{ index + 1 }} · {{ operationAction(step.action) }}</template
      >
      <template #footer>
        <div class="detail-actions">
          <IrisButton
            type="button"
            size="sm"
            variant="ghost"
            :disabled="index === 0"
            @click="move(index, -1)"
            >上移</IrisButton
          >
          <IrisButton
            type="button"
            size="sm"
            variant="ghost"
            :disabled="index === steps.length - 1"
            @click="move(index, 1)"
            >下移</IrisButton
          >
          <IrisButton
            type="button"
            size="sm"
            variant="ghost"
            @click="remove(step.id)"
            >移除步骤</IrisButton
          >
        </div>
      </template>
      <IrisFormField
        label="来源"
        required
        hint="切换来源会清空本步选择；步骤键、动作和其他高级参数不变。"
      >
        <IrisSelect
          :model-value="step.source"
          :items="operationSources"
          :teleport="false"
          :disabled="!operationDraftSelection(step, datasets)"
          @update:model-value="changeSource(step, $event)"
        />
      </IrisFormField>
      <OperationDatasetPicker
        :step="step"
        :datasets="datasets"
        @change="update(step.id, { payload: $event })"
      />
      <p>动作：{{ operationAction(step.action) }} · {{ step.action }}</p>
      <p v-if="step.compensationAction">
        补偿：{{ operationAction(step.compensationAction) }} ·
        {{ step.compensationAction }}（独立来源动作，不等于回滚）
      </p>
      <p v-else>无补偿动作。</p>
      <details data-operation-step-expert>
        <summary>高级步骤参数（键、动作与补偿 / JSON）</summary>
        <div class="operation-step-grid">
          <IrisFormField
            label="Step Key"
            required
            hint="自动生成且移位不改键；专家修改会改变原计划指纹。"
          >
            <IrisInput
              :model-value="step.stepKey"
              maxlength="128"
              @update:model-value="update(step.id, { stepKey: $event })"
            />
          </IrisFormField>
          <IrisFormField label="动作" required>
            <IrisSelect
              :model-value="step.action"
              :items="operationActions"
              :teleport="false"
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
              :teleport="false"
              @update:model-value="
                update(step.id, { compensationAction: String($event) })
              "
            />
          </IrisFormField>
          <IrisFormField
            label="Request Payload JSON"
            hint="每个正向和补偿步骤均使用显式同源 datasets；所有来源都必须包含当前 allow-list 中的非空数组"
          >
            <IrisTextarea
              :model-value="step.payload"
              autosize
              :max-rows="10"
              @update:model-value="update(step.id, { payload: $event })"
            />
          </IrisFormField>
        </div>
      </details>
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
