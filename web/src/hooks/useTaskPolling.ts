import { onBeforeUnmount, onMounted, reactive, watch } from "vue";

const activeStatuses = new Set([
  "pending",
  "requested",
  "running",
  "waiting",
  "compensating",
]);

export interface TaskPollingOptions {
  intervalMs?: number;
  maxIntervalMs?: number;
  maxDurationMs?: number;
  maxRequests?: number;
  blocked?(): boolean;
}

export interface TaskPolling {
  readonly requests: number;
  readonly exhausted: boolean;
}

// The supplied callback must be a metadata GET, never a command/reconcile or a
// one-time download. Await each read: slow requests must not overlap/cancel each
// other before the transport deadline. Unknown outcomes require explicit action.
export function useTaskPolling(
  status: () => unknown,
  refresh: () => Promise<void>,
  options: TaskPollingOptions = {},
): TaskPolling {
  const interval = Math.max(1000, options.intervalMs ?? 2000);
  const maxInterval = Math.max(interval, options.maxIntervalMs ?? 15_000);
  const maxDuration = Math.max(interval, options.maxDurationMs ?? 5 * 60_000);
  const maxRequests = Math.max(1, options.maxRequests ?? 60);
  const state = reactive({ requests: 0, exhausted: false });
  let mounted = false;
  let busy = false;
  let startedAt = 0;
  let timer: number | undefined;

  const clear = (): void => {
    if (timer !== undefined) window.clearTimeout(timer);
    timer = undefined;
  };
  const schedule = (): void => {
    clear();
    if (
      !mounted ||
      busy ||
      document.hidden ||
      options.blocked?.() ||
      state.exhausted
    )
      return;
    if (typeof status() !== "string" || !activeStatuses.has(String(status())))
      return;
    if (
      state.requests >= maxRequests ||
      Date.now() - startedAt >= maxDuration
    ) {
      state.exhausted = true;
      return;
    }
    const delay = Math.min(
      maxInterval,
      interval * 2 ** Math.floor(state.requests / 5),
    );
    timer = window.setTimeout(() => void tick(), delay);
  };
  const tick = async (): Promise<void> => {
    timer = undefined;
    if (!mounted || document.hidden || options.blocked?.() || state.exhausted)
      return;
    if (!activeStatuses.has(String(status()))) return;
    if (
      state.requests >= maxRequests ||
      Date.now() - startedAt >= maxDuration
    ) {
      state.exhausted = true;
      return;
    }
    busy = true;
    state.requests += 1;
    try {
      await refresh();
    } catch {
      // An unexpected callback rejection stops the loop; the resource owns
      // the visible request error. Never create an unbounded retry loop.
      state.exhausted = true;
    } finally {
      busy = false;
      schedule();
    }
  };

  watch(() => [status(), options.blocked?.()], schedule);
  onMounted(() => {
    mounted = true;
    startedAt = Date.now();
    document.addEventListener("visibilitychange", schedule);
    schedule();
  });
  onBeforeUnmount(() => {
    mounted = false;
    clear();
    document.removeEventListener("visibilitychange", schedule);
  });
  return {
    get requests() {
      return state.requests;
    },
    get exhausted() {
      return state.exhausted;
    },
  };
}
