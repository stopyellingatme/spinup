// OpenTelemetry for the SvelteKit server (ADR 0010 §4). SvelteKit 3 builds this file into the
// server output and runs it before any application code. The Docker image also loads it with
// `node --import`, so the SDK starts before the first application module.
//
// Without OTEL_EXPORTER_OTLP_ENDPOINT this file does nothing. With it, the SDK sends traces over
// OTLP/HTTP (the exporter appends /v1/traces). It also sends each console.error and console.warn
// line as a log record (/v1/logs), so an error of the web server is in the log store with the
// logs of the API. The standard variables apply:
//   OTEL_SERVICE_NAME          the service name (default: the package name, `<app>-web`)
//   OTEL_RESOURCE_ATTRIBUTES   extra resource attributes
//   OTEL_TRACES_SAMPLER        the sampler (default: parentbased_always_on)
//   OTEL_TRACES_SAMPLER_ARG    the sampling ratio for the traceidratio samplers (0 to 1)
import { name as packageName } from '../package.json';

const endpoint = process.env.OTEL_EXPORTER_OTLP_ENDPOINT?.trim();

if (endpoint) {
	const { OTLPLogExporter } = await import('@opentelemetry/exporter-logs-otlp-http');
	const { OTLPTraceExporter } = await import('@opentelemetry/exporter-trace-otlp-http');
	const { UndiciInstrumentation } = await import('@opentelemetry/instrumentation-undici');
	const { logs, NodeSDK } = await import('@opentelemetry/sdk-node');
	const { captureConsole } = await import('./lib/server/log.js');
	const sdk = new NodeSDK({
		serviceName: process.env.OTEL_SERVICE_NAME?.trim() || packageName,
		traceExporter: new OTLPTraceExporter(),
		// Node's fetch is undici. Without this instrumentation, a fetch to the Go API carries no
		// traceparent header, and the Go spans start a new trace (ADR 0002 rule 8).
		instrumentations: [new UndiciInstrumentation()],
		// No metrics: the Go API sends them. The logs are the error and warning lines only.
		metricReaders: [],
		logRecordProcessors: [new logs.BatchLogRecordProcessor({ exporter: new OTLPLogExporter() })]
	});
	sdk.start();
	captureConsole();

	// Flush the buffered spans before the process exits. On SIGTERM (or SIGINT), adapter-node
	// stops new connections, waits for the open requests, and then emits `sveltekit:shutdown`.
	// The SDK shuts down at that point, so the spans of the last requests are sent too.
	process.once('sveltekit:shutdown', () => {
		sdk.shutdown().catch((err: unknown) => {
			console.error('telemetry: cannot flush the spans and the logs at shutdown:', err);
		});
	});
}
