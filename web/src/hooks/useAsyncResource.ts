import { onBeforeUnmount, onMounted, ref, shallowReactive, watch } from "vue";

export interface AsyncResource<T> {
  readonly data: T | undefined;
  readonly error: Error | undefined;
  readonly loading: boolean;
  reload(): void;
  replace(data: T): void;
}

export function useAsyncResource<T>(load: () => Promise<T>): AsyncResource<T> {
  const state = shallowReactive({
    data: undefined as T | undefined,
    error: undefined as Error | undefined,
    loading: true,
  });
  const revision = ref(0);
  let active = true;
  let generation = 0;

  const run = async (): Promise<void> => {
    const current = ++generation;
    state.loading = true;
    state.error = undefined;
    try {
      const value = await load();
      if (!active || current !== generation) return;
      state.data = value;
    } catch (reason) {
      if (!active || current !== generation) return;
      state.error = reason instanceof Error ? reason : new Error("请求失败");
    } finally {
      if (active && current === generation) state.loading = false;
    }
  };

  onMounted(() => void run());
  watch(revision, () => void run());
  onBeforeUnmount(() => {
    active = false;
    generation += 1;
  });

  return {
    get data() {
      return state.data;
    },
    get error() {
      return state.error;
    },
    get loading() {
      return state.loading;
    },
    reload: () => {
      revision.value += 1;
    },
    replace: (value) => {
      state.data = value;
      state.error = undefined;
      state.loading = false;
    },
  };
}
