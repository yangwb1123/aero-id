<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from "vue";
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
const resource = useAsyncResource((signal) => client.getProfile(signal));
const draft = reactive<Partial<Profile>>({});
const saving = ref(false);
const saved = ref(false);
const saveError = ref<Error>();
let active = true;
onBeforeUnmount(() => {
  active = false;
});
let submitted: Partial<Profile> | undefined;
const hasUnsavedChanges = computed(() =>
  fields.some(([key]) => (draft[key] ?? "") !== (resource.data?.[key] ?? "")),
);
watch(
  () => resource.data,
  (profile) => {
    if (!profile) return;
    for (const [key] of fields) {
      if (
        key in profile &&
        (!submitted || (draft[key] ?? "") === submitted[key])
      )
        draft[key] = profile[key];
    }
  },
  // Apply the server result before finally releases its submitted snapshot.
  { immediate: true, flush: "sync" },
);

async function save(): Promise<void> {
  if (!active || saving.value) return;
  saving.value = true;
  saved.value = false;
  saveError.value = undefined;
  try {
    submitted = {
      display_name: draft.display_name ?? "",
      avatar_url: draft.avatar_url ?? "",
      locale: draft.locale ?? "",
      timezone: draft.timezone ?? "",
    };
    const updated = await client.updateProfile(submitted);
    resource.replace(updated);
    saved.value = true;
  } catch (reason) {
    saveError.value = reason instanceof Error ? reason : new Error("保存失败");
  } finally {
    submitted = undefined;
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
    <IrisAlert v-if="saved" :tone="hasUnsavedChanges ? 'info' : 'success'">
      {{
        hasUnsavedChanges
          ? "上次提交已保存，仍有未保存的修改。"
          : "资料已保存。"
      }}
    </IrisAlert>
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
