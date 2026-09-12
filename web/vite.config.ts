import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import { existsSync, readdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const webRoot = fileURLToPath(new URL(".", import.meta.url));
const irisRoot = resolve(webRoot, "../../iris-ui");
const irisPackages = join(irisRoot, "packages");
const useLocalIris =
  process.env.IRIS_UI_SOURCE === "1" &&
  existsSync(join(irisPackages, "vue", "src", "index.ts"));

function sourceAliases(pkg: string, bare: string): Record<string, string> {
  const aliases: Record<string, string> = {};
  const sourceDir = join(irisPackages, pkg, "src");
  if (!existsSync(sourceDir)) return aliases;
  for (const entry of readdirSync(sourceDir, { withFileTypes: true })) {
    if (
      entry.isDirectory() &&
      existsSync(join(sourceDir, entry.name, "index.ts"))
    ) {
      aliases[`${bare}/${entry.name}`] = join(
        sourceDir,
        entry.name,
        "index.ts",
      );
    }
    if (
      entry.isFile() &&
      entry.name.endsWith(".ts") &&
      entry.name !== "index.ts"
    ) {
      aliases[`${bare}/${entry.name.slice(0, -3)}`] = join(
        sourceDir,
        entry.name,
      );
    }
  }
  aliases[bare] = join(sourceDir, "index.ts");
  return aliases;
}

function localIrisAliases(): Record<string, string> {
  if (!useLocalIris) return {};
  return {
    ...sourceAliases("core", "@iris-ui-kit/core"),
    ...sourceAliases("tokens", "@iris-ui-kit/tokens"),
    ...sourceAliases("theme", "@iris-ui-kit/theme"),
    ...sourceAliases("skins", "@iris-ui-kit/skins"),
    ...sourceAliases("icons", "@iris-ui-kit/icons"),
    ...sourceAliases("vue", "@iris-ui-kit/vue"),
    ...sourceAliases("plugin-locale-zh", "@iris-ui-kit/plugin-locale-zh"),
  };
}

export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5180,
    strictPort: true,
    fs: useLocalIris ? { allow: [webRoot, irisRoot] } : undefined,
  },
  resolve: { alias: localIrisAliases() },
  test: { environment: "jsdom" },
});
