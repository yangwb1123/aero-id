<script setup lang="ts">
import { computed } from "vue";
import { IrisAlert } from "@iris-ui-kit/vue";
import type { JsonRecord } from "../types";
import { jobCount, jobDatasetLabels, jobText } from "./jobSummaryFields";
import { operationStepScope } from "./operationStepScope";
import {
  compensationState,
  operationAction,
  operationCode,
  operationResult,
  operationSource,
  operationStepKey,
  operationStepState,
} from "./operationResultSummary";
const props = defineProps<{ step: JsonRecord; index: number }>();
const state = computed(() => operationStepState(props.step.status));
const scope = computed(() => operationStepScope(props.step));
const forward = computed(() => operationResult(props.step));
const compensation = computed(() => operationResult(props.step, true));
const compensationStatus = computed(() => compensationState(props.step));
const hasCompensation = computed(() =>
  [
    "compensation_action",
    "compensation_status",
    "compensation_source_operation_id",
    "compensation_result",
    "compensation_error_code",
  ].some((key) => props.step[key] != null && props.step[key] !== ""),
);
</script>

<template>
  <li
    :data-operation-step="operationStepKey(step, index)"
    class="operation-step"
  >
    <h4>
      {{ index + 1 }}. {{ operationStepKey(step, index) }} ·
      {{ operationSource(step.source) }}
    </h4>
    <p>{{ operationAction(step.action) }}</p>
    <section class="operation-scope" aria-label="本步骤数据集范围">
      <h5>本步骤明确范围</h5>
      <IrisAlert v-if="scope.issue" tone="warning">{{ scope.issue }}</IrisAlert>
      <ul v-else data-operation-step-datasets>
        <li v-for="name in scope.datasets" :key="name">
          {{ jobDatasetLabels[name] }} · <code>{{ name }}</code>
        </li>
      </ul>
      <p v-if="scope.datasets && step.compensation_action">
        补偿沿用本步骤同一持久范围；不表示补偿已执行或来源允许该动作。
      </p>
    </section>
    <IrisAlert :tone="state.tone" :title="state.title">{{
      state.detail
    }}</IrisAlert>
    <dl class="operation-facts">
      <div>
        <dt>正向尝试次数（非进度）</dt>
        <dd>{{ jobCount(step.attempt) }}</dd>
      </div>
      <div>
        <dt>正向来源操作 ID</dt>
        <dd>
          <code>{{ jobText(step.source_operation_id) }}</code>
        </dd>
      </div>
      <div v-if="operationCode(step.error_code)">
        <dt>正向结果码</dt>
        <dd>
          <code>{{ operationCode(step.error_code) }}</code>
        </dd>
      </div>
    </dl>
    <IrisAlert v-if="forward.incomplete" tone="warning" title="所请求摘要不完整"
      >步骤成功与摘要完整性是两回事，请核对跳过范围。</IrisAlert
    >
    <dl v-if="forward.fields.length" class="operation-facts">
      <div v-for="field in forward.fields" :key="field.label">
        <dt>{{ field.label }}</dt>
        <dd>{{ field.value }}</dd>
      </div>
    </dl>
    <p>{{ forward.note }}</p>
    <section
      v-if="hasCompensation"
      class="operation-compensation"
      aria-label="本步骤补偿记录"
    >
      <h5>补偿计划与结果 · {{ operationAction(step.compensation_action) }}</h5>
      <IrisAlert
        :tone="compensationStatus.tone"
        :title="compensationStatus.title"
        >{{ compensationStatus.detail }}</IrisAlert
      >
      <dl class="operation-facts">
        <div>
          <dt>补偿尝试次数（非进度）</dt>
          <dd>{{ jobCount(step.compensation_attempt) }}</dd>
        </div>
        <div>
          <dt>补偿来源操作 ID</dt>
          <dd>
            <code>{{ jobText(step.compensation_source_operation_id) }}</code>
          </dd>
        </div>
        <div v-if="operationCode(step.compensation_error_code)">
          <dt>补偿结果码</dt>
          <dd>
            <code>{{ operationCode(step.compensation_error_code) }}</code>
          </dd>
        </div>
        <div v-for="field in compensation.fields" :key="field.label">
          <dt>{{ field.label }}</dt>
          <dd>{{ field.value }}</dd>
        </div>
      </dl>
      <IrisAlert
        v-if="compensation.incomplete"
        tone="warning"
        title="补偿摘要不完整"
        >请分别核对补偿状态与来源报告范围。</IrisAlert
      >
      <p>{{ compensation.note }}</p>
      <p>
        计划不代表已执行；补偿是独立动作，不代表整体回滚。执行先后与时间请对照下方审计事实。
      </p>
    </section>
    <p v-else>未提供补偿记录；不据此判断此前效果已撤回或无需补偿。</p>
  </li>
</template>

<style scoped>
.operation-step,
.operation-compensation,
.operation-scope {
  display: grid;
  gap: 12px;
  min-width: 0;
}
.operation-step {
  border: 1px solid var(--iris-color-border, #d8dee8);
  border-radius: 8px;
  padding: 16px;
}
.operation-compensation {
  border-inline-start: 3px solid var(--iris-color-border, #d8dee8);
  padding-inline-start: 14px;
}
h4,
h5,
p {
  margin: 0;
}
h4 {
  font-size: 1rem;
}
h5 {
  font-size: 0.95rem;
}
</style>
