<script setup lang="ts">
import { IrisCheckbox } from "@iris-ui-kit/vue";
import type { Dataset } from "../types";
const props = defineProps<{
  datasets: Dataset[];
  selected: string[];
  disabled?: boolean;
}>();
const emit = defineEmits<{ change: [datasets: string[]] }>();
function select(name: string, enabled: boolean): void {
  emit(
    "change",
    enabled
      ? [...new Set([...props.selected, name])]
      : props.selected.filter((value) => value !== name),
  );
}
</script>
<template>
  <fieldset class="dataset-selection" :disabled="disabled">
    <legend>显式选择数据集（{{ selected.length }} 项）</legend>
    <p>
      不隐含“全部来源”。导出仅含核心资料和所选本地投影引用，不含 Vault
      字节或完整 IM 消息。擦除会影响本地账户内容；源授权/启用、Legal Hold
      和跨区域删除证明不能从选项推断。
    </p>
    <IrisCheckbox
      v-for="dataset in datasets"
      :key="dataset.name"
      :disabled="disabled"
      :model-value="selected.includes(dataset.name)"
      @update:model-value="select(dataset.name, $event)"
    >
      {{ dataset.name }} · {{ dataset.source }}{{ dataset.pii ? " · PII" : "" }}
    </IrisCheckbox>
  </fieldset>
</template>
