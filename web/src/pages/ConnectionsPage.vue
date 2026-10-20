<script setup lang="ts">
import { computed } from "vue";
import { IrisAlert } from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import { capabilityAvailable } from "../api/capabilities";
import AccountProjectionContext from "../components/AccountProjectionContext.vue";
import ConnectionReferenceList from "../components/ConnectionReferenceList.vue";
import PageHeader from "../components/PageHeader.vue";
import { useCapabilities } from "../hooks/capabilityContext";
defineProps<{ client: AeroIdClient }>();
const capabilities = useCapabilities();
const report = computed(() => capabilities?.());
const canRead = computed(() =>
  capabilityAvailable(report.value, ["account.self.read"]),
);
const account = computed(() => ({
  account_id: report.value?.account_id,
  tenant_id: report.value?.tenant_id,
  home_region: report.value?.home_region,
}));
</script>

<template>
  <section>
    <PageHeader
      title="来源与成员关系"
      description="先选择一条已保存引用，查看它原本的来源账户、成员和作用域；不调用来源管理接口。"
    >
      <template #actions
        ><a href="#/overview">返回账户概览</a
        ><a href="#/resources">查看当前资源摘要</a
        ><a href="#/jobs">选择范围同步 / 导出</a></template
      >
    </PageHeader>
    <AccountProjectionContext :account="account" />
    <IrisAlert tone="warning" title="不同来源的作用域不能互换">
      当前会话租户不等于来源租户、IM 工作空间或 Vault 桶；相同 ID
      文本也不代表同一范围。成员关系与 owner 不是全局权限，不提供来源管理授权。
    </IrisAlert>
    <p class="reference-boundary">
      两个列表独立读取本地引用，未提供 coverage/complete
      或投影到期预算，不能确认来源全量、完整性或当前新鲜度。刷新只 GET
      原列表，不创建同步、导出或其他命令；需要更新时再显式选择范围并核对原预览。
    </p>
    <ConnectionReferenceList
      :client="client"
      kind="sources"
      :can-read="canRead"
    />
    <ConnectionReferenceList
      :client="client"
      kind="memberships"
      :can-read="canRead"
    />
  </section>
</template>

<style scoped>
.reference-boundary {
  line-height: 1.7;
  overflow-wrap: anywhere;
}
</style>
