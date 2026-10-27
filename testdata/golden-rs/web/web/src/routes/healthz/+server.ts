import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';

// Managed by spinup. The container HEALTHCHECK and the deploy smoke test call this route.
export const GET: RequestHandler = () => json({ status: 'ok' });
