<script lang="ts">
	import '../app.css';
	import { onMount } from 'svelte';
	import { onNavigate } from '$app/navigation';
	import { page } from '$app/state';
	import AppSidebar from '#lib/components/app/app-sidebar.svelte';
	import TopBar from '#lib/components/app/top-bar.svelte';
	import UserMenu from '#lib/components/app/user-menu.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import { adminRole } from '#lib/flags.gen.js';
	import { Toaster } from '#lib/components/ui/sonner/index.js';
	import { nav } from '#lib/nav.gen.js';
	import { session } from '#lib/session.svelte.js';
	import { flushSessionCheck, recheckSession, watchSession } from '#lib/session-watch.js';
	import type { LayoutProps } from './$types';

	let { data, children }: LayoutProps = $props();

	// The watch starts once, after the first session read. Its teardown closes the channel.
	onMount(watchSession);
	// A session check waits while a navigation runs. Run it when the navigation ends, also when the
	// navigation fails. The callback returns nothing, so the navigation does not wait for it.
	onNavigate((navigation) => {
		navigation.complete.finally(flushSessionCheck).catch(() => {});
	});

	// Hide the pages while the document goes to another identity, and while the session is not
	// verified. Signed-out visitors get no sidebar. The sign-in pages stay centered.
	const verified = $derived(!session.changingUser && data.sessionError === null);
	const shell = $derived(verified && data.user !== null);
</script>

<svelte:head>
	<title>todo</title>
</svelte:head>

<a
	href="#main"
	class="bg-background focus-visible:ring-ring sr-only z-50 rounded-md px-3 py-2 text-sm font-medium focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus-visible:ring-2"
>
	Skip to content
</a>

{#if !session.changingUser}
	<div class="flex min-h-svh flex-col">
		<TopBar appName="todo" items={nav} pathname={page.url.pathname} menu={shell}>
			{#snippet actions()}
				{#if shell && data.user}
					<UserMenu
						email={data.user.email}
						links={data.user.role === adminRole ? [{ href: '/admin/flags', label: 'Feature flags' }] : []}
					/>
				{:else if verified}
					<Button href="/login" variant="ghost" size="sm">Sign in</Button>
				{/if}
			{/snippet}
		</TopBar>
		<div class="flex flex-1">
			{#if shell}
				<aside class="bg-sidebar text-sidebar-foreground border-sidebar-border hidden w-60 shrink-0 border-r md:block">
					<AppSidebar items={nav} pathname={page.url.pathname} class="sticky top-14 h-[calc(100svh-3.5rem)]" />
				</aside>
			{/if}
			<main
				id="main"
				tabindex="-1"
				class={['min-w-0 flex-1 outline-none', !shell && 'flex flex-col items-center justify-center']}
			>
				<div class="mx-auto w-full max-w-5xl px-4 py-8 sm:px-6 lg:px-8">
					{#if data.sessionError !== null}
						<Card.Root class="mx-auto w-full max-w-lg" role="alert" data-testid="session-error">
							<Card.Header>
								<Card.Title>
									<h1 class="text-lg font-semibold tracking-tight">The session cannot be checked</h1>
								</Card.Title>
								<Card.Description>{data.sessionError}</Card.Description>
							</Card.Header>
							<Card.Footer>
								<Button onclick={recheckSession}>Try again</Button>
							</Card.Footer>
						</Card.Root>
					{:else}
						{@render children()}
					{/if}
				</div>
			</main>
		</div>
	</div>
{:else}
	<p role="status" class="sr-only">Loading</p>
{/if}

<Toaster richColors closeButton />
