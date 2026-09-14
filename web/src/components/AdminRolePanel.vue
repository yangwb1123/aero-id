<script setup lang="ts">
import { computed, ref } from "vue";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisDialog,
  IrisDialogContent,
  IrisDialogDescription,
  IrisDialogTitle,
  IrisFormField,
  IrisInput,
  IrisSelect,
} from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import PageError from "./PageError.vue";
import PageLoading from "./PageLoading.vue";
import RecordTable from "./RecordTable.vue";

const props = defineProps<{ client: AeroIdClient }>();
const roles = ref<Record<string, unknown>[]>([]);
const userRoles = ref<Record<string, unknown>[]>([]);
const uid = ref("");
const roleCode = ref("");
const loading = ref(true);
const userLoading = ref(false);
const error = ref<Error>();
const userError = ref<Error>();
const pending = ref<{
  action: "grant" | "revoke";
  uid: string;
  role: string;
  idempotencyKey: string;
}>();
const confirmation = ref("");
const running = ref(false);
const roleItems = computed(() =>
  roles.value.flatMap((role) => {
    const code = role.code ?? role.Code ?? role.role_code ?? role.RoleCode;
    if (typeof code !== "string" || !code) return [];
    const name = role.name ?? role.Name;
    return [
      {
        value: code,
        label: typeof name === "string" ? `${code} · ${name}` : code,
      },
    ];
  }),
);
const confirmationPhrase = computed(() =>
  pending.value?.action === "grant" ? "CONFIRM GRANT" : "CONFIRM REVOKE",
);
const confirmed = computed(
  () => confirmation.value === confirmationPhrase.value,
);
async function loadRoles(): Promise<void> {
  loading.value = true;
  error.value = undefined;
  try {
    roles.value = (await props.client.listRoles()) as Record<string, unknown>[];
  } catch (reason) {
    error.value =
      reason instanceof Error ? reason : new Error("角色目录读取失败");
  } finally {
    loading.value = false;
  }
}
async function loadUserRoles(): Promise<void> {
  const target = uid.value.trim();
  if (!target) {
    userError.value = new Error("UID 不能为空");
    return;
  }
  userLoading.value = true;
  userError.value = undefined;
  try {
    userRoles.value = (await props.client.listUserRoles(target)) as Record<
      string,
      unknown
    >[];
  } catch (reason) {
    userError.value =
      reason instanceof Error ? reason : new Error("用户角色读取失败");
  } finally {
    userLoading.value = false;
  }
}
function prepare(action: "grant" | "revoke"): void {
  const target = uid.value.trim();
  const role = roleCode.value.trim();
  if (!target || !role) {
    userError.value = new Error("UID 和角色代码不能为空");
    return;
  }
  pending.value = {
    action,
    uid: target,
    role,
    idempotencyKey: crypto.randomUUID(),
  };
  confirmation.value = "";
}
async function confirm(): Promise<void> {
  if (!pending.value || !confirmed.value) return;
  running.value = true;
  userError.value = undefined;
  try {
    if (pending.value.action === "grant") {
      await props.client.grantRole(
        pending.value.uid,
        pending.value.role,
        pending.value.idempotencyKey,
      );
    } else {
      await props.client.revokeRole(
        pending.value.uid,
        pending.value.role,
        pending.value.idempotencyKey,
      );
    }
    await loadUserRoles();
    pending.value = undefined;
  } catch (reason) {
    userError.value =
      reason instanceof Error ? reason : new Error("角色变更失败");
  } finally {
    running.value = false;
  }
}
function cancel(): void {
  if (!running.value) pending.value = undefined;
}
void loadRoles();
</script>

<template>
  <IrisCard variant="outline">
    <template #header>角色目录与授权</template>
    <IrisAlert tone="info">
      角色和权限由
      Snaplink/管理员策略控制。每次变更使用独立幂等键并二次确认；网络超时时保持同一意图和幂等键重试，不要重新生成命令。
    </IrisAlert>
    <PageLoading v-if="loading" />
    <PageError v-else-if="error" :error="error" :retry="loadRoles" />
    <RecordTable
      v-else
      :records="roles"
      :columns="[
        { key: 'Code', label: 'Role Code', aliases: ['code', 'role_code'] },
        { key: 'Name', label: '名称', aliases: ['name'] },
        { key: 'Permissions', label: 'Permissions', aliases: ['permissions'] },
        {
          key: 'CreatedAt',
          label: '创建时间',
          aliases: ['created_at'],
          kind: 'date',
        },
      ]"
      empty="角色目录为空"
    />
    <div class="admin-role-grid">
      <IrisFormField label="目标 UID" required
        ><IrisInput v-model="uid" placeholder="Snaplink UID"
      /></IrisFormField>
      <IrisFormField label="角色代码" required>
        <IrisSelect
          v-if="roleItems.length"
          :model-value="roleCode"
          :items="roleItems"
          @update:model-value="roleCode = String($event)"
        />
        <IrisInput v-else v-model="roleCode" placeholder="role_code" />
      </IrisFormField>
      <div class="admin-filter-actions">
        <IrisButton
          type="button"
          variant="outline"
          :loading="userLoading"
          @click="void loadUserRoles()"
          >读取用户角色</IrisButton
        >
        <IrisButton type="button" @click="prepare('grant')"
          >授予角色</IrisButton
        >
        <IrisButton type="button" variant="danger" @click="prepare('revoke')"
          >撤销角色</IrisButton
        >
      </div>
    </div>
    <IrisAlert v-if="userError" tone="danger">{{
      userError.message
    }}</IrisAlert>
    <RecordTable
      v-if="userRoles.length"
      :records="userRoles"
      :columns="[
        { key: 'UID', label: 'UID', aliases: ['uid'] },
        { key: 'RoleCode', label: 'Role Code', aliases: ['role_code', 'code'] },
        { key: 'GrantedBy', label: 'Granted By', aliases: ['granted_by'] },
        {
          key: 'CreatedAt',
          label: '时间',
          aliases: ['created_at'],
          kind: 'date',
        },
      ]"
      empty="该用户没有角色"
    />
  </IrisCard>
  <IrisDialog
    :open="Boolean(pending)"
    @update:open="(open) => !open && cancel()"
  >
    <IrisDialogContent>
      <IrisDialogTitle
        >确认{{
          pending?.action === "grant" ? "授予" : "撤销"
        }}角色</IrisDialogTitle
      >
      <IrisDialogDescription
        >目标 UID：{{ pending?.uid }}；角色：{{
          pending?.role
        }}。这是管理员权限变更，请完成二次确认。</IrisDialogDescription
      >
      <IrisFormField label="确认短语" :hint="confirmationPhrase"
        ><IrisInput v-model="confirmation" autocomplete="off"
      /></IrisFormField>
      <IrisAlert v-if="userError" tone="danger">{{
        userError.message
      }}</IrisAlert>
      <div class="dialog-actions">
        <IrisButton variant="ghost" :disabled="running" @click="cancel"
          >取消</IrisButton
        >
        <IrisButton
          :variant="pending?.action === 'revoke' ? 'danger' : 'solid'"
          :loading="running"
          :disabled="!confirmed"
          @click="void confirm()"
          >确认</IrisButton
        >
      </div>
    </IrisDialogContent>
  </IrisDialog>
</template>
