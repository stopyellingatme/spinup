import { expect, test } from '@playwright/test';

test('the home page loads', async ({ page }) => {
	await page.goto('/');
	await expect(page).toHaveTitle(/\S/);
});

test('a user can register, log in, reload, and log out', async ({ page }) => {
	const email = `playwright-${Date.now()}@example.com`;
	const password = 'correct horse battery staple';
	const banner = page.getByRole('banner');
	const main = page.getByRole('main');

	await page.goto('/register');
	await main.getByLabel('Email', { exact: true }).fill(email);
	await main.getByLabel('Password', { exact: true }).fill(password);
	await main.getByRole('button', { name: 'Create account', exact: true }).click();
	await expect(banner.getByTestId('user-email')).toHaveText(email);

	await banner.getByRole('button', { name: 'Sign out', exact: true }).click();
	await expect(banner.getByRole('link', { name: 'Sign in', exact: true })).toBeVisible();

	await page.goto('/login');
	await main.getByLabel('Email', { exact: true }).fill(email);
	await main.getByLabel('Password', { exact: true }).fill(password);
	await main.getByRole('button', { name: 'Sign in', exact: true }).click();
	await expect(banner.getByTestId('user-email')).toHaveText(email);

	await page.reload();
	await expect(banner.getByTestId('user-email')).toHaveText(email);

	await banner.getByRole('button', { name: 'Sign out', exact: true }).click();
	await expect(banner.getByRole('link', { name: 'Sign in', exact: true })).toBeVisible();
});
