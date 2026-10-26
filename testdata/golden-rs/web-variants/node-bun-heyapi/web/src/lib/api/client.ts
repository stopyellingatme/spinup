import type { Client } from 'openapi-fetch';
import type { paths } from './schema';
import { client } from './client/client.gen';

// The generated client is also available for native heyapi SDK calls.
export { client } from './client/client.gen';

type Options = Omit<RequestInit, 'body'> & {
	baseUrl?: string;
	body?: unknown;
	fetch?: typeof fetch;
	params?: { path?: Record<string, unknown>; query?: Record<string, unknown> };
};

// Keep resource pages compatible with their typed GET/POST interface.
// Each request uses heyapi, including SvelteKit's fetch for cookies and handleFetch.
function request(method: 'GET' | 'POST' | 'PUT' | 'DELETE' | 'PATCH' | 'HEAD' | 'OPTIONS' | 'TRACE') {
	return async (url: string, { params, baseUrl, body, headers, ...options }: Options = {}) => {
		const requestHeaders = new Headers(headers);
		if (body !== undefined && !requestHeaders.has('Content-Type')) {
			requestHeaders.set('Content-Type', 'application/json');
		}
		const result = await client.request({
			...options,
			method,
			url,
			baseUrl: baseUrl ?? (typeof window === 'undefined' ? __PUBLIC_ORIGIN__ : window.location.origin),
			path: params?.path,
			query: params?.query,
			body,
			bodySerializer: JSON.stringify,
			headers: requestHeaders,
			responseStyle: 'fields',
			throwOnError: false
		});
		// Match the existing interface: transport failures throw, HTTP errors return a response.
		if (!result.response) throw result.error;
		return result;
	};
}

export const api = {
	GET: request('GET'), POST: request('POST'), PUT: request('PUT'), DELETE: request('DELETE'),
	PATCH: request('PATCH'), HEAD: request('HEAD'), OPTIONS: request('OPTIONS'), TRACE: request('TRACE')
} as Pick<Client<paths>, 'GET' | 'POST' | 'PUT' | 'DELETE' | 'PATCH' | 'HEAD' | 'OPTIONS' | 'TRACE'>;
