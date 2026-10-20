<script setup lang="ts">
import { IrisAlert, IrisButton } from "@iris-ui-kit/vue";
import type { JsonRecord } from "../types";
import type { ReferenceKind } from "./referenceSummary";
defineProps<{ reference: JsonRecord; kind: ReferenceKind }>();
defineEmits<{ close: [] }>();
</script>

<template>
  <section data-reference-details aria-label="所选原生引用范围" tabindex="-1">
    <h3>{{ kind === "sources" ? "来源账户范围" : "成员关系范围" }}</h3>
    <IrisButton type="button" variant="outline" @click="$emit('close')">{{
      kind === "sources" ? "返回来源列表" : "返回成员关系列表"
    }}</IrisButton>
    <IrisAlert tone="warning" title="已保存引用，不是当前授权">
      来源角色（包括 owner）不是全局权限，也不代表来源资源
      ACL、可管理或可下载。请在对应来源核实当前权限。
    </IrisAlert>
    <slot name="actions" />
    <dl class="reference-fields">
      <div>
        <dt>来源</dt>
        <dd>{{ reference.source_label }} · {{ reference.source }}</dd>
      </div>
      <div>
        <dt>原生作用域</dt>
        <dd>{{ reference.scope_label }}</dd>
      </div>
      <div>
        <dt>作用域类型原值</dt>
        <dd>{{ reference.scope_type }}</dd>
      </div>
      <div>
        <dt>原作用域 ID</dt>
        <dd>{{ reference.scope_id }}</dd>
      </div>
      <div>
        <dt>{{ kind === "sources" ? "来源账户 ID" : "来源成员 ID" }}</dt>
        <dd>{{ reference.native_id }}</dd>
      </div>
      <div v-if="kind === 'memberships'">
        <dt>来源角色原值</dt>
        <dd>{{ reference.role }}</dd>
      </div>
      <div>
        <dt>记录状态</dt>
        <dd>{{ reference.status_label }} · 原值 {{ reference.status }}</dd>
      </div>
      <div>
        <dt>来源区域</dt>
        <dd>{{ reference.source_region }}</dd>
      </div>
      <div>
        <dt>来源版本</dt>
        <dd>{{ reference.version }}</dd>
      </div>
      <div v-if="kind === 'sources'">
        <dt>来源生成时间</dt>
        <dd>{{ reference.generated_at }}</dd>
      </div>
      <div>
        <dt>最后同步时间</dt>
        <dd>{{ reference.last_synced_at }}</dd>
      </div>
      <div v-if="kind === 'sources'">
        <dt>来源错误码</dt>
        <dd>{{ reference.error_code }}</dd>
      </div>
    </dl>
    <p class="muted">
      版本只属于该来源记录；同步时间不是新鲜度承诺或因果水位。缺失范围不推导为当前会话租户、全局账户或全部资源。
    </p>
    <details>
      <summary>本地引用标识与读取边界</summary>
      <p>
        本地引用 ID：{{
          reference.id
        }}。这是所选列表行，不会另查来源详情；不是来源操作 ID、审计事件 ID
        或命令回执。
      </p>
      <p>只展示明确的引用字段，不展开来源 data、消息、对象内容、凭据或 ACL。</p>
    </details>
  </section>
</template>

<style scoped>
[data-reference-details] {
  margin-block: 16px;
}
[data-reference-details] > [data-iris-alert] {
  margin-block: 16px;
}
.reference-fields {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 240px), 1fr));
  gap: 16px;
}
dt {
  font-size: 13px;
  color: var(--iris-muted);
}
dd {
  margin: 4px 0 0;
  overflow-wrap: anywhere;
}
p {
  overflow-wrap: anywhere;
  line-height: 1.7;
}
summary {
  cursor: pointer;
}
</style>
