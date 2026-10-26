import createClient from 'openapi-fetch';
import type { paths } from './schema';

/**
 * api calls the API with types from schema.d.ts (`spinup g client` regenerates it).
 *
 * The base URL is empty. In the browser, requests go to the current origin, where the Vite
 * proxy (dev) or Caddy (production) sends /api to the API. On the server, a relative URL does
 * not work (Node's Request needs an absolute one): pass the `fetch` of the load function or
 * action and the public origin, so handleFetch sends the request to API_URL with the cookie:
 *
 *     const { data } = await api.GET('/api/things', { fetch, baseUrl: __PUBLIC_ORIGIN__ });
 */
export const api = createClient<paths>({ baseUrl: '' });
