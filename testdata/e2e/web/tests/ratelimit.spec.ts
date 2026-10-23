import { expect, test, type Page } from '@playwright/test';
import { uniqueEmail } from './helpers';

// run.sh starts Node with ADDRESS_HEADER=X-Forwarded-For, so the web app passes the client
// address of the browser request to the API. A loopback client is exempt, so the test sends a
// public address (from the documentation range 198.51.100.0/24).
// The default login limit is a burst of 20 attempts, then one attempt every 3 seconds. A new
// email for each attempt keeps the per-email lockout from answering first.
const maxAttempts = 60;

async function attempt(page: Page): Promise<string> {
	await page.goto('/login');
	await page.getByLabel('Email').fill(uniqueEmail('limit'));
	await page.getByLabel('Password').fill('not the password');
	await page.getByRole('button', { name: 'Sign in' }).click();
	const message = page.getByTestId('form-error');
	await expect(message).toBeVisible();
	return (await message.textContent()) ?? '';
}

test.describe('the login rate limit', () => {
	test.setTimeout(120_000);

	test('a client that sends too many logins is refused, and another client is not', async ({ browser }) => {
		const flood = await browser.newContext({ extraHTTPHeaders: { 'x-forwarded-for': '198.51.100.20' } });
		const page = await flood.newPage();
		let limited = '';
		for (let i = 0; i < maxAttempts && !limited; i++) {
			const message = await attempt(page);
			if (/too many/i.test(message)) limited = message;
		}
		expect(limited, 'the login limit answered within the attempts').toMatch(/Try again in \d+ seconds?\./);
		await flood.close();

		// A client with another address still reaches the password check.
		const other = await browser.newContext({ extraHTTPHeaders: { 'x-forwarded-for': '198.51.100.21' } });
		const message = await attempt(await other.newPage());
		expect(message).not.toMatch(/too many/i);
		await other.close();
	});
});
