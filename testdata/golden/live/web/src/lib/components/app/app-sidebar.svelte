<script lang="ts">
	import BookOpenIcon from '@lucide/svelte/icons/book-open';
	import ExternalLinkIcon from '@lucide/svelte/icons/external-link';
	import { cn } from '#lib/utils.js';
	import type { NavItem } from '#lib/nav.gen.js';

	let {
		items,
		pathname,
		class: className
	}: {
		/** The links, from nav.gen.ts. */
		items: NavItem[];
		/** The current path. It marks the active link. */
		pathname: string;
		class?: string;
	} = $props();

	/** A link is active on its own path and on every path below it. */
	function active(href: string): boolean {
		return pathname === href || (href !== '/' && pathname.startsWith(href + '/'));
	}

	const link =
		'flex h-9 items-center gap-2 rounded-md px-3 text-sm font-medium text-sidebar-foreground/80 outline-none transition-colors hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:ring-2 focus-visible:ring-sidebar-ring [&_svg]:size-4 [&_svg]:shrink-0';
</script>

<div class={cn('flex h-full flex-col', className)}>
	<nav aria-label="Main" class="flex-1 overflow-y-auto p-3">
		{#if items.length > 0}
			<ul class="grid gap-1">
				{#each items as item (item.href)}
					<li>
						<a
							href={item.href}
							aria-current={active(item.href) ? 'page' : undefined}
							class={cn(link, 'aria-[current=page]:bg-sidebar-accent aria-[current=page]:text-sidebar-accent-foreground')}
						>
							{item.label}
						</a>
					</li>
				{/each}
			</ul>
		{:else}
			<p class="text-muted-foreground px-3 py-2 text-sm">
				Run <code class="text-foreground font-mono text-xs">spinup g resource</code> to add pages.
			</p>
		{/if}
	</nav>
	<div class="border-sidebar-border border-t p-3">
		<a href="/api/docs" data-sveltekit-reload class={link}>
			<BookOpenIcon aria-hidden="true" />
			<span class="flex-1">API docs</span>
			<ExternalLinkIcon aria-hidden="true" class="text-muted-foreground" />
		</a>
	</div>
</div>
