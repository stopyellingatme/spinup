// Managed by spinup: `spinup upgrade` keeps this file current.
import { describe, expect, it } from 'vitest';
import { captureConsole, emitLog, type LogLevel } from './log.js';

describe('captureConsole', () => {
	it('sends each error and warning line, and still prints it', () => {
		const printed: unknown[][] = [];
		const write = (...args: unknown[]): void => void printed.push(args);
		const target = { error: write, warn: write };
		const sent: [LogLevel, string][] = [];
		const restore = captureConsole((level, message) => void sent.push([level, message]), target);

		const failure = new Error('redirect refused');
		target.error('POST /pricing: unknown error:', failure);
		target.warn('session: GET %s returned %d', '/api/session', 503);

		expect(printed).toEqual([
			['POST /pricing: unknown error:', failure],
			['session: GET %s returned %d', '/api/session', 503]
		]);
		expect(sent[0][0]).toBe('ERROR');
		// The line has the stack of the error, as the console prints it.
		expect(sent[0][1]).toContain('POST /pricing: unknown error: Error: redirect refused\n    at ');
		expect(sent[1]).toEqual(['WARN', 'session: GET /api/session returned 503']);

		restore();
		target.error('after');
		expect(sent).toHaveLength(2);
		expect(printed).toHaveLength(3);
	});

	it('does not break the caller or send again when the export fails', () => {
		const printed: unknown[][] = [];
		const write = (...args: unknown[]): void => void printed.push(args);
		const target = { error: write, warn: write };
		let calls = 0;
		captureConsole(() => {
			calls++;
			// An exporter that reports its failure through the console must not start a loop.
			target.error('export failed');
			throw new Error('export failed');
		}, target);
		expect(() => target.error('first')).not.toThrow();
		expect(calls).toBe(1);
		expect(printed).toEqual([['first'], ['export failed']]);
	});
});

describe('emitLog', () => {
	it('does nothing without a started SDK', () => {
		expect(() => emitLog('ERROR', 'no SDK')).not.toThrow();
	});
});
