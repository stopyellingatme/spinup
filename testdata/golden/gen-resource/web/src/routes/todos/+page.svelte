<script lang="ts">
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();
</script>

<svelte:head>
	<title>Todos</title>
</svelte:head>

<Card.Root>
	<Card.Header>
		<Card.Title>Todos</Card.Title>
		<Card.Description>{data.total} in total.</Card.Description>
		<Card.Action>
			<Button href="/todos/new">New todo</Button>
		</Card.Action>
	</Card.Header>
	<Card.Content class="overflow-x-auto">
		{#if data.items.length === 0}
			<p class="text-muted-foreground text-sm">No todos yet.</p>
		{:else}
			<table class="w-full text-left text-sm">
				<thead class="text-muted-foreground border-b">
					<tr>
						<th class="py-2 pr-4 font-medium">ID</th>
						<th class="py-2 pr-4 font-medium">title</th>
						<th class="py-2 pr-4 font-medium">done</th>
						<th class="py-2 pr-4 font-medium">due</th>
						<th class="py-2 pr-4 font-medium">priority</th>
					</tr>
				</thead>
				<tbody>
					{#each data.items as item (item.id)}
						<tr class="border-b last:border-0">
							<td class="py-2 pr-4"><a class="font-medium hover:underline" href="/todos/{item.id}">{item.id}</a></td>
							<td class="max-w-xs truncate py-2 pr-4">{item.title}</td>
							<td class="max-w-xs truncate py-2 pr-4">{item.done ? 'Yes' : 'No'}</td>
							<td class="max-w-xs truncate py-2 pr-4">{item.due ?? ''}</td>
							<td class="max-w-xs truncate py-2 pr-4">{item.priority}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		{/if}
	</Card.Content>
</Card.Root>
