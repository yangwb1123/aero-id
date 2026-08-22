import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { existsSync, readdirSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const webRoot = fileURLToPath(new URL('.', import.meta.url))
const irisRoot = resolve(webRoot, '../../iris-ui')
const irisPackages = join(irisRoot, 'packages')
const useLocalIris =
  process.env.IRIS_UI_SOURCE === '1' &&
  existsSync(join(irisPackages, 'react', 'src', 'index.ts'))

const packageSource = (name: string) => join(irisPackages, name, 'src', 'index.ts')

function subpathAliases(pkg: string, bare: string): Record<string, string> {
  const aliases: Record<string, string> = {}
  const sourceDir = join(irisPackages, pkg, 'src')
  for (const entry of readdirSync(sourceDir, { withFileTypes: true })) {
    if (entry.isDirectory() && existsSync(join(sourceDir, entry.name, 'index.ts'))) {
      aliases[`${bare}/${entry.name}`] = join(sourceDir, entry.name, 'index.ts')
    }
  }
  return aliases
}

function fileSubpathAliases(pkg: string, bare: string): Record<string, string> {
  const aliases: Record<string, string> = {}
  const sourceDir = join(irisPackages, pkg, 'src')
  for (const entry of readdirSync(sourceDir, { withFileTypes: true })) {
    if (entry.isFile() && entry.name.endsWith('.ts') && entry.name !== 'index.ts') {
      aliases[`${bare}/${entry.name.slice(0, -3)}`] = join(sourceDir, entry.name)
    }
  }
  return aliases
}

function localIrisAliases(): Record<string, string> {
  if (!useLocalIris) return {}
  return {
    ...subpathAliases('core', '@iris-ui-kit/core'),
    ...fileSubpathAliases('core', '@iris-ui-kit/core'),
    '@iris-ui-kit/core': packageSource('core'),
    '@iris-ui-kit/tokens': packageSource('tokens'),
    '@iris-ui-kit/theme': packageSource('theme'),
    '@iris-ui-kit/skins': packageSource('skins'),
    '@iris-ui-kit/icons': packageSource('icons'),
    ...subpathAliases('react', '@iris-ui-kit/react'),
    '@iris-ui-kit/react': packageSource('react'),
    '@iris-ui-kit/plugin-locale-zh/core': join(
      irisPackages,
      'plugin-locale-zh',
      'src',
      'core',
      'index.ts',
    ),
  }
}

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5180,
    strictPort: true,
    fs: useLocalIris ? { allow: [webRoot, irisRoot] } : undefined,
  },
  resolve: { alias: localIrisAliases() },
  test: { environment: 'jsdom' },
})
