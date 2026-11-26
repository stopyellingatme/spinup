// The zod schema of the new password form in embed mode. The token is not a form field: the page
// reads it from the fragment of the address, and it goes only in the JSON body of the confirm call.
import { z } from 'zod';

// The Go API counts the length of a password in Unicode code points. A string length counts UTF-16
// units, so the schema counts code points with the spread operator.
const length = (value: string) => [...value].length;

/** resetConfirmSchema checks the new password. The Go API keeps the final say. */
export const resetConfirmSchema = z.object({
	password: z
		.string()
		.refine((value) => length(value) >= 12, 'Use at least 12 characters.')
		.refine((value) => length(value) <= 512, 'Use at most 512 characters.')
});
