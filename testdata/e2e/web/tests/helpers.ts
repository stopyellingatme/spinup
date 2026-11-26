import { createHash } from 'node:crypto';
import { expect, type Locator, type Page, type Response } from '@playwright/test';

/**
 * embed is true when run.sh tests an app in embed mode (E2E_FRONTEND_MODE=embed): one Go binary
 * serves a static shell and the API. The default is node mode: a SvelteKit server renders the pages.
 */
export const embed = process.env.E2E_FRONTEND_MODE === 'embed';
if (!['node', 'embed', undefined].includes(process.env.E2E_FRONTEND_MODE)) {
	throw new Error(
		`E2E_FRONTEND_MODE=${process.env.E2E_FRONTEND_MODE} is not known: use node or embed.\n  Try: E2E_FRONTEND_MODE=node testdata/e2e/web/run.sh`
	);
}

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
	return scriptSources(header).nonces[0] ?? null;
}

/** cspDirectives parses a Content-Security-Policy header into its directives and their tokens. */
export function cspDirectives(header: string | undefined): Map<string, string[]> {
	const directives = new Map<string, string[]>();
	for (const part of (header ?? '').split(';')) {
		const [raw, ...tokens] = part.trim().split(/\s+/);
		const name = raw.toLowerCase();
		// A browser uses the first directive of a name and ignores a repeat.
		if (name && !directives.has(name)) directives.set(name, tokens);
	}
	return directives;
}

/** The script sources of a policy, as scriptSources reads them. */
export type ScriptSources = { tokens: string[]; nonces: string[]; hashes: string[]; unsafe: string[] };

/**
 * scriptSources reads the tokens of script-src. nonces holds each nonempty nonce, hashes each
 * SHA-256 source, and unsafe each 'unsafe-inline' or 'unsafe-eval' token.
 */
export function scriptSources(header: string | undefined): ScriptSources {
	const tokens = cspDirectives(header).get('script-src') ?? [];
	const nonces: string[] = [];
	const hashes: string[] = [];
	const unsafe: string[] = [];
	for (const token of tokens) {
		const nonce = token.match(/^'nonce-([A-Za-z0-9+/_=-]+)'$/);
		const hash = token.match(/^'sha256-([A-Za-z0-9+/]{43}=)'$/);
		if (nonce) nonces.push(nonce[1]);
		else if (hash) hashes.push(hash[1]);
		else if (/^'unsafe-(inline|eval)'$/i.test(token)) unsafe.push(token);
	}
	return { tokens, nonces, hashes, unsafe };
}

/**
 * expectStrictScriptSrc checks script-src of a policy for the mode: node mode needs a nonce,
 * and embed mode needs a SHA-256 source. Neither mode may allow unsafe-inline or unsafe-eval.
 */
export function expectStrictScriptSrc(header: string | undefined): ScriptSources {
	const sources = scriptSources(header);
	expect(sources.tokens, `script-src of ${header}`).toContain("'self'");
	expect(sources.unsafe, `script-src of ${header}`).toEqual([]);
	if (embed) expect(sources.hashes.length, `script-src of ${header}`).toBeGreaterThan(0);
	else expect(sources.nonces.length, `script-src of ${header}`).toBeGreaterThan(0);
	return sources;
}

/**
 * The directives of the page policy besides script-src. Node mode sets them in vite.config.js,
 * and embed mode in app.WithSPA (stage 0 of ADR 0039).
 */
export const pagePolicy: Record<string, string[]> = {
	'default-src': ["'self'"],
	'style-src': ["'self'", "'unsafe-inline'"],
	'img-src': ["'self'", 'data:'],
	'font-src': ["'self'", 'data:'],
	'connect-src': ["'self'"],
	'object-src': ["'none'"],
	'base-uri': ["'self'"],
	'form-action': ["'self'"],
	'frame-ancestors': ["'none'"]
};

/** inlineScriptHashes returns the base64 SHA-256 of each inline script of an HTML document. */
export function inlineScriptHashes(html: string): string[] {
	const hashes: string[] = [];
	for (const match of html.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script\s*>/gi)) {
		if (/\ssrc\s*=/i.test(match[1])) continue;
		// A browser hashes the script text after it normalizes CRLF and CR to LF.
		const text = match[2].replace(/\r\n?/g, '\n');
		hashes.push(createHash('sha256').update(text, 'utf8').digest('base64'));
	}
	return hashes;
}

/** todoID returns the id of a todo from the URL of its read view. */
export function todoID(url: string): string {
	const id = new URL(url).pathname.match(/^\/todos\/(\d+)$/)?.[1];
	if (!id) throw new Error(`${url} is not the read view of a todo`);
	return id;
}

/**
 * isToggleWrite matches the write request of a row toggle. Node mode posts the form action
 * ?/toggle at the list path. Embed mode sends PUT to the API path of the one todo.
 */
export function isToggleWrite(res: Response, id: string): boolean {
	const url = new URL(res.url());
	const method = res.request().method();
	if (embed) return method === 'PUT' && url.pathname === `/api/todos/${id}` && url.search === '';
	return method === 'POST' && url.pathname === '/todos' && url.search.startsWith('?/toggle');
}

/** createTodo fills in the new page and submits it. The browser then shows the read view. */
export async function createTodo(page: Page, title: string): Promise<void> {
	await page.goto('/todos/new');
	await page.getByLabel('Title').fill(title);
	await page.getByRole('button', { name: 'Create todo' }).click();
	await expect(page).toHaveURL(/\/todos\/\d+/);
}

