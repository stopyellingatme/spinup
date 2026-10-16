import { expect, test } from '@playwright/test';
import { register, uniqueEmail } from './helpers';

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
