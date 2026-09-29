<script setup lang="ts">
import { reactive, ref, watch } from "vue";
import { IrisAlert, IrisButton, IrisCard, IrisInput } from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import AccountEraseAction from "../components/AccountEraseAction.vue";
import PageError from "../components/PageError.vue";
import PageHeader from "../components/PageHeader.vue";
import PageLoading from "../components/PageLoading.vue";
import { useAsyncResource } from "../hooks/useAsyncResource";
import type { Profile } from "../types";

const fields = [
  ["display_name", "显示名称"],
  ["avatar_url", "头像 URL"],
  ["locale", "语言"],
  ["timezone", "时区"],
] as const;
const { client } = defineProps<{ client: AeroIdClient }>();
const resource = useAsyncResource(() => client.getProfile());
const draft = reactive<Partial<Profile>>({});
const saving = ref(false);
const saved = ref(false);
const saveError = ref<Error>();
watch(
  () => resource.data,
  (profile) => {
    if (profile) Object.assign(draft, profile);
  },
  { immediate: true },
);

async function save(): Promise<void> {
  saving.value = true;
  saved.value = false;
  saveError.value = undefined;
  try {
    const updated = await client.updateProfile({
      display_name: draft.display_name ?? "",
      avatar_url: draft.avatar_url ?? "",
      locale: draft.locale ?? "",
      timezone: draft.timezone ?? "",
    });
    resource.replace(updated);
    saved.value = true;
  } catch (reason) {
    saveError.value = reason instanceof Error ? reason : new Error("保存失败");
  } finally {
    saving.value = false;
  }
}
</script>

<template>
  <PageLoading v-if="resource.loading" />
  <PageError
    v-else-if="resource.error"
    :error="resource.error"
    :retry="resource.reload"
  />
  <section v-else>
    <PageHeader
      title="个人资料"
      description="这里只修改 aero-id 明确拥有的展示和偏好字段。"
    />
    <IrisAlert tone="info" title="身份字段由 Snaplink 管理">
      邮箱、电话、验证状态、MFA 和账户生命周期不能在这里修改。
    </IrisAlert>
    <IrisAlert v-if="saveError" tone="danger">{{
      saveError.message
    }}</IrisAlert>
    <IrisAlert v-if="saved" tone="success">资料已保存。</IrisAlert>
    <IrisCard variant="outline">
      <form class="profile-form" @submit.prevent="void save()">
        <label v-for="[key, label] in fields" :key="key" class="form-row">
          <span>{{ label }}</span>
          <IrisInput v-model="draft[key]" />
        </label>
        <div class="form-actions">
          <IrisButton type="submit" :loading="saving">保存资料</IrisButton>
        </div>
      </form>
    </IrisCard>
    <AccountEraseAction
      :client="client"
      :account-id="resource.data?.account_id"
    />
  </section>
</template>
