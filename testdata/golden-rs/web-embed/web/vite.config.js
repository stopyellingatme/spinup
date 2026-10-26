// Managed by spinup: `spinup upgrade` keeps this file current.
// SvelteKit 3 has no svelte.config.js. The kit options live in the sveltekit() plugin.
import { cp, mkdir, rm } from 'node:fs/promises';
import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig, loadEnv } from 'vite';
import { configDefaults } from 'vitest/config';

export default defineConfig(({ mode }) => {
	// The process environment wins over web/.env files.
	const env = loadEnv(mode, '.', '');

	// Vite keeps the same proxy and public origin during development.
	const origin = env.PUBLIC_ORIGIN || 'http://localhost:5173';

	// API_URL is where the Vite dev server sends /api. The browser stays on one origin, so the
	// session cookie of the API is a first-party cookie.
	const apiURL = env.API_URL || 'http://localhost:8080';

	return {
		plugins: [
			tailwindcss(),
			sveltekit({
				adapter: {
					...adapter({ fallback: 'index.html' }),
					async adapt(builder) {
						await adapter({ fallback: 'index.html' }).adapt(builder);
						// Replace the generated assets so old hashes cannot survive a build.
						const dist = '../api/webdist/';
						await rm(dist, { recursive: true, force: true });
						await mkdir(dist, { recursive: true });
						await cp('build', dist, { recursive: true });
					}
				},
				paths: { origin },
				csrf: { trustedOrigins: [origin] },
				tracing: { server: false }
			})
		],
		test: {
			// A fresh app has no test files. Do not fail on that.
			passWithNoTests: true,
			exclude: ['e2e/**', ...configDefaults.exclude]
		},
		define: {
			__PUBLIC_ORIGIN__: JSON.stringify(origin)
		},
		server: {
			proxy: {
				'/api': { target: apiURL, xfwd: true }
			}
		}
	};
});
