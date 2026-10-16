import { expect, test } from '@playwright/test';
import { apiURL } from './helpers';

test('the API reference is served with a strict script policy', async ({ request, page }) => {
	const docs = await request.get(`${apiURL}/api/docs`);
	expect(docs.status()).toBe(200);
	expect(docs.headers()['content-security-policy']).toContain("script-src 'self'");

	const spec = await request.get(`${apiURL}/api/openapi.yaml`);
	expect(spec.status()).toBe(200);
	expect(await spec.text()).toContain('/api/todos');

	await page.goto(`${apiURL}/api/docs`);
	await expect(page.getByText('todos', { exact: false }).first()).toBeVisible({ timeout: 15_000 });
});
