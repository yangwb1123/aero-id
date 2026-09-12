import { readdir, readFile } from "node:fs/promises";

const webRoot = new URL("../", import.meta.url);
const srcRoot = new URL("src/", webRoot);
const installedPackagesRoot = new URL("node_modules/", webRoot).href;
const packageJson = JSON.parse(
  await readFile(new URL("package.json", webRoot), "utf8"),
);
const sourceExtensions = new Set([".ts", ".js", ".vue"]);
const importPatterns = [
  /\bfrom\s*["']([^"']+)["']/g,
  /\bimport\s*\(\s*["']([^"']+)["']/g,
  /\bimport\s*["']([^"']+)["']/g,
];

async function sourceFiles(directory) {
  const files = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const target = new URL(
      entry.name + (entry.isDirectory() ? "/" : ""),
      directory,
    );
    if (entry.isDirectory()) files.push(...(await sourceFiles(target)));
    else if (
      sourceExtensions.has(entry.name.slice(entry.name.lastIndexOf(".")))
    )
      files.push(target);
  }
  return files;
}

function packageName(specifier) {
  return specifier.startsWith("@")
    ? specifier.split("/").slice(0, 2).join("/")
    : specifier.split("/")[0];
}

const irisImports = new Set();
for (const file of await sourceFiles(srcRoot)) {
  const source = await readFile(file, "utf8");
  for (const pattern of importPatterns) {
    for (const match of source.matchAll(pattern)) {
      const specifier = match[1];
      if (!specifier.toLowerCase().includes("iris")) continue;
      if (!specifier.startsWith("@iris-ui-kit/")) {
        throw new Error(
          `${file.pathname}: Iris UI must be imported from a published @iris-ui-kit/* package, got ${specifier}`,
        );
      }
      irisImports.add(specifier);
    }
  }
}

if (irisImports.size === 0) {
  throw new Error("web/src does not import an @iris-ui-kit/* package");
}

for (const specifier of irisImports) {
  const dependency = packageName(specifier);
  const range = packageJson.dependencies?.[dependency];
  if (!range)
    throw new Error(`${dependency} must be a direct runtime dependency`);
  if (/^(workspace|file|link|portal):/.test(range)) {
    throw new Error(
      `${dependency} must use a published npm version, got ${range}`,
    );
  }
  const resolved = import.meta.resolve(specifier);
  if (!resolved.startsWith(installedPackagesRoot)) {
    throw new Error(
      `${specifier} did not resolve from web/node_modules: ${resolved}`,
    );
  }
}

console.log(
  `Verified published Iris UI imports: ${[...irisImports].sort().join(", ")}`,
);
