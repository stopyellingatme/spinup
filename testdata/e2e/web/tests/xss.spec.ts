import { expect, test, type Page, type Response } from '@playwright/test';
import {
	createTodo,
	cspDirectives,
	embed,
	expectStrictScriptSrc,
	inlineScriptHashes,
	pagePolicy,
	register,
	uniqueEmail,
	watchCSP
} from './helpers';

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
		// Node mode: SvelteKit renders the page with a new nonce in script-src (vite.config.js, csp
		// mode auto). Embed mode: app.WithSPA sets the SHA-256 of the inline boot script.
		expectStrictScriptSrc(response.headers()['content-security-policy']);
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
	const firstResponse = await page.goto('/');
	const firstHeader = firstResponse?.headers()['content-security-policy'];
	const secondResponse = await page.goto('/');
	const secondHeader = secondResponse?.headers()['content-security-policy'];
	const first = expectStrictScriptSrc(firstHeader);
	const second = expectStrictScriptSrc(secondHeader);
	for (const header of [firstHeader, secondHeader]) {
		const directives = cspDirectives(header);
		for (const [name, tokens] of Object.entries(pagePolicy)) {
			expect(directives.get(name), `${name} of ${header}`).toEqual(tokens);
		}
	}
	if (!embed) {
		// Node mode renders each page on the server with a new nonce.
		expect(first.nonces[0]).toBeTruthy();
		expect(second.nonces[0]).toBeTruthy();
		expect(second.nonces[0]).not.toBe(first.nonces[0]);
		return;
	}
	// Embed mode serves one static shell. It has no nonce. Both responses carry the same hash, and
	// that hash is the SHA-256 of the inline boot script of the shell that the binary sent.
	expect(first.nonces).toEqual([]);
	expect(second.hashes).toEqual(first.hashes);
	const body = (await secondResponse?.text()) ?? '';
	const actual = inlineScriptHashes(body);
	expect(actual.length).toBeGreaterThan(0);
	expect([...first.hashes].sort()).toEqual([...actual].sort());
});
