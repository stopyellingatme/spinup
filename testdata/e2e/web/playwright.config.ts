import { defineConfig, devices } from '@playwright/test';

// run.sh copies this file and tests/ into web/e2e of the generated app and sets E2E_BASE_URL.
// E2E_BROWSERS=all adds Firefox and WebKit (ADR 0036 section 3.4). The default is Chromium only.
const projects = [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }];
if (process.env.E2E_BROWSERS === 'all') {
	projects.push(
		{ name: 'firefox', use: { ...devices['Desktop Firefox'] } },
		{ name: 'webkit', use: { ...devices['Desktop Safari'] } }
	);
} else if (process.env.E2E_BROWSERS && process.env.E2E_BROWSERS !== 'chromium') {
	throw new Error(
		`E2E_BROWSERS=${process.env.E2E_BROWSERS} is not known: use "all" or "chromium".\n  Try: E2E_BROWSERS=all testdata/e2e/web/run.sh`
	);
}

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
	projects
});
