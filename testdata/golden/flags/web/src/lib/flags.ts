// Managed by spinup: `spinup upgrade` keeps this file current.
import type { FlagKey } from '#lib/flags.gen.js';

/** flag reads a public flag from page or layout data. A missing key is off. */
export function flag(data: { flags?: Record<string, boolean> }, key: FlagKey): boolean {
	return data.flags?.[key] === true;
}
