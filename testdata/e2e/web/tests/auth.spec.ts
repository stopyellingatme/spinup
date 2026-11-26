import { expect, test, type Route } from '@playwright/test';
import {
	apiURL,
	baseURL,
	becomeVisible,
	createTodo,
	documentMarked,
	embed,
	markDocument,
	todoID,
	openSignOut,
	password,
	recordRequests,
	register,
	uniqueEmail
} from './helpers';

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

// The cases below exist only in embed mode (ADR 0039 section 10.6). The root load reads the session
// one time in the browser, so a change of the session elsewhere must reach the page by an event.
if (embed) {
	test('embed: a tab that another tab moves to a new account shows no data of the old account', async ({ context }) => {
		const tabA = await context.newPage();
		const first = uniqueEmail('tab-a');
		await register(tabA, first);
		await createTodo(tabA, 'Secret of A');
		await tabA.goto('/todos');
		await expect(tabA.getByRole('link', { name: 'Secret of A' })).toBeVisible();
		await markDocument(tabA);

		// Tab B shares the cookie. Its sign-out tells tab A, which loads the plain sign-in page.
		const tabB = await context.newPage();
		await tabB.goto('/todos');
		await (await openSignOut(tabB, first)).click();
		await expect(tabB).toHaveURL(`${baseURL}/login`);
		await expect(tabA).toHaveURL(`${baseURL}/login`);
		expect(await documentMarked(tabA)).toBe(false);
		await expect(tabA.getByRole('button', { name: 'Sign in' })).toBeVisible();

		// Tab B registers user B. Tab A replaces its document again and shows user B on the home route.
		await markDocument(tabA);
		const second = uniqueEmail('tab-b');
		await register(tabB, second);
		await expect(tabA.getByTestId('user-email')).toHaveText(second);
		await expect(tabA).toHaveURL(`${baseURL}/todos`);
		expect(await documentMarked(tabA)).toBe(false);
		await expect(tabA.getByRole('heading', { name: 'No todos yet' })).toBeVisible();
		await expect(tabA.getByText('Secret of A')).toHaveCount(0);
		await expect(tabA.getByText(first)).toHaveCount(0);
	});

	test('embed: a tab that becomes visible after a sign-out elsewhere goes to /login', async ({ context, page }) => {
		const email = uniqueEmail('visible');
		await register(page, email);
		await createTodo(page, 'Visible secret');
		await page.goto('/todos');
		await expect(page.getByRole('link', { name: 'Visible secret' })).toBeVisible();
		await markDocument(page);

		// The sign-out goes straight to the API, so no tab hears of it. The page still shows the user.
		const res = await context.request.post(`${apiURL}/api/auth/logout`);
		expect(res.ok()).toBeTruthy();
		await expect(page.getByTestId('user-email')).toHaveText(email);

		await becomeVisible(page);
		await expect(page).toHaveURL(`${baseURL}/login`);
		expect(await documentMarked(page)).toBe(false);
		await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible();
		await expect(page.getByText('Visible secret')).toHaveCount(0);
	});

	test('embed: an API call that answers 401 sends the page to /login one time', async ({ context, page }) => {
		const email = uniqueEmail('expired');
		await register(page, email);
		await createTodo(page, 'Expired note');
		await page.goto('/todos');
		const box = page.getByRole('checkbox', { name: 'Done: Expired note' });
		await expect(box).toBeVisible();
		const requests = recordRequests(page);

		// The session cookie goes away, as when it expires. The next API call answers 401.
		await context.clearCookies();
		await box.click();
		await expect(page).toHaveURL(`${baseURL}/login`);
		await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible();
		// The sign-in page reads the session (401). A loop would load a second document before a
		// later round trip of the page ends.
		await page.evaluate(() => fetch('/api/flags').then((r) => r.status));
		expect(requests.filter((r) => r.startsWith('doc '))).toEqual(['doc /login']);
	});

	test('embed: a failed session read shows a retry card and no page', async ({ page }) => {
		const email = uniqueEmail('retry');
		await register(page, email);
		await createTodo(page, 'Retry note');

		// Each session read answers 500 until the route goes away.
		const failing = (url: URL) => url.pathname === '/api/session';
		await page.route(failing, (route) =>
			route.fulfill({ status: 500, contentType: 'application/problem+json', body: '{"status":500}' })
		);
		await page.goto('/todos');
		const card = page.getByTestId('session-error');
		await expect(card).toBeVisible();
		// No guard redirects on a session that is not verified: the address keeps the deep link.
		await expect(page).toHaveURL(`${baseURL}/todos`);
		await expect(page.getByRole('heading', { name: 'Todos', level: 1 })).toHaveCount(0);
		await expect(page.getByText('Retry note')).toHaveCount(0);
		await expect(page.getByTestId('user-email')).toHaveCount(0);

		// The session read works again. Try again reads it, and the page shows.
		await page.unroute(failing);
		await card.getByRole('button', { name: 'Try again' }).click();
		await expect(card).toHaveCount(0);
		await expect(page.getByTestId('user-email')).toHaveText(email);
		await expect(page.getByRole('link', { name: 'Retry note' })).toBeVisible();
		await expect(page).toHaveURL(`${baseURL}/todos`);
	});

	test('embed: one failed session read keeps the deep link of an item, and Try again opens it', async ({ page }) => {
		const email = uniqueEmail('deep');
		await register(page, email);
		await createTodo(page, 'Deep note');
		const id = todoID(page.url());
		const deepLink = `${baseURL}/todos/${id}`;

		// Only the first session read of the next document answers 500.
		let failed = 0;
		await page.route(
			(url) => url.pathname === '/api/session',
			async (route) => {
				if (failed === 0) {
					failed += 1;
					await route.fulfill({ status: 500, contentType: 'application/problem+json', body: '{"status":500}' });
					return;
				}
				await route.fallback();
			}
		);
		await page.goto(`/todos/${id}`);
		const card = page.getByTestId('session-error');
		await expect(card).toBeVisible();
		await expect(page).toHaveURL(deepLink);
		await expect(page.getByRole('heading', { name: 'Deep note' })).toHaveCount(0);
		await expect(page.getByTestId('user-email')).toHaveCount(0);

		// Try again reads the session. The leaf load runs again on verified data, at the same address.
		await card.getByRole('button', { name: 'Try again' }).click();
		await expect(card).toHaveCount(0);
		await expect(page.getByRole('heading', { name: 'Deep note', level: 1 })).toBeVisible();
		await expect(page.getByTestId('user-email')).toHaveText(email);
		await expect(page).toHaveURL(deepLink);
		expect(failed).toBe(1);
	});

	test('embed: a signed-out visitor with one failed session read goes to sign in with the deep link', async ({ page }) => {
		let failed = 0;
		await page.route(
			(url) => url.pathname === '/api/session',
			async (route) => {
				if (failed === 0) {
					failed += 1;
					await route.fulfill({ status: 500, contentType: 'application/problem+json', body: '{"status":500}' });
					return;
				}
				await route.fallback();
			}
		);
		await page.goto('/todos/4242?tab=notes');
		const card = page.getByTestId('session-error');
		await expect(card).toBeVisible();
		await expect(page).toHaveURL(`${baseURL}/todos/4242?tab=notes`);

		// The retry gets 401: signed out. The guard sends the visitor to sign in, with the deep link.
		await card.getByRole('button', { name: 'Try again' }).click();
		await expect(page).toHaveURL(`${baseURL}/login?redirectTo=${encodeURIComponent('/todos/4242?tab=notes')}`);
		await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible();
		await expect(card).toHaveCount(0);
		expect(failed).toBe(1);
	});

	test('embed: an identity change replaces the document while the flags read still waits', async ({ context }) => {
		const tabA = await context.newPage();
		const first = uniqueEmail('slow-flags');
		await register(tabA, first);
		await createTodo(tabA, 'Secret behind flags');
		await tabA.goto('/todos');
		await expect(tabA.getByRole('link', { name: 'Secret behind flags' })).toBeVisible();
		await markDocument(tabA);

		// From now on, each flags read of tab A waits until the test releases it.
		const held: Route[] = [];
		const flagsRead = (url: URL) => url.pathname === '/api/flags';
		await tabA.route(flagsRead, (route) => {
			held.push(route);
		});

		const tabB = await context.newPage();
		await tabB.goto('/todos');
		await (await openSignOut(tabB, first)).click();
		await expect(tabB).toHaveURL(`${baseURL}/login`);
		const second = uniqueEmail('slow-flags-b');
		await register(tabB, second);

		// Tab A read the session again and asked for the flags. It replaces its document while that
		// flags read waits.
		await expect.poll(() => held.length).toBeGreaterThan(0);
		await expect.poll(() => documentMarked(tabA).catch(() => false)).toBe(false);
		await expect(tabA.getByText('Secret behind flags')).toHaveCount(0);
		await expect(tabA.getByText(first)).toHaveCount(0);
		expect(new URL(tabA.url()).pathname).toMatch(/^\/(login|todos)$/);

		await tabA.unroute(flagsRead);
		for (const route of held) await route.continue().catch(() => {});
		await expect(tabA.getByTestId('user-email')).toHaveText(second);
		await expect(tabA.getByText('Secret behind flags')).toHaveCount(0);
	});
}
