import createClient from 'openapi-fetch';
import type { paths } from './schema';

/**
 * api calls the Go API with types from schema.d.ts (`spinup g client` regenerates it).
 *
 * The base URL is empty. In the browser, requests go to the current origin, where the Vite
 * proxy (dev) or Caddy (production) sends /api to Go. On the server, pass the `fetch` of the
 * load function or action, so handleFetch sends the request to API_URL with the cookie:
 *
 *     const { data } = await api.GET('/api/things', { fetch });
 */
export const api = createClient<paths>({ baseUrl: '' });
