import { expect, test, type Page, type Response } from '@playwright/test';
import { createTodo, register, scriptNonce, uniqueEmail, watchCSP } from './helpers';

// ADR 0036 section 3.1. The title is markup. If a view renders it as HTML, the image fails to
// load and its handler sets window.__xss. A safe view shows the title as literal text.
const title = '<img src=x onerror="window.__xss=1">';

/** expectSafe checks the page after a view rendered the title. */
async function expectSafe(page: Page, response: Response | null, dialogs: string[]): Promise<void> {
	await page.waitForLoadState('load');
	// The markup did not become an element.
	await expect(page.locator('img[src="x"]')).toHaveCount(0);
	expect(await page.evaluate(() => (window as unknown as { __xss?: unknown }).__xss)).toBeUndefined();
	expect(dialogs).toEqual([]);
	if (response) {
		// SvelteKit renders the page with a new nonce in script-src (vite.config.js, csp mode auto).
		expect(scriptNonce(response.headers()['content-security-policy'])).toBeTruthy();
	}
}

test('a todo title with markup stays text on the list, read, and edit views', async ({ page }) => {
	const violations = await watchCSP(page);
	const dialogs: string[] = [];
	page.on('dialog', (dialog) => {
		dialogs.push(`${dialog.type()}: ${dialog.message()}`);
		void dialog.dismiss();
	});

	await register(page, uniqueEmail('xss'));
	await createTodo(page, title);
	const readURL = page.url();

	// The read view, rendered on the server.
	let response = await page.goto(readURL);
	await expect(page.getByRole('heading', { level: 1 })).toHaveText(title);
	await expectSafe(page, response, dialogs);
	expect(await violations()).toEqual([]);

	// The list view, rendered on the server. The title is the text of the row link.
	response = await page.goto('/todos');
	const link = page.getByRole('link', { name: title, exact: true });
	await expect(link).toHaveText(title);
	await expectSafe(page, response, dialogs);
	expect(await violations()).toEqual([]);

	// The read view again, rendered in the browser after a client-side navigation.
	await link.click();
	await expect(page).toHaveURL(readURL);
	await expect(page.getByRole('heading', { level: 1 })).toHaveText(title);
	await expectSafe(page, null, dialogs);
	expect(await violations()).toEqual([]);

	// The edit view puts the title in the value of the input.
	response = await page.goto(`${readURL}/edit`);
	await expect(page.getByLabel('Title')).toHaveValue(title);
	await expectSafe(page, response, dialogs);
	expect(await violations()).toEqual([]);
});

test('each page response has its own script nonce', async ({ page }) => {
	const first = scriptNonce((await page.goto('/'))?.headers()['content-security-policy']);
	const second = scriptNonce((await page.goto('/'))?.headers()['content-security-policy']);
	expect(first).toBeTruthy();
	expect(second).toBeTruthy();
	expect(second).not.toBe(first);
});
