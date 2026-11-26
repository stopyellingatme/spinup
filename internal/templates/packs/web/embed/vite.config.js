// Managed by spinup: `spinup upgrade` keeps this file current.
// SvelteKit 3 has no svelte.config.js. The kit options live in the sveltekit() plugin.
import { createHash } from 'node:crypto';
import { existsSync, readFileSync, readdirSync } from 'node:fs';
import { cp, mkdir, rm } from 'node:fs/promises';
import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig, loadEnv } from 'vite';
import { configDefaults } from 'vitest/config';

// zod 4 calls `new Function('')` once in the browser to test if it can compile fast parsers. The
// Content-Security-Policy of the Go binary blocks eval, so zod falls back to its normal parsers, but
// the browser still reports a securitypolicyviolation. This plugin sets the `jitless` option of
// z.config() before zod starts. zod then skips the test. It changes only the browser code.
const zodCore = /[\\/]zod[\\/]v4[\\/]core[\\/]core\.js$/;
/** @type {import('vite').Plugin} */
const zodJitless = {
	name: 'spinup:zod-jitless',
	transform(code, id, options) {
		if (options?.ssr || !zodCore.test(id)) return null;
		return { code: `globalThis.__zod_globalConfig ??= { jitless: true };\n${code}`, map: null };
	}
};

// buildVersion gives the version name of the build: a hash of the build inputs. The default of
// SvelteKit is the build time. That value is in index.html, so each build would change the script
// hash of the Content-Security-Policy, the ETag of the shell, and the Go binary. With this name,
// two builds of the same inputs give the same files, and a changed input gives a new name.
/** @param {Record<string, string>} publicEnv */
function buildVersion(publicEnv) {
	const hash = createHash('sha256');
	/** @type {(name: string, content: Uint8Array) => void} */
	const add = (name, content) => {
		hash.update(`${name}\0${content.length}\0`).update(content);
	};
	/** @type {(dir: string) => void} */
	const walk = (dir) => {
		const entries = readdirSync(dir, { withFileTypes: true });
		for (const entry of entries.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0))) {
			const path = `${dir}/${entry.name}`;
			if (entry.isDirectory()) walk(path);
			else if (entry.isFile()) add(path, readFileSync(path));
		}
	};
	for (const dir of ['src', 'static']) {
		if (existsSync(dir)) walk(dir);
	}
	for (const file of ['package.json', 'pnpm-lock.yaml', 'bun.lock', 'vite.config.js']) {
		if (existsSync(file)) add(file, readFileSync(file));
	}
	// A PUBLIC_ value is part of the client code.
	for (const key of Object.keys(publicEnv).sort()) {
		add(key, Buffer.from(publicEnv[key]));
	}
	return hash.digest('hex').slice(0, 16);
}

export default defineConfig(({ mode }) => {
	// The process environment wins over web/.env files.
	const env = loadEnv(mode, '.', '');

	// Vite keeps the same proxy and public origin during development.
	const origin = env.PUBLIC_ORIGIN || 'http://localhost:5173';

	// API_URL is where the Vite dev server sends /api. The browser stays on one origin, so the
	// session cookie of the Go API is a first-party cookie.
	const apiURL = env.API_URL || 'http://localhost:8080';

	const staticAdapter = adapter({ fallback: 'index.html', precompress: true });

	const publicEnv = Object.fromEntries(Object.entries(env).filter(([key]) => key.startsWith('PUBLIC_')));

	return {
		plugins: [
			zodJitless,
			tailwindcss(),
			sveltekit({
				adapter: {
					...staticAdapter,
					async adapt(builder) {
						// precompress writes a .br and a .gz sibling for each file. The Go binary serves them.
						await staticAdapter.adapt(builder);
						// Replace the generated assets so old hashes cannot survive a build.
						const dist = '../api/internal/webdist/dist';
						await rm(dist, { recursive: true, force: true });
						await mkdir(dist, { recursive: true });
						await cp('build', dist, { recursive: true });
					}
				},
				paths: { origin },
				csrf: { trustedOrigins: [origin] },
				version: { name: buildVersion({ ...publicEnv, PUBLIC_ORIGIN: origin }) },
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
