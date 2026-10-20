import { createApp, nextTick, ref } from "vue";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useTaskPolling, type TaskPollingOptions } from "./useTaskPolling";

function mountPolling(
  refresh: () => Promise<void>,
  options: TaskPollingOptions = {},
) {
  const status = ref("pending");
  let polling!: ReturnType<typeof useTaskPolling>;
  const app = createApp({
    setup() {
      polling = useTaskPolling(() => status.value, refresh, options);
      return () => null;
    },
  });
  app.mount(document.createElement("div"));
  return { app, status, polling };
}

function visible(): void {
  vi.useFakeTimers();
  vi.spyOn(document, "hidden", "get").mockReturnValue(false);
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe("useTaskPolling", () => {
  it("stops at unknown/terminal states and resumes only pending states", async () => {
    visible();
    const refresh = vi.fn(async () => {});
    const { app, status } = mountPolling(refresh);
    try {
      await vi.advanceTimersByTimeAsync(2000);
      expect(refresh).toHaveBeenCalledTimes(1);
      status.value = "unknown";
      await nextTick();
      await vi.advanceTimersByTimeAsync(60_000);
      expect(refresh).toHaveBeenCalledTimes(1);
      status.value = "running";
      await nextTick();
      await vi.advanceTimersByTimeAsync(2000);
      expect(refresh).toHaveBeenCalledTimes(2);
      status.value = "completed";
      await nextTick();
      await vi.advanceTimersByTimeAsync(60_000);
      expect(refresh).toHaveBeenCalledTimes(2);
    } finally {
      app.unmount();
    }
  });

  it("enforces a finite request budget and cancels timers on unmount", async () => {
    visible();
    const refresh = vi.fn(async () => {});
    const { app, polling } = mountPolling(refresh, {
      maxRequests: 2,
      intervalMs: 1000,
    });
    await vi.advanceTimersByTimeAsync(60_000);
    expect(refresh).toHaveBeenCalledTimes(2);
    expect(polling.exhausted).toBe(true);
    app.unmount();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("does not overlap slow reads or poll while a tab is hidden", async () => {
    visible();
    let finish!: () => void;
    const refresh = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          finish = resolve;
        }),
    );
    const { app } = mountPolling(refresh);
    try {
      await vi.advanceTimersByTimeAsync(2000);
      await vi.advanceTimersByTimeAsync(30_000);
      expect(refresh).toHaveBeenCalledTimes(1);
      vi.spyOn(document, "hidden", "get").mockReturnValue(true);
      document.dispatchEvent(new Event("visibilitychange"));
      finish();
      await Promise.resolve();
      await vi.advanceTimersByTimeAsync(30_000);
      expect(refresh).toHaveBeenCalledTimes(1);
      vi.spyOn(document, "hidden", "get").mockReturnValue(false);
      document.dispatchEvent(new Event("visibilitychange"));
      await vi.advanceTimersByTimeAsync(2000);
      expect(refresh).toHaveBeenCalledTimes(2);
      finish();
    } finally {
      app.unmount();
    }
  });

  it("pauses a queued poll for foreground reads without spending or resetting its request budget", async () => {
    visible();
    const blocked = ref(false);
    const refresh = vi.fn(async () => {});
    const { app, polling } = mountPolling(refresh, {
      intervalMs: 1000,
      maxRequests: 2,
      blocked: () => blocked.value,
    });
    try {
      await vi.advanceTimersByTimeAsync(500);
      blocked.value = true;
      await nextTick();
      await vi.advanceTimersByTimeAsync(5000);
      expect(refresh).not.toHaveBeenCalled();
      expect(polling.requests).toBe(0);
      for (let count = 1; count <= 2; count++) {
        blocked.value = false;
        await nextTick();
        await vi.advanceTimersByTimeAsync(999);
        expect(refresh).toHaveBeenCalledTimes(count - 1);
        await vi.advanceTimersByTimeAsync(1);
        expect(refresh).toHaveBeenCalledTimes(count);
        blocked.value = true;
        await nextTick();
        await vi.advanceTimersByTimeAsync(5000);
      }
      expect(polling.requests).toBe(2);
      expect(polling.exhausted).toBe(true);
      blocked.value = false;
      await nextTick();
      await vi.advanceTimersByTimeAsync(60_000);
      expect(refresh).toHaveBeenCalledTimes(2);
    } finally {
      app.unmount();
    }
  });

  it("keeps the original wall-clock budget when a foreground read unblocks", async () => {
    visible();
    const blocked = ref(true);
    const refresh = vi.fn(async () => {});
    const { app, polling } = mountPolling(refresh, {
      intervalMs: 1000,
      maxDurationMs: 5000,
      blocked: () => blocked.value,
    });
    try {
      await vi.advanceTimersByTimeAsync(6000);
      expect(refresh).not.toHaveBeenCalled();
      blocked.value = false;
      await nextTick();
      expect(polling.exhausted).toBe(true);
      await vi.advanceTimersByTimeAsync(60_000);
      expect(refresh).not.toHaveBeenCalled();
      expect(polling.requests).toBe(0);
    } finally {
      app.unmount();
    }
  });

  it("checks the wall-clock budget before starting a delayed read", async () => {
    visible();
    const refresh = vi.fn(async () => {});
    const { app, polling } = mountPolling(refresh, {
      intervalMs: 1000,
      maxDurationMs: 1000,
    });
    try {
      await vi.advanceTimersByTimeAsync(1000);
      expect(refresh).not.toHaveBeenCalled();
      expect(polling.exhausted).toBe(true);
    } finally {
      app.unmount();
    }
  });
});
