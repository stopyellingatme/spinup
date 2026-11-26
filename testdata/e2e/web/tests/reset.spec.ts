import { createServer, type Server, type Socket } from 'node:net';
import { expect, test, type Page } from '@playwright/test';
import { baseURL, embed, messages, openSignOut, password, register, signIn, uniqueEmail } from './helpers';

// Password reset of embed mode (ADR 0039 section 10.10). The mail link is /reset/confirm#<token>.
// A browser never sends the fragment to a server, so no request URL and no Referer holds the token.
// run.sh sets SMTP_URL of the API to E2E_SMTP_PORT. The sink below receives the mail in this process.

/** A mail that the sink received: the recipients and the decoded message text. */
type Mail = { to: string[]; text: string };

/** decodeQP decodes a quoted-printable body: soft line breaks and =XX escapes. */
function decodeQP(text: string): string {
	return text.replace(/=\r?\n/g, '').replace(/=([0-9A-F]{2})/gi, (_, hex: string) => String.fromCharCode(parseInt(hex, 16)));
}

/**
 * smtpSink listens on the port and answers just enough SMTP for one client without TLS or AUTH.
 * next(to) resolves with the first mail to that address.
 */
async function smtpSink(port: number): Promise<{ next(to: string): Promise<Mail>; close(): Promise<void> }> {
	const mails: Mail[] = [];
	const waiting: { to: string; resolve: (mail: Mail) => void }[] = [];
	const deliver = (mail: Mail) => {
		mails.push(mail);
		for (const w of [...waiting]) {
			if (mail.to.includes(w.to)) {
				waiting.splice(waiting.indexOf(w), 1);
				w.resolve(mail);
			}
		}
	};
	const sockets = new Set<Socket>();
	const server: Server = createServer((socket) => {
		sockets.add(socket);
		socket.on('close', () => sockets.delete(socket));
		let buffer = '';
		let data: string[] | null = null;
		let to: string[] = [];
		socket.write('220 e2e-sink ESMTP\r\n');
		socket.on('data', (chunk) => {
			buffer += chunk.toString('latin1');
			let end: number;
			while ((end = buffer.indexOf('\r\n')) >= 0) {
				const line = buffer.slice(0, end);
				buffer = buffer.slice(end + 2);
				if (data !== null) {
					if (line === '.') {
						deliver({ to, text: decodeQP(data.join('\n')) });
						data = null;
						to = [];
						socket.write('250 OK\r\n');
					} else {
						data.push(line.startsWith('..') ? line.slice(1) : line);
					}
					continue;
				}
				const verb = line.slice(0, 4).toUpperCase();
				if (verb === 'EHLO') socket.write('250-e2e-sink\r\n250 8BITMIME\r\n');
				else if (verb === 'HELO') socket.write('250 e2e-sink\r\n');
				else if (verb === 'RCPT') {
					to.push(line.match(/<([^>]*)>/)?.[1] ?? '');
					socket.write('250 OK\r\n');
				} else if (verb === 'DATA') {
					data = [];
					socket.write('354 End data with <CR><LF>.<CR><LF>\r\n');
				} else if (verb === 'QUIT') {
					socket.end('221 Bye\r\n');
				} else socket.write('250 OK\r\n');
			}
		});
	});
	await new Promise<void>((resolve, reject) => {
		server.once('error', reject);
		server.listen(port, '127.0.0.1', () => resolve());
	});
	return {
		next(address: string) {
			const found = mails.find((mail) => mail.to.includes(address));
			if (found) return Promise.resolve(found);
			return new Promise<Mail>((resolve) => waiting.push({ to: address, resolve }));
		},
		close() {
			for (const socket of sockets) socket.destroy();
			return new Promise<void>((resolve) => server.close(() => resolve()));
		}
	};
}

/** recordURLs records the URL and the Referer of every request of the page. */
function recordURLs(page: Page): string[] {
	const seen: string[] = [];
	page.on('request', (request) => {
		seen.push(request.url());
		const referer = request.headers()['referer'];
		if (referer) seen.push(referer);
	});
	return seen;
}

/** A token of the right form that the API never issued: 32 bytes as unpadded base64url. */
const wrongToken = 'A'.repeat(42) + 'E';

const newPassword = 'a new password that is long';

