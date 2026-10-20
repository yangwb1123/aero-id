<script setup lang="ts">
import { computed } from "vue";
import { IrisAlert } from "@iris-ui-kit/vue";
import TaskAuditNavigation from "./TaskAuditNavigation.vue";
import type { JsonRecord } from "../types";
import { jobOutcome, jobSourceResults } from "./jobResultSummary";
import { jobDownloadState, jobExportMetadata } from "./jobExportSummary";
import {
  jobCount,
  jobDatasetLabels,
  jobFlag,
  jobScope,
  jobText,
  jobTime,
} from "./jobSummaryFields";
const props = defineProps<{
  job: JsonRecord;
  now: number;
  downloadEligible: boolean;
  downloadReceived: boolean;
}>();
const outcome = computed(() => jobOutcome(props.job));
const scope = computed(() => jobScope(props.job));
const sources = computed(() => jobSourceResults(props.job));
const isExport = computed(
  () =>
    props.job.job_type === "export" || props.job.job_type === "audit_export",
);
const metadata = computed(() => jobExportMetadata(props.job));
const result = computed(() => metadata.value.result);
const download = computed(() =>
  jobDownloadState(props.job, props.now, props.downloadReceived),
);
const missing = computed(() =>
  Array.isArray(result.value.missing_datasets)
    ? (result.value.missing_datasets as string[])
    : undefined,
);
const countFields = [
  { key: "dataset_count", label: "包含的投影条数" },
  { key: "source_count", label: "来源引用数" },
  { key: "membership_count", label: "成员关系引用数" },
];
</script>

