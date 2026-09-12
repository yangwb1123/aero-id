<script setup lang="ts">
import { IrisBadge, IrisEmptyState } from "@iris-ui-kit/vue";
import type { JsonRecord } from "../types";
import { statusTone, valueOf, type RecordColumn } from "./table";

defineProps<{
  records: JsonRecord[];
  columns: RecordColumn[];
  empty?: string;
  actionsLabel?: string;
}>();

function keyOf(record: JsonRecord, index: number): string {
  return String(
    record.job_id ??
      record.operation_id ??
      record.source_account_id ??
      record.id ??
      record.event_id ??
      record.account_id ??
      index,
  );
}

function display(value: unknown, kind: RecordColumn["kind"]): string {
  if (value === undefined || value === null || value === "") return "—";
  if (kind === "date") {
    const date = new Date(String(value));
    return Number.isNaN(date.getTime()) ? String(value) : date.toLocaleString();
  }
  if (typeof value === "object") return JSON.stringify(value);
  return String(value);
}
</script>

<template>
  <IrisEmptyState
    v-if="records.length === 0"
    :title="empty ?? '暂无数据'"
    description="当前筛选条件没有记录。"
  />
  <div v-else class="table-scroll">
    <table class="platform-table">
      <thead>
        <tr>
          <th v-for="column in columns" :key="column.key">
            {{ column.label }}
          </th>
          <th v-if="$slots.actions">{{ actionsLabel ?? "操作" }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="(record, index) in records" :key="keyOf(record, index)">
          <td v-for="column in columns" :key="column.key">
            <IrisBadge
              v-if="
                column.kind === 'status' &&
                valueOf(record, column) !== undefined
              "
              :tone="statusTone(String(valueOf(record, column)))"
            >
              {{ display(valueOf(record, column), column.kind) }}
            </IrisBadge>
            <span
              v-else
              :class="{
                muted: display(valueOf(record, column), column.kind) === '—',
              }"
            >
              {{ display(valueOf(record, column), column.kind) }}
            </span>
          </td>
          <td v-if="$slots.actions">
            <slot name="actions" :record="record" />
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
