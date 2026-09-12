<script setup lang="ts">
import {
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisSelect,
  IrisTextarea,
} from "@iris-ui-kit/vue";
import type { ReplayCommand } from "../api/replay";
import {
  replayActions,
  replaySources,
  type ReplayCommandDraft,
} from "./replayHelpers";
const props = defineProps<{ commands: ReplayCommandDraft[] }>();
const emit = defineEmits<{ change: [commands: ReplayCommandDraft[]] }>();
function update(id: string, patch: Partial<ReplayCommandDraft>): void {
  emit(
    "change",
    props.commands.map((command) =>
      command.id === id ? { ...command, ...patch } : command,
    ),
  );
}
function remove(id: string): void {
  emit(
    "change",
    props.commands.filter((command) => command.id !== id),
  );
}
function add(): void {
  emit("change", [
    ...props.commands,
    {
      id: crypto.randomUUID(),
      source: "aero-im",
      action: "summary",
      payload: "{}",
    },
  ]);
}
</script>

<template>
  <div class="replay-command-list">
    <IrisCard
      v-for="(command, index) in commands"
      :key="command.id"
      variant="outline"
    >
      <template #header>命令 {{ index + 1 }}</template>
      <template #footer
        ><div class="detail-actions">
          <IrisButton size="sm" variant="ghost" @click="remove(command.id)"
            >移除</IrisButton
          >
        </div></template
      >
      <div class="replay-command-grid">
        <IrisFormField label="来源">
          <IrisSelect
            :model-value="command.source"
            :items="replaySources"
            @update:model-value="
              update(command.id, {
                source: String($event) as ReplayCommand['source'],
              })
            "
          />
        </IrisFormField>
        <IrisFormField label="Allow-list 动作">
          <IrisSelect
            :model-value="command.action"
            :items="replayActions"
            @update:model-value="
              update(command.id, {
                action: String($event) as ReplayCommand['action'],
              })
            "
          />
        </IrisFormField>
        <IrisFormField label="Payload JSON">
          <IrisTextarea
            :model-value="command.payload"
            autosize
            :max-rows="8"
            @update:model-value="update(command.id, { payload: $event })"
          />
        </IrisFormField>
      </div>
    </IrisCard>
    <IrisButton variant="outline" :disabled="commands.length >= 32" @click="add"
      >添加命令（{{ commands.length }}/32）</IrisButton
    >
  </div>
</template>
