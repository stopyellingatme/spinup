import type { LayoutServerLoad } from './$types';

// hooks.server.ts sets locals.user and locals.flags. Every page gets them as data.user and data.flags.
export const load: LayoutServerLoad = ({ locals }) => ({ user: locals.user, flags: locals.flags });
