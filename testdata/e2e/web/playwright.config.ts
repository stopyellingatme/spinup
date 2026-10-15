import { defineConfig, devices } from '@playwright/test';

// run.sh copies this file and tests/ into web/e2e of the generated app and sets E2E_BASE_URL.
export default defineConfig({
	testDir: './tests',
	fullyParallel: false,
	workers: 1,
	retries: 0,
	timeout: 30_000,
	reporter: [['list']],
	use: {
		baseURL: process.env.E2E_BASE_URL ?? 'http://127.0.0.1:4173',
		trace: 'retain-on-failure'
	},
	projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }]
});
