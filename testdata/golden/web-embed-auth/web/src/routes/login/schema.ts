// The zod schemas of the sign-in and register forms in embed mode. They repeat the rules of the Go
// API and of the HTML inputs, so the client and the API agree. The Go API keeps the final say.
import { z } from 'zod';

// The Go API counts the length of a password in Unicode code points. A string length counts UTF-16
// units, so the schemas count code points with the spread operator.
const length = (value: string) => [...value].length;

/** loginSchema checks the sign-in form. */
export const loginSchema = z.object({
	email: z.string().trim().min(1, 'Enter your email address.'),
	password: z
		.string()
		.min(1, 'Enter your password.')
		.refine((value) => length(value) <= 512, 'Use at most 512 characters.')
});

/** registerSchema checks the register form. */
export const registerSchema = z.object({
	email: z.string().trim().min(1, 'Enter your email address.'),
	password: z
		.string()
		.refine((value) => length(value) >= 12, 'Use at least 12 characters.')
		.refine((value) => length(value) <= 512, 'Use at most 512 characters.')
});
