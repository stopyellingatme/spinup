import { expect, test, type Page } from '@playwright/test';

// One serial flow: register, log in, read the session on the server, use a resource, log out.
test.describe.configure({ mode: 'serial' });

const password = 'correct horse battery staple';
const email = `ada-${Date.now()}@example.com`;

async function login(page: Page) {
	await page.goto('/login');
	await page.getByLabel('Email').fill(email);
	await page.getByLabel('Password').fill(password);
	await page.getByRole('button', { name: 'Sign in' }).click();
	await expect(page).toHaveURL(/\/$/);
	await expect(page.getByTestId('user-email')).toHaveText(email);
}

test('the home page renders', async ({ page }) => {
	await page.goto('/');
	await expect(page.getByRole('link', { name: 'Sign in' }).first()).toBeVisible();
});

test('a new user registers', async ({ page }) => {
	await page.goto('/register');
	await page.getByLabel('Email').fill(email);
	await page.getByLabel('Password').fill(password);
	await page.getByRole('button', { name: 'Create account' }).click();
	// Registering signs the user in and goes to the home page.
	await expect(page).toHaveURL(/\/$/);
	await expect(page.getByTestId('user-email')).toHaveText(email);
});

test('the user logs in', async ({ page }) => {
	await login(page);
});

test('the session survives a server-side render', async ({ page }) => {
	await login(page);
	await page.goto('/account');
	await page.reload();
	await expect(page).toHaveURL(/\/account$/);
	await expect(page.getByTestId('account-email')).toHaveText(email);
});

test('a user manages a to-do, then logs out', async ({ page }) => {
	await login(page);

	// Create.
	await page.goto('/todos/new');
	await page.getByLabel('Title').fill('Buy milk');
	await page.getByRole('button', { name: 'Create' }).click();
	// Creating a to-do opens its page.
	await expect(page).toHaveURL(/\/todos\/\d+$/);
	await page.goto('/todos');
	await expect(page.getByRole('cell', { name: 'Buy milk' })).toBeVisible();

	// Edit.
	await page.getByRole('row', { name: /Buy milk/ }).getByRole('link').click();
	await expect(page).toHaveURL(/\/todos\/\d+$/);
	await page.getByLabel('Title').fill('Buy oat milk');
	await page.getByLabel('Done').check();
	// Wait for the save to finish before the next navigation.
	await Promise.all([
		page.waitForResponse((res) => res.request().method() === 'POST'),
		page.getByRole('button', { name: 'Save' }).click()
	]);
	await page.goto('/todos');
	const row = page.getByRole('row', { name: /Buy oat milk/ });
	await expect(row).toBeVisible();
	await expect(row.getByRole('cell', { name: 'Yes' })).toBeVisible();

	// Delete. The page asks for a confirmation.
	await row.getByRole('link').click();
	page.once('dialog', (dialog) => dialog.accept());
	await page.getByRole('button', { name: 'Delete' }).click();
	await expect(page).toHaveURL(/\/todos$/);
	await expect(page.getByText('Buy oat milk')).toHaveCount(0);

	// Log out. The account page then redirects to the login page.
	await page.goto('/account');
	await page.getByRole('button', { name: 'Sign out' }).first().click();
	await page.goto('/account');
	await expect(page).toHaveURL(/\/login$/);
});
