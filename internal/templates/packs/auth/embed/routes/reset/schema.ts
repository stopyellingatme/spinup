// The zod schema of the reset request form in embed mode. The Go API gives the same answer for a
// known and an unknown address, so the schema checks only that the field is not empty.
import { z } from 'zod';

/** resetRequestSchema checks the reset request form. */
export const resetRequestSchema = z.object({
	email: z.string().trim().min(1, 'Enter your email address.')
});
