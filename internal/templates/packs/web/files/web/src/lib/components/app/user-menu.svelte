<script lang="ts">
	import CircleUserIcon from '@lucide/svelte/icons/circle-user';
	import LogOutIcon from '@lucide/svelte/icons/log-out';
	import UserIcon from '@lucide/svelte/icons/user';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as DropdownMenu from '#lib/components/ui/dropdown-menu/index.js';

	let {
		email
	}: {
		/** The email address of the signed-in user. */
		email: string;
	} = $props();

	let logout: HTMLFormElement | undefined = $state();
</script>

<!-- Signing out needs a POST. The menu item submits this form. -->
<form method="POST" action="/logout" bind:this={logout} hidden></form>

<DropdownMenu.Root>
	<DropdownMenu.Trigger>
		{#snippet child({ props })}
			<Button {...props} variant="ghost" class="max-w-64 gap-2 px-2">
				<CircleUserIcon aria-hidden="true" class="size-5" />
				<span class="sr-only sm:hidden">Account menu</span>
				<span class="hidden truncate sm:inline" data-testid="user-email">{email}</span>
			</Button>
		{/snippet}
	</DropdownMenu.Trigger>
	<DropdownMenu.Content align="end" class="w-56">
		<DropdownMenu.Label class="truncate">Signed in as {email}</DropdownMenu.Label>
		<DropdownMenu.Separator />
		<DropdownMenu.Item>
			{#snippet child({ props })}
				<a {...props} href="/account">
					<UserIcon aria-hidden="true" />
					Account
				</a>
			{/snippet}
		</DropdownMenu.Item>
		<DropdownMenu.Item onSelect={() => logout?.requestSubmit()}>
			<LogOutIcon aria-hidden="true" />
			Sign out
		</DropdownMenu.Item>
	</DropdownMenu.Content>
</DropdownMenu.Root>
