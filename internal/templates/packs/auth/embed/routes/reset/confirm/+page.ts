import type { PageLoad } from './$types';

// The page is public. The token is not here: it is in the fragment of the address, which the
// router does not pass to a load. The page reads it once, with takeResetToken.
export const load: PageLoad = () => ({});