if (embed) {
	test('embed: the reset link opens with the token in the fragment only, and works one time', async ({ page }) => {
		const port = Number(process.env.E2E_SMTP_PORT);
		expect(port, 'run.sh sets E2E_SMTP_PORT in embed mode').toBeGreaterThan(0);
		const sink = await smtpSink(port);
		try {
			const email = uniqueEmail('reset');
			await register(page, email);
			await (await openSignOut(page, email)).click();
			await expect(page).toHaveURL(`${baseURL}/login`);

			await page.getByRole('link', { name: 'Forgot password?' }).click();
			await expect(page).toHaveURL(`${baseURL}/reset`);
			await page.getByLabel('Email').fill(email);
			await page.getByRole('button', { name: 'Send a reset link' }).click();
			await expect(page.getByText('If an account exists for this email address, you will receive a reset link.')).toBeVisible();

			const mail = await sink.next(email);
			const escaped = baseURL.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
			const match = mail.text.match(new RegExp(`${escaped}/reset/confirm#([A-Za-z0-9_-]+)`));
			expect(match, `a reset link in the mail:\n${mail.text}`).not.toBeNull();
			const link = match![0];
			const token = match![1];

			// Open the link. The page removes the fragment, and no request carries the token.
			const urls = recordURLs(page);
			const confirm = page.waitForRequest((r) => new URL(r.url()).pathname === '/api/auth/reset/confirm');
			await page.goto(link);
			await expect(page.getByRole('heading', { name: 'Choose a new password' })).toBeVisible();
			expect(page.url()).toBe(`${baseURL}/reset/confirm`);
			await page.getByLabel('New password').fill(newPassword);
			await page.getByRole('button', { name: 'Save the new password' }).click();
			expect(((await confirm).postDataJSON() as { token: string }).token).toBe(token);
			await expect(page).toHaveURL(`${baseURL}/login`);
			expect(urls.filter((url) => url.includes(token))).toEqual([]);

			// The new password signs in. The old one does not.
			await signIn(page, email, newPassword);
			await (await openSignOut(page, email)).click();
			await expect(page).toHaveURL(`${baseURL}/login`);
			await page.getByLabel('Email').fill(email);
			await page.getByLabel('Password').fill(password);
			await page.getByRole('button', { name: 'Sign in' }).click();
			await expect(page.getByTestId('form-error')).toBeVisible();
			await expect(page).toHaveURL(`${baseURL}/login`);

			// The link works one time.
			await page.goto(link);
			await expect(page.getByRole('heading', { name: 'Choose a new password' })).toBeVisible();
			expect(page.url()).toBe(`${baseURL}/reset/confirm`);
			await page.getByLabel('New password').fill('another new password here');
			await page.getByRole('button', { name: 'Save the new password' }).click();
			await expect(page.getByText(messages.resetInvalid)).toBeVisible();
			await expect(page).toHaveURL(`${baseURL}/reset/confirm`);
			expect(urls.filter((url) => url.includes(token))).toEqual([]);
		} finally {
			await sink.close();
		}
	});

	test('embed: a reset link without a fragment shows the error and calls no API', async ({ page }) => {
		const urls = recordURLs(page);
		await page.goto('/reset/confirm');
		await expect(page.getByText(messages.resetLinkInvalid)).toBeVisible();
		await expect(page.getByLabel('New password')).toHaveCount(0);
		// The page reads the session (401) and the flags at the start. Wait for a later round trip.
		await page.evaluate(() => fetch('/api/flags').then((r) => r.status));
		expect(urls.filter((url) => url.includes('/api/auth/reset/confirm'))).toEqual([]);
	});

	test('embed: a reset link with a wrong token shows the error of the API', async ({ page }) => {
		const urls = recordURLs(page);
		await page.goto(`/reset/confirm#${wrongToken}`);
		await expect(page.getByRole('heading', { name: 'Choose a new password' })).toBeVisible();
		expect(page.url()).toBe(`${baseURL}/reset/confirm`);
		const confirm = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/auth/reset/confirm');
		await page.getByLabel('New password').fill(newPassword);
		await page.getByRole('button', { name: 'Save the new password' }).click();
		expect((await confirm).status()).toBe(422);
		await expect(page.getByText(messages.resetInvalid)).toBeVisible();
		expect(urls.filter((url) => url.includes(wrongToken))).toEqual([]);
	});
}
