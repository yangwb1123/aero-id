import type { Dataset } from "../types";

export function validateDatasetSelection(
  selected: readonly string[],
  datasets: readonly Dataset[],
): string[] {
  const allowed = new Set(datasets.map((dataset) => dataset.name));
  if (selected.some((name) => !allowed.has(name))) {
    throw new Error(
      "所选数据集已不在当前服务端 allow-list 中，请清除选择或重新选择。",
    );
  }
  return [...selected];
}
