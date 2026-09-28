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
});
