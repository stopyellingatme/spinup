import { expect, test } from '@playwright/test';
import { apiURL, createTodo, register, uniqueEmail } from './helpers';

test('a user creates, toggles, edits, and deletes a todo', async ({ page }) => {
	await register(page, uniqueEmail('crud'));
	await page.goto('/todos');
	await expect(page.getByRole('heading', { name: 'No todos yet' })).toBeVisible();

	// Create. The browser opens the read view and a toast confirms.
	await page.getByRole('link', { name: 'New todo' }).first().click();
	await page.getByLabel('Title').fill('Buy milk');
	await page.getByRole('button', { name: 'Create todo' }).click();
	await expect(page).toHaveURL(/\/todos\/\d+/);
	await expect(page.getByText('Todo created.')).toBeVisible();
	// The flag leaves the address bar.
	await expect(page).toHaveURL(/\/todos\/\d+$/);

	// The title is the row link.
	await page.goto('/todos');
	const link = page.getByRole('link', { name: 'Buy milk' });
	await expect(link).toBeVisible();

	// Toggle done in the row. It persists after a reload.
	const done = page.getByRole('checkbox', { name: 'Done: Buy milk' });
	await expect(done).not.toBeChecked();
	await Promise.all([
		page.waitForResponse((res) => res.request().method() === 'POST' && res.url().includes('toggle')),
		done.click()
	]);
	await expect(done).toBeChecked();
	await page.reload();
	await expect(page.getByRole('checkbox', { name: 'Done: Buy milk' })).toBeChecked();

	// Edit from the row menu.
	await page.getByRole('button', { name: 'Actions for Buy milk' }).click();
	await page.getByRole('menuitem', { name: 'Edit' }).click();
	await expect(page).toHaveURL(/\/todos\/\d+\/edit$/);
	await page.getByLabel('Title').fill('Buy oat milk');
	await page.getByRole('button', { name: /^Save/ }).click();
	await expect(page).toHaveURL(/\/todos\/\d+$/);
	await expect(page.getByText('Changes saved.')).toBeVisible();
	await expect(page.getByRole('heading', { name: 'Buy oat milk', level: 1 })).toBeVisible();

	// Delete from the read view, through the alert dialog.
	await page.getByRole('button', { name: 'Delete' }).click();
	const dialog = page.getByRole('alertdialog');
	await expect(dialog).toBeVisible();
	await dialog.getByRole('button', { name: 'Delete' }).click();
	await expect(page).toHaveURL(/\/todos$/);
	await expect(page.getByText('Todo deleted.')).toBeVisible();
	await expect(page.getByRole('heading', { name: 'No todos yet' })).toBeVisible();
});

test('an empty title shows an error that the input points to', async ({ page }) => {
	await register(page, uniqueEmail('validation'));
	await page.goto('/todos/new');
	await page.getByRole('button', { name: 'Create todo' }).click();

	const title = page.getByLabel('Title');
	await expect(title).toHaveAttribute('aria-invalid', 'true');
	const describedBy = await title.getAttribute('aria-describedby');
	expect(describedBy).toBeTruthy();
	await expect(page.locator(`[id="${describedBy}"]`)).toBeVisible();
	await expect(page).toHaveURL(/\/todos\/new$/);
});

test("a user cannot see or open another user's todo", async ({ browser }) => {
	const first = await browser.newContext();
	const page = await first.newPage();
	await register(page, uniqueEmail('owner'));
	await createTodo(page, 'Private note');
	const url = page.url();

	const second = await browser.newContext();
	const other = await second.newPage();
	await register(other, uniqueEmail('intruder'));
	await other.goto('/todos');
	await expect(other.getByText('Private note')).toHaveCount(0);
	await expect(other.getByRole('heading', { name: 'No todos yet' })).toBeVisible();

	const response = await other.goto(url);
	expect(response?.status()).toBe(404);
	await expect(other.getByRole('heading', { name: 'Page not found' })).toBeVisible();

	await first.close();
	await second.close();
});

test('the list has 20 rows on a page and links to the next page', async ({ page }) => {
	await register(page, uniqueEmail('pages'));
	// The page context carries the session cookie, so this request is the signed-in user.
	for (let i = 1; i <= 21; i++) {
		const res = await page.context().request.post(`${apiURL}/api/todos`, {
			data: { title: `Item ${String(i).padStart(2, '0')}`, done: false }
		});
		expect(res.ok()).toBeTruthy();
	}

	await page.goto('/todos');
	await expect(page.getByRole('row')).toHaveCount(21); // 20 rows and the header row
	const next = page.getByRole('link', { name: 'Next' });
	await expect(next).toBeVisible();
	await next.click();
	await expect(page).toHaveURL(/\/todos\?page=2$/);
	await expect(page.getByRole('row')).toHaveCount(2); // 1 row and the header row
});
