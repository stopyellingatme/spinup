<script lang="ts">
	import CircleUserIcon from '@lucide/svelte/icons/circle-user';
	import LogOutIcon from '@lucide/svelte/icons/log-out';
	import UserIcon from '@lucide/svelte/icons/user';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as DropdownMenu from '#lib/components/ui/dropdown-menu/index.js';
	import { session } from '#lib/session.svelte.js';
	import { signOut } from '#lib/sign-out.js';

	let {
		email,
		links = []
	}: {
		/** The email address of the signed-in user. */
		email: string;
		/** More links under Account, for example the admin pages of a module. */
		links?: { href: string; label: string }[];
	} = $props();

	// signOut calls the Go API. On a failure it shows the reason in a toast, and the item works again.
	// After a success the item stays disabled until the document goes to /login.
</script>

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
		{#each links as link (link.href)}
			<DropdownMenu.Item>
				{#snippet child({ props })}
					<a {...props} href={link.href}>{link.label}</a>
				{/snippet}
			</DropdownMenu.Item>
		{/each}
		<DropdownMenu.Item disabled={session.signingOut} onSelect={() => void signOut()}>
			<LogOutIcon aria-hidden="true" />
			Sign out
		</DropdownMenu.Item>
	</DropdownMenu.Content>
</DropdownMenu.Root>
