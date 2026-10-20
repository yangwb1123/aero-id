<script setup lang="ts">
import { computed } from "vue";
import { IrisAlert } from "@iris-ui-kit/vue";
import type { JsonRecord } from "../types";
import { jobText, jobTime } from "./jobSummaryFields";
import { operationStepScope } from "./operationStepScope";
import {
  operationCode,
  operationState,
  operationSteps,
  operationType,
} from "./operationResultSummary";
import OperationStepResult from "./OperationStepResult.vue";
import TaskAuditNavigation from "./TaskAuditNavigation.vue";
const props = defineProps<{ operation: JsonRecord }>();
const state = computed(() => operationState(props.operation.status));
const steps = computed(() => operationSteps(props.operation.steps));
const scopeMissing = computed(() =>
  steps.value.items.some((step) => !operationStepScope(step).datasets),
);
const dangerous = computed(
  () =>
    props.operation.operation_type === "erase" ||
    steps.value.items.some((step) =>
      [step.action, step.compensation_action].some((action) =>
        ["erase", "delete", "source.erase"].includes(action as string),
      ),
    ),
);
</script>

<template>
  <section
    data-operation-summary
    class="operation-result-summary"
    aria-label="Operation 业务结果"
  >
    <h2>{{ operationType(operation.operation_type) }} · {{ state.title }}</h2>
    <IrisAlert :tone="state.tone" :title="state.title">{{
      state.detail
    }}</IrisAlert>
    <IrisAlert v-if="dangerous" tone="warning" title="含危险来源动作或补偿"
      >来源擦除/删除可能不可逆；读取本页不会执行。计划、当前操作者权限与详情链接都不替代原发起者授权或来源资源
      ACL。</IrisAlert
    >
    <dl class="operation-facts">
      <div>
        <dt>目标账户</dt>
        <dd>
          <code>{{ jobText(operation.account_id) }}</code>
        </dd>
      </div>
      <div>
        <dt>租户</dt>
        <dd>{{ jobText(operation.tenant_id) }}</dd>
      </div>
      <div>
        <dt>账户主区域</dt>
        <dd>{{ jobText(operation.home_region) }}</dd>
      </div>
      <div>
        <dt>创建时间</dt>
        <dd>{{ jobTime(operation.created_at) }}</dd>
      </div>
      <div>
        <dt>最近更新时间</dt>
        <dd>{{ jobTime(operation.updated_at) }}</dd>
      </div>
      <div>
        <dt>完成时间</dt>
        <dd>
          {{ jobTime(operation.completed_at)
          }}<span v-if="operation.completed_at == null"
            >（不据此推断未执行）</span
          >
        </dd>
      </div>
      <div v-if="operationCode(operation.error_code)">
        <dt>操作结果码</dt>
        <dd>
          <code>{{ operationCode(operation.error_code) }}</code>
        </dd>
      </div>
    </dl>
    <slot name="actions" />
    <section aria-label="步骤与补偿时间线">
      <h3>步骤与补偿</h3>
      <p>
        按本次服务端返回的步骤顺序展示最新记录，不是尝试历史或执行时间线；补偿可能逆序执行。详情未提供逐步骤时间，实际先后请核对审计事实。
      </p>
      <IrisAlert v-if="steps.issue" tone="warning">{{ steps.issue }}</IrisAlert>
      <p v-else-if="!steps.items.length">
        本次回应未列出步骤；不证明没有执行或已完成所有动作。
      </p>
      <ol v-else class="operation-step-list">
        <OperationStepResult
          v-for="(step, index) in steps.items"
          :key="index"
          :step="step"
          :index="index"
        />
      </ol>
      <IrisAlert
        v-if="scopeMissing"
        tone="info"
        title="本详情未提供逐步骤数据集范围"
        >至少一步缺少可确认的明确范围。不从动作、顶层请求或历史审计补位；缺失或无效范围时原
        Run / 对账继续暂停，不放宽资格。</IrisAlert
      >
    </section>
    <section aria-label="安全的下一步">
      <h3>下一步</h3>
      <p>{{ state.detail }}</p>
      <p>
        「刷新状态」只 GET
        原回执，不运行或对账；关闭/离开页面不是取消或回滚。当前资源摘要不是原操作时点，完成也不证明当前投影新鲜。
      </p>
      <nav class="detail-actions" aria-label="Operation 结果后续导航">
        <a href="#/resources">查看当前资源摘要</a>
        <a href="#/audit">打开审计台账</a>
      </nav>
      <TaskAuditNavigation
        page="operations"
        :task-id="operation.operation_id"
        :request-id="operation.request_id"
      />
      <p>
        本操作 ID：<code>{{ jobText(operation.operation_id) }}</code
        >；Request ID：<code>{{ jobText(operation.request_id) }}</code
        >。上方链接分别预填原标识，进入台账后显式查询；不要与来源操作
        ID、审计事件 ID 混用。
      </p>
    </section>
  </section>
</template>

<style>
.operation-facts {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 230px), 1fr));
  gap: 14px;
  margin: 0;
}
.operation-facts > div {
  min-width: 0;
}
.operation-facts dt {
  color: var(--iris-color-text-secondary, #64748b);
  font-size: 0.875rem;
}
.operation-facts dd {
  margin: 4px 0 0;
  overflow-wrap: anywhere;
}
</style>
<style scoped>
.operation-result-summary,
.operation-result-summary > section {
  display: grid;
  gap: 14px;
  min-width: 0;
  overflow-wrap: anywhere;
}
h2,
h3,
p {
  margin: 0;
}
.operation-step-list {
  list-style: none;
  padding: 0;
  margin: 0;
  display: grid;
  gap: 14px;
}
</style>
