<script lang="ts" module>
	/** One change in the audit response. Redacted changes contain no values. */
	export type AuditChange = { old?: unknown; new?: unknown; redacted?: boolean };

	/** One event from the history endpoint. */
	export type AuditEvent = {
		id: number;
		occurred_at: string;
		action: string;
		actor: { email?: string; role?: string; kind?: string };
		changes: Record<string, AuditChange>;
	};
</script>

<script lang="ts">
	import * as Pagination from '#lib/components/ui/pagination/index.js';
	import { buttonVariants } from '#lib/components/ui/button/index.js';

	let {
		events,
		previousHref,
		nextHref
	}: {
		events: AuditEvent[];
		/** A server link to the previous page. */
		previousHref?: string;
		/** A server link to the next page. */
		nextHref?: string;
	} = $props();

	function value(input: unknown): string {
		if (input === undefined) return '—';
		if (input === null) return 'null';
		if (typeof input === 'string') return input;
		return JSON.stringify(input);
	}
</script>

{#if events.length === 0}
	<p class="text-muted-foreground py-8 text-center">No history yet.</p>
{:else}
	<ol aria-label="History" class="divide-y">
		{#each events as event (event.id)}
			<li class="space-y-2 py-4 break-words">
				<div class="flex flex-wrap gap-x-3 gap-y-1 text-sm">
					<time datetime={event.occurred_at}>{event.occurred_at}</time>
					<span>{event.actor.email || event.actor.kind || 'unattributed'}{#if event.actor.role} ({event.actor.role}){/if}</span>
					<span class="text-muted-foreground">{event.action}</span>
				</div>
				<ul class="space-y-1 font-mono text-sm">
					{#each Object.entries(event.changes).sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0) as [field, change] (field)}
						<li>
							{field}: {#if change.redacted}changed (value hidden){:else}{value(change.old)} -&gt; {value(change.new)}{/if}
						</li>
					{/each}
				</ul>
			</li>
		{/each}
	</ol>
{/if}

{#if previousHref || nextHref}
	<nav aria-label="History pages" class="mt-4 print:hidden">
		<Pagination.Content class="justify-between">
			{#if previousHref}
				<Pagination.Item>
					<a href={previousHref} rel="prev" class={buttonVariants({ variant: 'outline' })}>Previous</a>
				</Pagination.Item>
			{/if}
			{#if nextHref}
				<Pagination.Item>
					<a href={nextHref} rel="next" class={buttonVariants({ variant: 'outline' })}>Next</a>
				</Pagination.Item>
			{/if}
		</Pagination.Content>
	</nav>
{/if}
