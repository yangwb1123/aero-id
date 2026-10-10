import { createApp, nextTick } from "vue";
import { describe, expect, it } from "vitest";
import { useAsyncResource } from "./useAsyncResource";
import type { AsyncResource } from "./useAsyncResource";

describe("useAsyncResource", () => {
  it("exposes read-only state with explicit replacement", async () => {
    const directWrite = (resource: AsyncResource<string>): void => {
      // @ts-expect-error State is read-only; callers must use replace().
      resource.data = "unsupported";
      // @ts-expect-error State is read-only.
      resource.error = new Error("unsupported");
      // @ts-expect-error State is read-only.
      resource.loading = false;
    };
    expect(directWrite).toBeTypeOf("function");

    let resource!: AsyncResource<string>;
    const app = createApp({
      setup() {
        resource = useAsyncResource(async () => "loaded");
        return () => null;
      },
    });
    app.mount(document.createElement("div"));

    try {
      await nextTick();
      await Promise.resolve();
      expect(resource.data).toBe("loaded");
      for (const key of ["data", "error", "loading"] as const) {
        expect(
          Object.getOwnPropertyDescriptor(resource, key)?.set,
        ).toBeUndefined();
      }
      resource.replace("replaced");
      expect(resource.data).toBe("replaced");
    } finally {
      app.unmount();
    }
  });

  it("aborts superseded reads and preserves a replacement against stale completions", async () => {
    const signals: AbortSignal[] = [];
    const resolves: Array<(value: string) => void> = [];
    let resource!: AsyncResource<string>;
    const app = createApp({
      setup() {
        resource = useAsyncResource((signal) => {
          signals.push(signal);
          return new Promise<string>((resolve) => resolves.push(resolve));
        });
        return () => null;
      },
    });
    app.mount(document.createElement("div"));
    let unmounted = false;
    try {
      resource.reload();
      await nextTick();
      expect(signals).toHaveLength(2);
      expect(signals[0].aborted).toBe(true);
      resource.replace("saved");
      expect(signals[1].aborted).toBe(true);
      resolves[0]("old-query");
      resolves[1]("later-query");
      await Promise.resolve();
      expect(resource.data).toBe("saved");
      const refresh = resource.refresh();
      expect(resource.loading).toBe(false);
      expect(resource.data).toBe("saved");
      app.unmount();
      unmounted = true;
      expect(signals[2].aborted).toBe(true);
      resolves[2]("after-unmount");
      await refresh;
      expect(resource.data).toBe("saved");
    } finally {
      if (!unmounted) app.unmount();
    }
  });
});