/** The origin of the pages. In embed mode the Go binary serves the pages and the API on it. */
export const baseURL = process.env.E2E_BASE_URL ?? 'http://127.0.0.1:4173';

/** The account with the admin role of the flags module. run.sh creates it in embed mode. */
export const adminEmail = process.env.E2E_ADMIN_EMAIL ?? '';

/** signIn signs in on the sign-in page. The user then lands on the home route. */
export async function signIn(page: Page, email: string, secret = password): Promise<void> {
	await page.goto('/login');
	await page.getByLabel('Email').fill(email);
	await page.getByLabel('Password').fill(secret);
	await page.getByRole('button', { name: 'Sign in' }).click();
	await expect(page.getByTestId('user-email')).toHaveText(email);
}

/** openSignOut opens the user menu and returns its sign-out item. */
export async function openSignOut(page: Page, email: string): Promise<Locator> {
	await page.getByRole('banner').getByRole('button', { name: email }).click();
	return page.getByRole('menuitem', { name: 'Sign out' });
}

/** markDocument marks the current document. documentMarked is false after a full document load. */
export async function markDocument(page: Page): Promise<void> {
	await page.evaluate(() => {
		(window as unknown as { __e2eOldDocument?: boolean }).__e2eOldDocument = true;
	});
}

/** documentMarked tells whether the document of markDocument is still loaded. */
export function documentMarked(page: Page): Promise<boolean> {
	return page.evaluate(() => (window as unknown as { __e2eOldDocument?: boolean }).__e2eOldDocument === true);
}

/** becomeVisible fires the event that a browser fires when the tab of the page becomes visible. */
export async function becomeVisible(page: Page): Promise<void> {
	await page.evaluate(() => {
		if (document.visibilityState !== 'visible') throw new Error('the page is not visible');
		document.dispatchEvent(new Event('visibilitychange'));
	});
}

/**
 * recordRequests records the requests of the main frame in order: each document load as
 * "doc <path>", and each API request as "<method> <path>". Read the returned array at any time.
 */
export function recordRequests(page: Page): string[] {
	const log: string[] = [];
	page.on('request', (request) => {
		const url = new URL(request.url());
		if (request.isNavigationRequest() && request.frame() === page.mainFrame()) {
			log.push(`doc ${url.pathname}${url.search}`);
		} else if (url.pathname.startsWith('/api/')) {
			log.push(`${request.method()} ${url.pathname}`);
		}
	});
	return log;
}

/**
 * abortOnce makes the first matching request fail as a network failure. Later requests pass. The
 * returned function gives the number of aborted requests.
 */
export async function abortOnce(page: Page, method: string, pathname: string): Promise<() => number> {
	let aborted = 0;
	await page.route(
		(url) => url.pathname === pathname,
		async (route) => {
			if (aborted === 0 && route.request().method() === method) {
				aborted += 1;
				await route.abort('failed');
				return;
			}
			await route.fallback();
		}
	);
	return () => aborted;
}

/** The fixed messages of the generated embed pages that the specs assert. */
export const messages = {
	unreachable: 'The server did not answer. Check the connection and try again.',
	signOutUnavailable: 'Sign-out failed. The server is not available. Try again later.',
	signOutServerError: 'Sign-out failed. Something went wrong on the server. Try again later.',
	roleDenied: 'Your role cannot open this page. Try: sign in with an account that has a required role.',
	resetInvalid: 'The reset link is invalid or expired. Request a new link.',
	resetLinkInvalid: 'The reset link is not valid. Request a new link.'
};

/**
 * clientNavigate adds a link to the document and clicks it, so the SvelteKit router handles the
 * navigation without a document load. Use markDocument and documentMarked to prove it.
 */
export async function clientNavigate(page: Page, href: string): Promise<void> {
	await page.evaluate((target) => {
		const link = document.createElement('a');
		link.href = target;
		link.textContent = 'e2e client navigation';
		document.body.append(link);
		link.click();
		link.remove();
	}, href);
}

/**
 * recordFirstRenderURL records location.href at the first element that the app adds to the page,
 * before any test step can run. Call it before the goto. firstRenderURL reads the record.
 */
export async function recordFirstRenderURL(page: Page): Promise<void> {
	await page.addInitScript(() => {
		const w = window as unknown as { __e2eFirstRenderURL?: string };
		const observer = new MutationObserver((records) => {
			for (const record of records) {
				for (const node of record.addedNodes) {
					// The parser adds the static shell. The app adds preload links to the head and its
					// elements to the body. Only an element in the body is visible.
					if (
						node instanceof HTMLElement &&
						node.tagName !== 'SCRIPT' &&
						document.body?.contains(node) &&
						document.readyState !== 'loading'
					) {
						w.__e2eFirstRenderURL ??= location.href;
						observer.disconnect();
						return;
					}
				}
			}
		});
		observer.observe(document, { childList: true, subtree: true });
	});
}

/** firstRenderURL returns the address that recordFirstRenderURL recorded, or null. */
export function firstRenderURL(page: Page): Promise<string | null> {
	return page.evaluate(() => (window as unknown as { __e2eFirstRenderURL?: string }).__e2eFirstRenderURL ?? null);
}
