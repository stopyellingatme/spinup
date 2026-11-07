import { API_URL } from '$app/env/private';
import type { RequestHandler } from './$types';

// Managed by spinup. This route lets an MCP client find API metadata through the web frontend.
export const GET: RequestHandler = async ({ params }) => {
	const api = API_URL.replace(/\/+$/, '');
	const response = await globalThis.fetch(`${api}/.well-known/oauth-protected-resource/${params.path}`, {
		headers: { accept: 'application/json' }
	});
	const contentType = response.headers.get('content-type');
	return new Response(response.body, {
		status: response.status,
		headers: contentType ? { 'Content-Type': contentType } : undefined
	});
};
