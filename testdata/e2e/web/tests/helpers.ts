import { expect, type Page } from '@playwright/test';

/** The password of every test user. */
export const password = 'correct horse battery staple';

/** The address of the Go API. In production a reverse proxy serves /api on the web origin. */
export const apiURL = process.env.E2E_API_URL ?? 'http://127.0.0.1:8080';

let counter = 0;

/** uniqueEmail returns an email address that no earlier test used. */
export function uniqueEmail(label: string): string {
	counter += 1;
	return `${label}-${Date.now()}-${counter}@example.com`;
}

/** register creates an account in the browser. The user then lands on the home route. */
export async function register(page: Page, email: string): Promise<void> {
	await page.goto('/register');
	await page.getByLabel('Email').fill(email);
	await page.getByLabel('Password').fill(password);
	await page.getByRole('button', { name: 'Create account' }).click();
	await expect(page.getByTestId('user-email')).toHaveText(email);
}

/** createTodo fills in the new page and submits it. The browser then shows the read view. */
export async function createTodo(page: Page, title: string): Promise<void> {
	await page.goto('/todos/new');
	await page.getByLabel('Title').fill(title);
	await page.getByRole('button', { name: 'Create todo' }).click();
	await expect(page).toHaveURL(/\/todos\/\d+/);
}
