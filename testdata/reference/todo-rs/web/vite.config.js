// Managed by spinup: `spinup upgrade` keeps this file current.
// SvelteKit 3 has no svelte.config.js. The kit options live in the sveltekit() plugin.
import adapter from '@sveltejs/adapter-node';
import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig, loadEnv } from 'vite';
import { configDefaults } from 'vitest/config';

export default defineConfig(({ mode }) => {
	// The process environment wins over web/.env files.
	const env = loadEnv(mode, '.', '');

	// PUBLIC_ORIGIN is the one source of the public URL. adapter-node does not read ORIGIN at
	// run time: the build bakes this value into paths.origin, the CSRF check, and handleFetch.
	const origin = env.PUBLIC_ORIGIN || 'http://localhost:5173';

	// API_URL is where the Vite dev server sends /api. The browser stays on one origin, so the
	// session cookie of the Go API is a first-party cookie.
	const apiURL = env.API_URL || 'http://localhost:8080';

	return {
		plugins: [
			tailwindcss(),
			sveltekit({
				adapter: adapter(),
				paths: { origin },
				csrf: { trustedOrigins: [origin] },
				// Spans for handle, load, and form actions. They go nowhere until
				// src/instrumentation.server.ts starts the SDK (OTEL_EXPORTER_OTLP_ENDPOINT).
				// SvelteKit 3 runs that file without an opt-in: experimental.instrumentation is gone.
				// OTEL_TRACES_SAMPLER and OTEL_TRACES_SAMPLER_ARG set the sampling ratio at run time.
				tracing: { server: true }
			})
		],
		test: {
			// A fresh app without the auth module has no test files. Do not fail on that.
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
