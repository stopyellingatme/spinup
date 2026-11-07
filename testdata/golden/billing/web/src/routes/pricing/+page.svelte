<script lang="ts">
	import PageHeader from '#lib/components/app/page-header.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import type { PageProps } from './$types';

	let { data, form }: PageProps = $props();
	let submitting = $state(false);
	const failed = $derived(form && 'form' in form ? form.form : undefined);

	function amount(value: number, currency: string): string {
		const formatter = new Intl.NumberFormat('en', { style: 'currency', currency });
		const digits = formatter.resolvedOptions().maximumFractionDigits ?? 2;
		return formatter.format(value / 10 ** digits);
	}
</script>

<svelte:head><title>Pricing</title></svelte:head>

<div class="mx-auto grid w-full max-w-5xl gap-6">
	<PageHeader title="Pricing" description="Select a plan. Stripe handles your payment details." />
	{#if failed?.message}<p role="alert" class="text-destructive">{failed.message}</p>{/if}
	{#if failed?.errors.price}<p role="alert" class="text-destructive">{failed.errors.price.join(' ')}</p>{/if}
	<div class="grid gap-6 md:grid-cols-2 xl:grid-cols-3">
		{#each data.plans as plan (plan.name)}
			<Card.Root>
				<Card.Header><Card.Title>{plan.name}</Card.Title></Card.Header>
				<Card.Content class="grid gap-4">
					{#if plan.features.length > 0}
						<ul class="list-inside list-disc text-sm">
							{#each plan.features as feature (feature)}<li>{feature}</li>{/each}
						</ul>
					{/if}
					{#if plan.prices.length === 0}
						<p class="text-muted-foreground text-sm">No paid price is available for this plan.</p>
					{/if}
					{#each plan.prices as price (price.lookup_key)}
						<div class="grid gap-2">
							<p class="font-medium">{price.product_name}</p>
							<p class="text-sm">{amount(price.unit_amount, price.currency)} / {price.interval_count} {price.interval}</p>
							{#if price.usage_type === 'metered'}<p class="text-muted-foreground text-sm">The charge depends on your API key usage.</p>{/if}
							{#if data.user}
								<form method="POST" action="?/checkout" onsubmit={() => { submitting = true; }}>
									<input type="hidden" name="price" value={price.lookup_key} />
									<Button type="submit" disabled={submitting}>Subscribe to {plan.name}</Button>
								</form>
							{:else}
								<Button href="/login?redirectTo=%2Fpricing">Sign in to subscribe</Button>
							{/if}
						</div>
					{/each}
				</Card.Content>
			</Card.Root>
		{/each}
	</div>
	{#if data.user}<a href="/settings/billing" class="text-sm underline">Manage billing</a>{/if}
</div>
