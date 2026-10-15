<script lang="ts">
	import { onMount } from 'svelte';

	type Provider = { name: string; label: string };
	let providers = $state<Provider[]>([]);
	let failed = $state(false);

	onMount(() => {
		const controller = new AbortController();
		void fetch('/api/auth/oidc/providers', { signal: controller.signal })
			.then(async (response) => {
				if (!response.ok) throw new Error('Cannot load sign-in providers.');
				providers = await response.json();
			})
			.catch(() => {
				if (!controller.signal.aborted) failed = true;
			});
		return () => controller.abort();
	});
</script>

{#if providers.length > 0}
	<div class="mt-4 grid gap-2" aria-label="OpenID Connect sign-in">
		{#each providers as provider (provider.name)}
			<a
				class="rounded-md border px-4 py-2 text-center text-sm font-medium hover:bg-muted focus-visible:outline-2"
				href={`/api/auth/oidc/${encodeURIComponent(provider.name)}/start`}
			>
				Sign in with {provider.label}
			</a>
		{/each}
	</div>
{/if}
{#if failed}
	<p role="alert" class="mt-4 text-sm">Cannot load sign-in providers. Refresh the page to try again.</p>
{/if}
