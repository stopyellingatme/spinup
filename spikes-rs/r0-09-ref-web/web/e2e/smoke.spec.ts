import { expect, test } from '@playwright/test';

test('the home page loads', async ({ page }) => {
	await page.goto('/');
	await expect(page).toHaveTitle(/\S/);
});

test('a user can register, sign out, sign in again, and reload', async ({ page }) => {
	const email = `playwright-${Date.now()}@example.com`;
	const password = 'correct horse battery staple';
	const banner = page.getByRole('banner');
	const main = page.getByRole('main');

	// The account page needs a session. A signed-out visitor goes to the sign-in page.
	await page.goto('/account');
	await expect(page).toHaveURL(/\/login\?redirectTo=%2Faccount$/);

	await page.goto('/register');
	await main.getByLabel('Email', { exact: true }).fill(email);
	await main.getByLabel('Password', { exact: true }).fill(password);
	await main.getByRole('button', { name: 'Create account', exact: true }).click();
	await expect(banner.getByTestId('user-email')).toHaveText(email);

	// Sign out is an item of the user menu.
	await banner.getByRole('button', { name: email }).click();
	await page.getByRole('menuitem', { name: 'Sign out', exact: true }).click();
	await expect(page).toHaveURL(/\/login$/);
	await expect(banner.getByRole('link', { name: 'Sign in', exact: true })).toBeVisible();

	await main.getByLabel('Email', { exact: true }).fill(email);
	await main.getByLabel('Password', { exact: true }).fill(password);
	await main.getByRole('button', { name: 'Sign in', exact: true }).click();
	await expect(banner.getByTestId('user-email')).toHaveText(email);

	await page.reload();
	await expect(banner.getByTestId('user-email')).toHaveText(email);
});
