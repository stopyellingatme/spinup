import { expect, test } from '@playwright/test';

test('the home page loads', async ({ page }) => {
	await page.goto('/');
	await expect(page).toHaveTitle(/\S/);
});

test('the home page sends a Content-Security-Policy with script-src', async ({ page }) => {
	const response = await page.goto('/');
	expect(response).not.toBeNull();
	// The embed Go binary sets the policy and script hashes.
	expect(response?.headers()['content-security-policy']).toMatch(/(^|;\s*)script-src\s/);
});
