import { expect, test } from '@playwright/test';

test('the home page loads', async ({ page }) => {
	await page.goto('/');
	await expect(page).toHaveTitle(/\S/);
});
