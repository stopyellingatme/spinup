import { expect, test, type Route } from '@playwright/test';
import {
	adminEmail,
	apiURL,
	baseURL,
	createTodo,
	embed,
	messages,
	openSignOut,
	recordRequests,
	register,
	signIn,
	uniqueEmail
} from './helpers';

test('the sidebar marks the current page, and the user menu signs out', async ({ page }) => {
	const email = uniqueEmail('shell');
	await register(page, email);
	await expect(page).toHaveURL(/\/todos$/);

	const nav = page.getByRole('navigation', { name: 'Main' }).first();
	await expect(nav.getByRole('link', { name: 'Todos' })).toHaveAttribute('aria-current', 'page');

	const banner = page.getByRole('banner');
	await expect(banner.getByTestId('user-email')).toHaveText(email);
	await banner.getByRole('button', { name: email }).click();
	await page.getByRole('menuitem', { name: 'Sign out' }).click();
	await expect(page).toHaveURL(/\/login$/);
	await expect(banner.getByRole('link', { name: 'Sign in' })).toBeVisible();
});

// The cases below exist only in embed mode (ADR 0039 sections 10.2 and 10.6). Only the Go API can end
// the session: JavaScript cannot delete the HttpOnly cookie.
if (embed) {
	test('embed: a failed sign-out keeps the user signed in, and a second try signs out', async ({ context, page }) => {
		const email = uniqueEmail('signout-fail');
		await register(page, email);
		await expect(page).toHaveURL(`${baseURL}/todos`);

		// The first logout request fails in the network, the second answers 500, the third passes.
		let calls = 0;
		await page.route(
			(url) => url.pathname === '/api/auth/logout',
			async (route) => {
				calls += 1;
				if (calls === 1) await route.abort('failed');
				else if (calls === 2)
					await route.fulfill({ status: 500, contentType: 'application/problem+json', body: '{"status":500}' });
				else await route.fallback();
			}
		);

		for (const message of [messages.signOutUnavailable, messages.signOutServerError]) {
			await (await openSignOut(page, email)).click();
			await expect(page.getByText(message, { exact: true })).toBeVisible();
			await expect(page).toHaveURL(`${baseURL}/todos`);
			await expect(page.getByTestId('user-email')).toHaveText(email);
			expect((await context.request.get(`${apiURL}/api/session`)).status()).toBe(200);
			// The sign-out item works again.
			const item = await openSignOut(page, email);
			await expect(item).toBeEnabled();
			await page.keyboard.press('Escape');
			await expect(item).toHaveCount(0);
		}

		await (await openSignOut(page, email)).click();
		await expect(page).toHaveURL(`${baseURL}/login`);
		expect(calls).toBe(3);
		expect((await context.request.get(`${apiURL}/api/session`)).status()).toBe(401);
	});

	test('embed: sign-out stops the live list before the logout request, and loads /login one time', async ({ page }) => {
		const email = uniqueEmail('signout-live');
		await register(page, email);
		await createTodo(page, 'Live row');
		const stream = page.waitForRequest((r) => new URL(r.url()).pathname === '/api/live/todos/live');
		await page.goto('/todos');
		await expect(page.getByRole('link', { name: 'Live row' })).toBeVisible();
		await stream;
		const requests = recordRequests(page);

		await (await openSignOut(page, email)).click();
		await expect(page).toHaveURL(`${baseURL}/login`);
		await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible();
		// A later round trip of the page ends after any request that the old page could still start.
		await page.evaluate(() => fetch('/api/flags').then((r) => r.status));

		const logout = requests.indexOf('POST /api/auth/logout');
		expect(logout).toBeGreaterThanOrEqual(0);
		const after = requests.slice(logout + 1);
		expect(after.filter((r) => r.includes('/api/live/'))).toEqual([]);
		expect(after.filter((r) => r.startsWith('doc '))).toEqual(['doc /login']);
		expect(requests.slice(0, logout).filter((r) => r.startsWith('doc '))).toEqual([]);
	});

	test('embed: a live stream that answers 401 after the sign-out leaves the page on /login', async ({ page }) => {
		const email = uniqueEmail('signout-late');
		await register(page, email);
		await createTodo(page, 'Late row');

		// The stream request gets no answer until the sign-out is done.
		const held: Route[] = [];
		await page.route(
			(url) => url.pathname === '/api/live/todos/live',
			(route) => {
				held.push(route);
			}
		);
		await page.goto('/todos');
		await expect(page.getByRole('link', { name: 'Late row' })).toBeVisible();
		await expect.poll(() => held.length).toBeGreaterThan(0);
		const requests = recordRequests(page);

		await (await openSignOut(page, email)).click();
		await expect(page).toHaveURL(`${baseURL}/login`);
		await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible();
		for (const route of held) {
			// The page aborted the request at the sign-out, so the answer can have no receiver.
			await route.fulfill({ status: 401, contentType: 'application/problem+json', body: '{"status":401}' }).catch(() => {});
		}
		await page.evaluate(() => fetch('/api/flags').then((r) => r.status));
		await expect(page).toHaveURL(`${baseURL}/login`);
		expect(requests.filter((r) => r.startsWith('doc '))).toEqual(['doc /login']);
	});

	test('embed: an admin saves a flag rule, and the public flags change without a reload', async ({ page }) => {
		expect(adminEmail, 'run.sh sets E2E_ADMIN_EMAIL in embed mode').not.toBe('');
		await signIn(page, adminEmail);
		const current = await page.context().request.get(`${apiURL}/api/flags`);
		const before = ((await current.json()) as { flags: Record<string, boolean> }).flags.beta_banner;
		expect(typeof before).toBe('boolean');

		await (await openSignOut(page, adminEmail)).waitFor();
		await page.getByRole('menuitem', { name: 'Feature flags' }).click();
		await expect(page).toHaveURL(`${baseURL}/admin/flags`);
		await expect(page.getByRole('heading', { name: 'Feature flags', level: 1 })).toBeVisible();
		const requests = recordRequests(page);

		// The rule turns the flag on or off for everyone: the opposite of its value now.
		const card = page.locator('form').filter({ has: page.locator('input[name="key"][value="beta_banner"]') }).first();
		const everyone = card.getByRole('checkbox', { name: /On for everyone/ });
		await everyone.setChecked(!before);
		const reread = page.waitForResponse(
			(r) => new URL(r.url()).pathname === '/api/flags' && r.request().method() === 'GET'
		);
		await card.getByRole('button', { name: 'Save rule' }).click();
		await expect(page.getByText('Flag saved.')).toBeVisible();
		const after = ((await (await reread).json()) as { flags: Record<string, boolean> }).flags.beta_banner;
		expect(after).toBe(!before);
		expect(requests).toContain('PUT /api/admin/flags/beta_banner');
		expect(requests.filter((r) => r.startsWith('doc '))).toEqual([]);
	});

	test('embed: a user without the admin role has no flags link and gets the access error', async ({ page }) => {
		const email = uniqueEmail('no-admin');
		await register(page, email);
		const signOut = await openSignOut(page, email);
		await expect(signOut).toBeVisible();
		await expect(page.getByRole('menuitem', { name: 'Feature flags' })).toHaveCount(0);
		await page.keyboard.press('Escape');

		const requests = recordRequests(page);
		await page.goto('/admin/flags');
		await expect(page.getByRole('heading', { name: 'Access denied' })).toBeVisible();
		await expect(page.getByText(messages.roleDenied)).toBeVisible();
		await expect(page.getByText('beta_banner')).toHaveCount(0);
		// The guard runs before the admin read.
		expect(requests).not.toContain('GET /api/admin/flags');
	});

	test('embed: the Add user rule button is busy while its save runs', async ({ browser, page }) => {
		expect(adminEmail, 'run.sh sets E2E_ADMIN_EMAIL in embed mode').not.toBe('');
		// The rule is for a new account. A rule for the admin would change the flag of the other case.
		const other = await browser.newContext();
		const target = uniqueEmail('flag-user');
		await register(await other.newPage(), target);
		await other.close();

		await signIn(page, adminEmail);
		await page.goto('/admin/flags');
		await expect(page.getByRole('heading', { name: 'Feature flags', level: 1 })).toBeVisible();

		// The save of the user rule waits until the test releases it.
		const userPath = `/api/admin/flags/beta_banner/users/${encodeURIComponent(target)}`;
		let release: () => void = () => {};
		const gate = new Promise<void>((resolve) => (release = resolve));
		let held: Route | null = null;
		await page.route(
			(url) => url.pathname === userPath,
			async (route) => {
				if (route.request().method() !== 'PUT') return route.fallback();
				held = route;
				await gate;
				await route.continue();
			}
		);

		const form = page
			.locator('form')
			.filter({ has: page.locator('input[name="key"][value="beta_banner"]') })
			.filter({ has: page.getByRole('button', { name: 'Add user rule' }) });
		const add = form.getByRole('button', { name: 'Add user rule' });
		await form.getByLabel('Email address').fill(target);
		await expect(add).toBeEnabled();
		await add.click();
		await expect.poll(() => held !== null).toBe(true);
		await expect(add).toBeDisabled();

		const done = page.waitForResponse((r) => r.request().method() === 'PUT' && new URL(r.url()).pathname === userPath);
		release();
		expect((await done).ok()).toBeTruthy();
		await expect(page.getByText('Flag saved.')).toBeVisible();
		await expect(add).toBeEnabled();
		const clear = page.getByRole('button', { name: `Clear the rule of ${target}` });
		await expect(clear).toBeVisible();

		// Remove the rule again, so a repeat run starts from the same list.
		await clear.click();
		await expect(clear).toHaveCount(0);
	});
}
