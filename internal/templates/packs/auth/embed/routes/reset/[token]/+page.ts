import type { PageLoad } from './$types';

// The page is public. The token comes from the address. The page sends it only in the JSON body of
// the confirm call. It never goes in a request address, a log line, or storage.
export const load: PageLoad = ({ params }) => ({ token: params.token });
