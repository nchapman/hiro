import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'
import { defineConfig, globalIgnores } from 'eslint/config'

export default defineConfig([
  globalIgnores(['dist']),
  {
    files: ['**/*.{ts,tsx}'],
    extends: [
      js.configs.recommended,
      tseslint.configs.recommended,
      reactHooks.configs.flat.recommended,
      reactRefresh.configs.vite,
    ],
    languageOptions: {
      ecmaVersion: 2020,
      globals: globals.browser,
    },
    rules: {
      // This rule flags legitimate async data-fetching patterns (fetch in
      // effect, setState in callback) as errors. Disable it — the real
      // antipattern (synchronous setState in effect body) is rare here and
      // caught in review.
      'react-hooks/set-state-in-effect': 'off',
      // shadcn/ui component files export both components and variant helpers,
      // which is the intended pattern for CVA-based components.
      'react-refresh/only-export-components': ['warn', { allowConstantExport: true }],
    },
  },
])
