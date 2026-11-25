// Managed by spinup: `spinup upgrade` keeps this file current.
// The web server sends its error and warning lines to the log store (ADR 0010 section 4).
import { format } from 'node:util';
import { logs, SeverityNumber } from '@opentelemetry/api-logs';

/** LogLevel is the level of a line that the web server sends to the log store. */
export type LogLevel = 'ERROR' | 'WARN';

const severity: Record<LogLevel, SeverityNumber> = { ERROR: SeverityNumber.ERROR, WARN: SeverityNumber.WARN };

/** emitLog sends one log record. It does nothing when no OpenTelemetry SDK runs. */
export function emitLog(level: LogLevel, message: string): void {
	logs.getLogger('web').emit({ severityNumber: severity[level], severityText: level, body: message });
}

/**
 * captureConsole makes console.error and console.warn also send each line through `emit`.
 * The line still goes to the output of the process. The function that it returns removes the capture.
 */
export function captureConsole(
	emit: (level: LogLevel, message: string) => void = emitLog,
	target: Pick<Console, 'error' | 'warn'> = console
): () => void {
	const original = { error: target.error, warn: target.warn };
	// The SDK can report an export failure through the console. Do not send that line again.
	let busy = false;
	const wrap =
		(level: LogLevel, write: (...args: unknown[]) => void) =>
		(...args: unknown[]): void => {
			write.apply(target, args);
			if (busy) return;
			busy = true;
			try {
				emit(level, format(...args));
			} catch {
				// A failed log export must never break the request.
			} finally {
				busy = false;
			}
		};
	target.error = wrap('ERROR', original.error);
	target.warn = wrap('WARN', original.warn);
	return () => {
		target.error = original.error;
		target.warn = original.warn;
	};
}
