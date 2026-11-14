<script lang="ts">
	import { enhance } from '$app/forms';
	import PageHeader from '#lib/components/app/page-header.svelte';
	import { Badge } from '#lib/components/ui/badge/index.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import type { PageProps, SubmitFunction } from './$types';

	let { data, form }: PageProps = $props();
	let submitting = $state(false);
	const billing = $derived(data.billing);
	const failed = $derived(form && 'form' in form ? form.form : undefined);
	// The server action answers with a redirect to Stripe, which is another origin. The CSP has
	// form-action 'self', and a browser blocks a plain form POST that redirects to another origin.
	// The enhanced form sends the POST with fetch, and this callback then opens the Stripe page.
	const toStripe: SubmitFunction = () => {
		submitting = true;
		return async ({ result, update }) => {
			if (result.type === 'redirect' && /^https?:/.test(result.location)) {
				window.location.assign(result.location);
				return;
			}
			submitting = false;
			await update();
		};
	};
</script>

<svelte:head><title>Billing</title></svelte:head>
<svelte:window onpageshow={(event) => { if (event.persisted) submitting = false; }} />

<div class="mx-auto grid w-full max-w-3xl gap-6">
	<PageHeader title="Billing" description="View your plan and manage your subscription with Stripe." />
	{#if failed?.message}<p role="alert" class="text-destructive">{failed.message}</p>{/if}
	<Card.Root>
		<Card.Header><Card.Title>Current plan</Card.Title></Card.Header>
		<Card.Content class="grid gap-4">
			<div class="flex items-center gap-3">
				<p class="text-xl font-semibold">{billing.plan}</p>
				{#if billing.status}<Badge variant="secondary">{billing.status}</Badge>{/if}
			</div>
			{#if billing.current_period_end}
				<p class="text-sm">The current period ends on {billing.current_period_end.slice(0, 10)} (UTC).</p>
			{/if}
			{#if billing.cancel_at_period_end}
				<p class="text-sm">Your subscription will cancel at the end of this period.</p>
			{/if}
			{#if billing.features.length > 0}
				<ul class="list-inside list-disc text-sm">
					{#each billing.features as feature (feature)}<li>{feature}</li>{/each}
				</ul>
			{/if}
			<div class="flex flex-wrap gap-3">
				<Button href="/pricing" variant="outline">Upgrade</Button>
				{#if billing.has_customer}
					<form method="POST" action="?/portal" use:enhance={toStripe}>
						<Button type="submit" disabled={submitting}>Manage billing</Button>
					</form>
				{/if}
			</div>
		</Card.Content>
	</Card.Root>
</div>
