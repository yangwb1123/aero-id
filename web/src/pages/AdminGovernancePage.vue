<script setup lang="ts">
import { IrisAlert } from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import AdminAuditLogPanel from "../components/AdminAuditLogPanel.vue";
import AdminBanPanel from "../components/AdminBanPanel.vue";
import AdminRolePanel from "../components/AdminRolePanel.vue";
import PageHeader from "../components/PageHeader.vue";

const { client } = defineProps<{ client: AeroIdClient }>();
</script>

<template>
  <section>
    <PageHeader
      title="管理员治理"
      description="对应 aero-id OpenAPI 的 /admin/audit-log、/admin/roles* 和 /admin/bans*；权限仍由服务端逐请求校验。"
    />
    <IrisAlert tone="info" title="管理边界">
      本页只调用 aero-id。页面不缓存凭据、不把 token 转发到 Snaplink、Aero
      IM、Aero Vault 或 Audit Governance；权限不足和服务不可用会明确显示。
    </IrisAlert>
    <AdminAuditLogPanel :client="client" />
    <AdminRolePanel :client="client" />
    <AdminBanPanel :client="client" />
  </section>
</template>
