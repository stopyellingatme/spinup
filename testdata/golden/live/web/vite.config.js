// Managed by spinup: `spinup upgrade` keeps this file current.
// SvelteKit 3 has no svelte.config.js. The kit options live in the sveltekit() plugin.
import adapter from '@sveltejs/adapter-node';
import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig, loadEnv } from 'vite';
import { configDefaults } from 'vitest/config';

// zod 4 calls `new Function('')` once in the browser to test if it can compile fast parsers. The
// CSP below blocks eval, so zod falls back to its normal parsers, but the browser still reports a
// securitypolicyviolation. This plugin sets the `jitless` option of z.config() before zod starts.
// zod then skips the test. It changes only the browser code. The server keeps the fast parsers.
const zodCore = /[\\/]zod[\\/]v4[\\/]core[\\/]core\.js$/;
/** @type {import('vite').Plugin} */
const zodJitless = {
	name: 'spinup:zod-jitless',
	transform(code, id, options) {
		if (options?.ssr || !zodCore.test(id)) return null;
		return { code: `globalThis.__zod_globalConfig ??= { jitless: true };\n${code}`, map: null };
	}
};

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
			zodJitless,
			tailwindcss(),
			sveltekit({
				adapter: adapter(),
				paths: { origin },
				csrf: { trustedOrigins: [origin] },
				// The Content-Security-Policy of each page (ADR 0036 section 2). In 'auto' mode, SvelteKit
				// adds a nonce to script-src for each rendered page, and a hash for a prerendered page.
				// 'self' in connect-src also covers the WebSocket of Vite HMR on the same host and port.
				// Inline styles stay allowed: Svelte transitions and the UI components set style attributes.
				// A page with its own inline script needs nonce="%sveltekit.nonce%".
				csp: {
					mode: 'auto',
					directives: {
						'default-src': ['self'],
						'script-src': ['self'],
						'style-src': ['self', 'unsafe-inline'],
						'img-src': ['self', 'data:'],
						'font-src': ['self', 'data:'],
						'connect-src': ['self'],
						'object-src': ['none'],
						'base-uri': ['self'],
						'form-action': ['self'],
						'frame-ancestors': ['none']
					}
				},
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
		// The dev server bundles zod before the plugins above run, so the bundler needs the plugin too.
		optimizeDeps: {
			rolldownOptions: { plugins: [zodJitless] }
		},
		server: {
			proxy: {
				'/api': { target: apiURL, xfwd: true }
			}
		}
	};
});
