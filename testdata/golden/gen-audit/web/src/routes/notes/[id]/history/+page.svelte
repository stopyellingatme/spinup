<script lang="ts">
	import HistoryList from '#lib/components/app/history-list.svelte';
	import PageHeader from '#lib/components/app/page-header.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();
</script>

<svelte:head>
	<title>History of note #{data.id}</title>
</svelte:head>

<div class="space-y-6">
	<PageHeader title="History of note #{data.id}" />
	<HistoryList events={data.events} />
	<nav aria-label="History pages" class="flex gap-4">
		<a href="/notes/{data.id}/history">Newest events</a>
		{#if data.nextBeforeID !== null}
			<a href="/notes/{data.id}/history?before_id={data.nextBeforeID}">Older events</a>
		{/if}
	</nav>
</div>