<template>
  <section
    data-job-summary
    class="job-result-summary"
    aria-label="任务业务结果"
  >
    <h2>{{ outcome.kind.title }} · {{ outcome.status.title }}</h2>
    <p>{{ outcome.kind.detail }}</p>
    <IrisAlert :tone="outcome.status.tone" :title="outcome.status.title">
      {{ outcome.status.detail }}
    </IrisAlert>
    <dl class="job-facts">
      <div>
        <dt>目标账户</dt>
        <dd>{{ jobText(job.account_id) }}</dd>
      </div>
      <div>
        <dt>租户</dt>
        <dd>{{ jobText(job.tenant_id) }}</dd>
      </div>
      <div>
        <dt>创建时间</dt>
        <dd>{{ jobTime(job.created_at) }}</dd>
      </div>
      <div>
        <dt>最近更新时间</dt>
        <dd>{{ jobTime(job.updated_at) }}</dd>
      </div>
      <div>
        <dt>完成时间</dt>
        <dd>
          {{ jobTime(job.completed_at)
          }}<span v-if="job.completed_at == null">（不据此推断未执行）</span>
        </dd>
      </div>
      <div>
        <dt>任务尝试次数</dt>
        <dd>{{ jobCount(job.attempt) }}（不是进度）</dd>
      </div>
    </dl>

    <section v-if="job.job_type !== 'audit_export'" aria-label="原任务范围">
      <h3>原任务选择范围</h3>
      <IrisAlert v-if="scope.issue" tone="warning">{{ scope.issue }}</IrisAlert>
      <ul v-else class="job-dataset-list">
        <li v-for="name in scope.datasets" :key="name">
          {{ jobDatasetLabels[name] }} <code>{{ name }}</code>
        </li>
      </ul>
    </section>
    <p v-else>范围为上述租户的有界审计窗口，不以账户数据集选择推断审计覆盖。</p>

    <section
      v-if="['sync', 'reconcile', 'erase'].includes(String(job.job_type))"
      aria-label="逐来源结果"
    >
      <h3>逐来源结果</h3>
      <p>
        仅列原范围的来源。缺少结果不能据此判断未执行；计数不等于来源资源总数。
      </p>
      <div class="job-source-grid">
        <article
          v-for="source in sources"
          :key="source.source"
          :data-job-source="source.source"
        >
          <h4>{{ source.label }} · {{ source.title }}</h4>
          <p>{{ source.datasets.join("、") }}</p>
          <p v-if="source.missing">
            不能据此判断未执行；请刷新原任务并核对审计。
          </p>
          <p v-if="source.count !== undefined">
            摘要投影条数：{{ source.count }}
          </p>
          <p v-if="source.complete">{{ source.complete }}</p>
          <p v-if="source.drift !== undefined">
            检测到的漂移投影：{{ source.drift }}
          </p>
          <p v-if="source.repaired !== undefined">
            报告的修复投影：{{ source.repaired }}
          </p>
          <p v-if="source.outcome">{{ source.outcome }}</p>
          <p v-if="source.skipped">跳过的数据集：{{ source.skipped }}</p>
          <p v-if="source.skippedInvalid">
            来源报告的跳过范围无法安全映射，不当作完整范围。
          </p>
          <p v-if="source.operationID">
            来源操作 ID：<code>{{ source.operationID }}</code>
          </p>
          <p v-if="source.errorCode">
            结果码：<code>{{ source.errorCode }}</code>
          </p>
        </article>
      </div>
    </section>

    <section v-if="isExport" aria-label="导出结果与下载条件">
      <h3>导出结果</h3>
      <IrisAlert v-if="metadata.issue" tone="warning">{{
        metadata.issue
      }}</IrisAlert>
      <template v-else-if="job.job_type === 'export'">
        <IrisAlert
          v-if="
            result.complete === false ||
            result.coverage_complete === false ||
            missing?.length
          "
          tone="warning"
          title="导出范围不完整"
          >任务完成不代表所选投影完整；请先核对缺失范围和清单计数，再决定是否下载原制品。</IrisAlert
        >
        <dl class="job-facts">
          <div v-for="field in countFields" :key="field.key">
            <dt>{{ field.label }}</dt>
            <dd>{{ jobCount(result[field.key]) }}</dd>
          </div>
          <div>
            <dt>所选投影可用性</dt>
            <dd>
              {{
                jobFlag(
                  result.complete,
                  "所选投影可用（仍可能陈旧）",
                  "所选投影存在缺失或不可用",
                )
              }}
            </dd>
          </div>
          <div>
            <dt>数据集覆盖</dt>
            <dd>
              {{
                jobFlag(
                  result.coverage_complete,
                  "所选数据集均有投影",
                  "所选数据集覆盖不完整",
                )
              }}
            </dd>
          </div>
          <div>
            <dt>引用遍历</dt>
            <dd>
              {{
                jobFlag(
                  result.traversal_complete,
                  "引用分页遍历完成",
                  "引用分页遍历未完成",
                )
              }}
            </dd>
          </div>
        </dl>
        <div v-if="missing?.length">
          <h4>缺失的数据集</h4>
          <ul>
            <li v-for="name in missing" :key="name">
              {{ jobDatasetLabels[name] }} <code>{{ name }}</code>
            </li>
          </ul>
        </div>
        <p v-else>
          {{
            missing
              ? "本次清单未报告缺失数据集；不代表来源实时完整。"
              : "缺失的数据集：来源未提供。"
          }}
        </p>
        <p>
          不是跨源同一时点，也不证明所有投影新鲜。任务元数据未提供逐来源区域、版本与新鲜度；可查看当前资源摘要，原导出时的明细仅在原制品中。
        </p>
      </template>
      <template v-else>
        <dl class="job-facts">
          <div>
            <dt>本次窗口审计事实数</dt>
            <dd>{{ jobCount(result.event_count) }}</dd>
          </div>
        </dl>
        <IrisAlert
          v-if="result.truncated === true"
          tone="warning"
          title="本次窗口已截断"
          >还有后续窗口；本次不是完整审计历史。这里只查看原结果，不自动创建续批导出。</IrisAlert
        >
        <p v-else>
          {{
            jobFlag(result.truncated, "本次窗口已截断", "本次未报告窗口截断")
          }}；不是完整审计历史或固定全局时点证明。
        </p>
      </template>
      <dl class="job-facts">
        <div>
          <dt>清单生成时间</dt>
          <dd>{{ jobTime(result.generated_at) }}</dd>
        </div>
        <div>
          <dt>制品到期时间</dt>
          <dd>{{ jobTime(result.expires_at) }}</dd>
        </div>
      </dl>
      <IrisAlert
        :tone="download.blocked ? 'warning' : 'info'"
        :title="download.title"
        >{{ download.detail }}</IrisAlert
      >
      <p v-if="!downloadEligible">
        当前读取者权限或账户范围尚未确认，下载暂停。账户导出需要
        account.export；审计导出同时需要 audit.export 与 audit.read。
      </p>
      <p>
        下载仍由服务端复查当前读取者、原制品、原 TTL
        和账户状态。确认才请求一次性下载；消费/读取审计先于响应，丢回应可能无法重下。
      </p>
    </section>
    <nav class="detail-actions" aria-label="结果后续导航">
      <a href="#/jobs">返回同步与导出</a>
      <a href="#/resources">查看当前资源摘要</a>
      <a href="#/audit">打开审计台账</a>
    </nav>
    <TaskAuditNavigation
      page="jobs"
      :task-id="job.job_id"
      :request-id="job.request_id"
    />
    <p>
      对照审计请保留本任务 ID；请求 ID：<code>{{
        jobText(job.request_id)
      }}</code
      >。任务 ID、来源操作 ID 与审计事件 ID 不是同一种标识。
    </p>
  </section>
</template>

<style scoped>
.job-result-summary {
  display: grid;
  gap: 14px;
  min-width: 0;
  overflow-wrap: anywhere;
}
.job-result-summary h2,
.job-result-summary h3,
.job-result-summary h4,
.job-result-summary p {
  margin: 0;
}
.job-result-summary section {
  display: grid;
  gap: 12px;
  min-width: 0;
}
.job-facts {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 230px), 1fr));
  gap: 14px;
  margin: 0;
}
.job-facts dt {
  color: var(--iris-color-text-secondary, #64748b);
  font-size: 0.875rem;
}
.job-facts dd {
  margin: 4px 0 0;
}
.job-source-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 260px), 1fr));
  gap: 12px;
}
.job-source-grid article {
  border: 1px solid var(--iris-color-border, #d8dee8);
  border-radius: 8px;
  padding: 14px;
  display: grid;
  align-content: start;
  gap: 8px;
  min-width: 0;
}
.job-dataset-list {
  display: grid;
  gap: 6px;
  margin: 0;
  padding-inline-start: 22px;
}
</style>
