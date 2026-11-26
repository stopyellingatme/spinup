import { takeResetToken } from '#lib/reset-token.js';
import type { PageLoad } from './$types';

// The page is public. The mail link is /reset/confirm#<token>. The browser never sends the fragment
// to a server, and the router does not pass it to a load. takeResetToken reads it, removes it from
// the address bar, and keeps it in memory. The load runs also when the root layout hides the page
// behind its session error card, and a later run gets the same token.
export const load: PageLoad = () => ({ token: takeResetToken() });
