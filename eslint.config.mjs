import js from '@eslint/js';
import nextPlugin from '@next/eslint-plugin-next';
import globals from 'globals';
import tseslint from 'typescript-eslint';
import { fileURLToPath } from 'node:url';

export default tseslint.config(
  {
    ignores: [
      '**/node_modules/**',
      '**/.next/**',
      '**/dist/**',
      'bin/**',
      '.tmp/**',
      '**/next-env.d.ts',
    ],
  },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ['**/*.{js,mjs,ts,tsx}'],
    languageOptions: { globals: globals.node },
    plugins: { '@next/next': nextPlugin },
    settings: {
      next: {
        rootDir: fileURLToPath(new URL('./apps/dashboard/', import.meta.url)),
      },
    },
  },
  {
    files: ['apps/dashboard/**/*.{ts,tsx}'],
    languageOptions: { globals: globals.browser },
    rules: nextPlugin.configs['core-web-vitals'].rules,
  },
);
