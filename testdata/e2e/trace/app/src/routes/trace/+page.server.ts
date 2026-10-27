import { API_URL } from '$app/env/private';
import type { PageServerLoad } from './$types';

// The trace e2e test (testdata/e2e/trace/run.sh) copies this route into a generated app.
// The server load calls the API (Go or Rust), so one request makes one trace that holds both services.
export const load: PageServerLoad = async ({ fetch }) => {
	const res = await fetch(`${API_URL.replace(/\/+$/, '')}/readyz`);
	return { apiStatus: res.status };
};
