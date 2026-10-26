<script lang="ts">
	import type { Snippet } from 'svelte';
	import { afterNavigate } from '$app/navigation';
	import MenuIcon from '@lucide/svelte/icons/menu';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Sheet from '#lib/components/ui/sheet/index.js';
	import type { NavItem } from '#lib/nav.gen.js';
	import AppSidebar from './app-sidebar.svelte';

	let {
		appName,
		items,
		pathname,
		menu = true,
		actions
	}: {
		/** The name in the top bar. */
		appName: string;
		/** The sidebar links. Small screens show them in a sheet. */
		items: NavItem[];
		/** The current path. It marks the active link. */
		pathname: string;
		/** Show the menu button on small screens. Turn it off when the page has no sidebar. */
		menu?: boolean;
		/** The controls at the right end, for example the user menu. */
		actions?: Snippet;
	} = $props();

	let open = $state(false);

	// Close the sheet after each navigation, also when the link is the current page.
	afterNavigate(() => {
		open = false;
	});
</script>

<header class="bg-background/95 supports-[backdrop-filter]:bg-background/80 sticky top-0 z-40 border-b backdrop-blur">
	<div class="flex h-14 items-center gap-2 px-4 sm:px-6">
		{#if menu}
			<Sheet.Root bind:open>
				<Sheet.Trigger>
					{#snippet child({ props })}
						<Button {...props} variant="ghost" size="icon" class="-ml-2 md:hidden" aria-label="Open the menu">
							<MenuIcon aria-hidden="true" />
						</Button>
					{/snippet}
				</Sheet.Trigger>
				<Sheet.Content side="left" class="bg-sidebar text-sidebar-foreground w-72 gap-0 p-0">
					<Sheet.Header class="border-sidebar-border h-14 justify-center border-b px-4 py-0">
						<Sheet.Title>{appName}</Sheet.Title>
						<Sheet.Description class="sr-only">Pages of {appName}</Sheet.Description>
					</Sheet.Header>
					<AppSidebar {items} {pathname} class="min-h-0 flex-1" />
				</Sheet.Content>
			</Sheet.Root>
		{/if}
		<a
			href="/"
			class="focus-visible:ring-ring rounded-md px-1 font-semibold tracking-tight outline-none focus-visible:ring-2"
		>
			{appName}
		</a>
		{#if actions}
			<div class="ml-auto flex items-center gap-2">
				{@render actions()}
			</div>
		{/if}
	</div>
</header>
