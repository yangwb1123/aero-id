<script setup lang="ts">
import { IrisAlert, IrisButton, IrisCard } from "@iris-ui-kit/vue";
import type { PlatformConfig } from "../config";
import PageHeader from "../components/PageHeader.vue";

const props = defineProps<{ config: PlatformConfig }>();
const services = [
  [
    "审计治理",
    "全平台审计查询、完整性、导出、保留与受控重放。",
    "auditConsoleUrl",
  ],
  [
    "消息与通知",
    "Aero IM 的会话、通知、工作区和资源权限。",
    "aeroImConsoleUrl",
  ],
  [
    "文件与对象",
    "Aero Vault 的 Bucket、对象、共享和对象 ACL。",
    "aeroVaultConsoleUrl",
  ],
] as const;
</script>

<template>
  <section>
    <PageHeader
      title="平台服务"
      description="同一 Snaplink 身份下的领域控制台入口。"
    />
    <IrisAlert tone="info" title="使用独立 audience">
      跳转后的服务必须为自己的 audience 获取令牌；本控制台不会把 aero-id access
      token 转发给其他服务。
    </IrisAlert>
    <div class="service-grid">
      <IrisCard
        v-for="[title, description, key] in services"
        :key="key"
        variant="outline"
      >
        <template #header>{{ title }}</template>
        <span>{{ description }}</span>
        <template #footer>
          <IrisButton v-if="props.config[key]" variant="outline" as-child>
            <a :href="props.config[key]">打开控制台</a>
          </IrisButton>
          <span v-else class="muted">尚未配置入口 URL</span>
        </template>
      </IrisCard>
    </div>
  </section>
</template>
