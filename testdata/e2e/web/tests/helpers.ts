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

/** A securitypolicyviolation event that the browser fired, as watchCSP records it. */
export type CSPViolation = { directive: string; blocked: string; source: string };

/**
 * watchCSP records each securitypolicyviolation event of the page, also after a navigation.
 * Call it before the first goto. The returned function reads the events of the current document.
 */
export async function watchCSP(page: Page): Promise<() => Promise<CSPViolation[]>> {
	// Playwright runs init scripts outside the CSP of the page, so the policy does not block this one.
	await page.addInitScript(() => {
		const w = window as unknown as { __cspViolations: CSPViolation[] };
		w.__cspViolations = [];
		document.addEventListener('securitypolicyviolation', (event) => {
			w.__cspViolations.push({
				directive: event.effectiveDirective,
				blocked: event.blockedURI,
				source: `${event.sourceFile}:${event.lineNumber}`
			});
		});
	});
	return () =>
		page.evaluate(() => (window as unknown as { __cspViolations?: CSPViolation[] }).__cspViolations ?? []);
}

/** scriptNonce returns the nonce of script-src in a Content-Security-Policy header, or null. */
export function scriptNonce(header: string | undefined): string | null {
	const scriptSrc = (header ?? '')
		.split(';')
		.map((directive) => directive.trim())
		.find((directive) => directive.startsWith('script-src '));
	return scriptSrc?.match(/'nonce-([A-Za-z0-9+/_=-]+)'/)?.[1] ?? null;
}

/** createTodo fills in the new page and submits it. The browser then shows the read view. */
export async function createTodo(page: Page, title: string): Promise<void> {
	await page.goto('/todos/new');
	await page.getByLabel('Title').fill(title);
	await page.getByRole('button', { name: 'Create todo' }).click();
	await expect(page).toHaveURL(/\/todos\/\d+/);
}
