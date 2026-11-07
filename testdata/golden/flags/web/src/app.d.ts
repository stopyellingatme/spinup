// See https://svelte.dev/docs/kit/types#app.d.ts for information about these interfaces.

declare global {
	/** The public origin from vite.config.js (PUBLIC_ORIGIN), the same string as paths.origin. */
	const __PUBLIC_ORIGIN__: string;

	namespace App {
		/** The signed-in user, as `GET /api/session` returns it. */
		interface User {
			id: number;
			email: string;
			role: string;
		}
		interface Locals {
			user: User | null;
			/** The public flags of this request, as GET /api/flags returns them. hooks.server.ts always sets them. */
			flags?: Record<string, boolean>;
		}
		// interface Error {}
		// interface PageData {}
		// interface PageState {}
		// interface Platform {}
	}
}

export {};
