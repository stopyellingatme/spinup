import { expect, test } from '@playwright/test';
import { password, register, uniqueEmail } from './helpers';

test('a signed-out visitor is sent to sign in, then back to the page', async ({ page }) => {
	const email = uniqueEmail('guard');
	// Create the account first, then sign out, so the sign-in below is a real sign-in.
	await register(page, email);
	await page.getByTestId('user-email').click();
	await page.getByRole('menuitem', { name: 'Sign out' }).click();
	await expect(page).toHaveURL(/\/login$/);

	await page.goto('/todos');
	await expect(page).toHaveURL(/\/login\?redirectTo=%2Ftodos$/);
	await page.getByLabel('Email').fill(email);
	await page.getByLabel('Password').fill(password);
	await page.getByRole('button', { name: 'Sign in' }).click();
	await expect(page).toHaveURL(/\/todos$/);
	await expect(page.getByRole('heading', { name: 'Todos', level: 1 })).toBeVisible();
});

test('registering lands on the home route, and / redirects there', async ({ page }) => {
	await register(page, uniqueEmail('home'));
	await expect(page).toHaveURL(/\/todos$/);

	await page.goto('/');
	await expect(page).toHaveURL(/\/todos$/);
});

test('a wrong password shows an error and stays on the sign-in page', async ({ page }) => {
	await page.goto('/login');
	await page.getByLabel('Email').fill(uniqueEmail('nobody'));
	await page.getByLabel('Password').fill('not the password');
	await page.getByRole('button', { name: 'Sign in' }).click();
	await expect(page.getByTestId('form-error')).toBeVisible();
	await expect(page).toHaveURL(/\/login/);
});
