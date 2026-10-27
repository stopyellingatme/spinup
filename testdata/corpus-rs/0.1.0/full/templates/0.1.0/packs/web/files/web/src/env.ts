import { defineEnvVars } from '@sveltejs/kit/env';

// Server environment variables, read when the app starts. Import them from '$app/env/private'.
// PUBLIC_ORIGIN is not here: it is a build input (see vite.config.js).
export const variables = defineEnvVars({
	API_URL: {
		description: 'The base URL where server-side code reaches the API.',
		schema: (value) => (value === undefined || value === '' ? 'http://localhost:8080' : value)
	}
});
