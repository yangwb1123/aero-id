// @vitest-environment node
import { build } from "vite";
import { describe, expect, it } from "vitest";

describe("production page loading boundary", () => {
  it("keeps Operation page code outside the static entry graph", async () => {
    const result = await build({
      configFile: "vite.config.ts",
      logLevel: "warn",
      build: { write: false },
    });
    if (Array.isArray(result) || !("output" in result))
      throw new Error("expected one browser build");
    const chunks = result.output.filter((item) => item.type === "chunk");
    const entry = chunks.find((item) => item.isEntry);
    const operations = chunks.find((item) =>
      Object.keys(item.modules).some((id) =>
        id.endsWith("/src/pages/OperationsPage.vue"),
      ),
    );
    expect(entry).toBeDefined();
    expect(operations).toBeDefined();
    expect(operations?.isDynamicEntry).toBe(true);
    const staticFiles = new Set<string>();
    const visit = (fileName: string): void => {
      if (staticFiles.has(fileName)) return;
      staticFiles.add(fileName);
      chunks.find((item) => item.fileName === fileName)?.imports.forEach(visit);
    };
    visit(entry!.fileName);
    expect(staticFiles.has(operations!.fileName)).toBe(false);
    expect(
      chunks.some((item) => item.dynamicImports.includes(operations!.fileName)),
    ).toBe(true);
  }, 30_000);
});
