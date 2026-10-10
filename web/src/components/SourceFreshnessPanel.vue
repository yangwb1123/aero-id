<script setup lang="ts">
import { computed } from "vue";
import { IrisAlert, IrisCard } from "@iris-ui-kit/vue";
import type { CapabilityReport } from "../types";
const props = defineProps<{ report: CapabilityReport }>();
const counts = computed(() => ({
  registered: props.report.datasets.filter((item) => item.registered).length,
  enabled: props.report.datasets.filter((item) => item.enabled).length,
  authorized: props.report.datasets.filter(
    (item) => item.authorization === "allowed",
  ).length,
  fresh: props.report.datasets.filter((item) => item.fresh).length,
}));
</script>

<template>
  <IrisCard variant="outline" header="来源能力与本地新鲜度">
    <p>
      已注册 {{ counts.registered }} · 已启用 {{ counts.enabled }} ·
      本地读取资格 {{ counts.authorized }} · 新鲜 {{ counts.fresh }}
    </p>
    <IrisAlert tone="info" title="资格不等于来源 ACL">
      不探测远端健康、不推断资源权限；来源调用仍重新鉴权。Writer Epoch
      不是投影因果水位。
    </IrisAlert>
    <div class="record-table-wrap">
      <table class="record-table">
        <thead>
          <tr>
            <th>数据集</th>
            <th>启用</th>
            <th>投影状态</th>
            <th>版本</th>
            <th>最近同步</th>
            <th>原因</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="item in report.datasets" :key="item.name">
            <td>{{ item.name }}{{ item.pii ? " · PII" : "" }}</td>
            <td>{{ item.enabled ? "已启用" : "未配置" }}</td>
            <td>{{ item.status }}{{ item.fresh ? " · fresh" : "" }}</td>
            <td>{{ item.version }}</td>
            <td>{{ item.last_synced_at ?? "未同步" }}</td>
            <td>{{ item.reason_code ?? "—" }}</td>
          </tr>
        </tbody>
      </table>
    </div>
    <p>
      生成于 {{ report.generated_at }} · 账户主区域 {{ report.home_region }} ·
      当前区域 {{ report.local_region }}
    </p>
  </IrisCard>
</template>
