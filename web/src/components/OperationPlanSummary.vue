<script setup lang="ts">
import { computed } from "vue";
import type { CommandPlan } from "../api/commandIntent";
import { jobDatasetLabels, jobDatasets } from "./jobSummaryFields";
import { operationAction, operationSource } from "./operationResultSummary";
const props = defineProps<{ plan: CommandPlan }>();
const steps = computed(() => props.plan.steps?.slice(0, 32) ?? []);
function scope(index: number): string[] | undefined {
  const step = steps.value[index];
  const values = jobDatasets(step.request_payload?.datasets);
  return values?.length &&
    values.every((name) => name.startsWith(`${step.source}.`))
    ? values
    : undefined;
}
</script>
<template>
  <section data-operation-plan class="operation-plan-summary">
    <h3>核对服务端有序步骤</h3>
    <p>
      这是规范化后的原计划。确认仅创建回执；运行、补偿与来源资源授权仍单独核对。
    </p>
    <ol>
      <li v-for="(step, index) in steps" :key="step.step_key">
        <h4>
          步骤 {{ index + 1 }} · {{ operationSource(step.source) }} ·
          {{ operationAction(step.action) }}
        </h4>
        <p>步骤键 {{ step.step_key }} · 动作 {{ step.action }}</p>
        <ul v-if="scope(index)">
          <li v-for="name in scope(index)" :key="name">
            {{ jobDatasetLabels[name] }} · {{ name }}
          </li>
        </ul>
        <p v-else>范围无法确认；请核实原计划，不推断为全部。</p>
        <p v-if="step.compensation_action">
          补偿：{{ operationAction(step.compensation_action) }} ·
          {{ step.compensation_action }}；沿用本步范围，不等于整体回滚。
        </p>
        <p v-else>无补偿动作。</p>
      </li>
    </ol>
  </section>
</template>
<style scoped>
.operation-plan-summary {
  min-width: 0;
  overflow-wrap: anywhere;
}
.operation-plan-summary h4 {
  margin-block-end: 8px;
}
.operation-plan-summary > ol {
  padding-inline-start: 24px;
}
.operation-plan-summary > ol > li {
  margin-block-end: 20px;
}
</style>
