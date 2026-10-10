import { onBeforeUnmount, onMounted, ref, shallowReactive, watch } from "vue";

export interface AsyncResource<T> {
  readonly data: T | undefined;
  readonly error: Error | undefined;
  readonly loading: boolean;
  reload(): void;
  refresh(): Promise<void>;
  replace(data: T): void;
}

export function useAsyncResource<T>(
  load: (signal: AbortSignal) => Promise<T>,
): AsyncResource<T> {
  const state = shallowReactive({
    data: undefined as T | undefined,
    error: undefined as Error | undefined,
    loading: true,
  });
  const revision = ref(0);
  let active = true;
  let generation = 0;
  let request: AbortController | undefined;

  const run = async (background = false): Promise<void> => {
    if (!active) return;
    request?.abort();
    const controller = new AbortController();
    request = controller;
    const current = ++generation;
    state.loading = !background || state.data === undefined;
    state.error = undefined;
    try {
      const value = await load(controller.signal);
      if (!active || current !== generation) return;
      state.data = value;
    } catch (reason) {
      if (!active || current !== generation) return;
      state.error = reason instanceof Error ? reason : new Error("请求失败");
    } finally {
      if (active && current === generation) {
        state.loading = false;
        request = undefined;
      }
    }
  };

  onMounted(() => void run());
  watch(revision, () => void run());
  onBeforeUnmount(() => {
    active = false;
    generation += 1;
    request?.abort();
    request = undefined;
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
    refresh: () => run(true),
    replace: (value) => {
      generation += 1;
      request?.abort();
      request = undefined;
      state.data = value;
      state.error = undefined;
      state.loading = false;
    },
  };
}
