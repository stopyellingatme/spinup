<script lang="ts">
	import { enhance } from '$app/forms';
	import { afterNavigate, goto } from '$app/navigation';
	import { page } from '$app/state';
	import { toast } from 'svelte-sonner';
	import PageHeader from '#lib/components/app/page-header.svelte';
	import { Badge } from '#lib/components/ui/badge/index.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import * as Table from '#lib/components/ui/table/index.js';
	import type { PageProps } from './$types';

	let { data, form }: PageProps = $props();

	// Show the toast of a redirect once, then remove its flag from the address bar.
	afterNavigate(({ to }) => {
		if (!to?.url.searchParams.has('saved')) return;
		toast.success('Flag saved.');
		const url = new URL(to.url);
		url.searchParams.delete('saved');
		void goto(url, { replace: true, shallow: true, state: page.state });
	});

	const live = $derived(data.admin.flags.filter((f) => !f.stale));
	const stale = $derived(data.admin.flags.filter((f) => f.stale));

	// The failed form of the last action. Its key names the row that shows the errors.
	const failed = $derived(form?.form);
	function errorsFor(key: string, field: string): string | undefined {
		if (failed?.data?.key !== key) return undefined;
		return (failed.errors as Record<string, string[] | undefined>)[field]?.join(' ');
	}
	function messageFor(key: string): string | undefined {
		return failed?.data?.key === key ? failed.message : undefined;
	}

	// The look of a native checkbox, so that the page posts without JavaScript.
	const box = 'border-input size-4 rounded-[4px] border';
</script>

<svelte:head>
	<title>Feature flags</title>
</svelte:head>

<PageHeader
	title="Feature flags"
	description="Change who sees a feature. A change takes effect within the refresh time of the API."
/>

{#if live.length === 0}
	<Card.Root>
		<Card.Content class="text-muted-foreground py-8 text-center text-sm">
			No flags are declared. Add a [flags.&lt;key&gt;] table to spinup.toml and run spinup g flags.
		</Card.Content>
	</Card.Root>
{:else}
	<div class="grid gap-4">
		{#each live as flag (flag.key)}
			<Card.Root>
				<Card.Header>
					<Card.Title class="flex flex-wrap items-center gap-2">
						<code>{flag.key}</code>
						{#if flag.public}<Badge variant="secondary">public</Badge>{/if}
						{#if flag.expired}<Badge variant="destructive">expired</Badge>{/if}
					</Card.Title>
					{#if flag.description}<Card.Description>{flag.description}</Card.Description>{/if}
				</Card.Header>
				<Card.Content class="grid gap-6">
					{#if messageFor(flag.key)}
						<p role="alert" class="text-destructive text-sm">{messageFor(flag.key)}</p>
					{/if}
					<form method="POST" action="?/rule" use:enhance class="grid gap-4">
						<input type="hidden" name="key" value={flag.key} />
						<label class="flex items-center gap-2 text-sm font-medium">
							<input type="checkbox" name="enabled" class={box} checked={flag.enabled} />
							On for everyone who matches a rule below
						</label>
						<fieldset class="grid gap-2">
							<legend class="text-sm font-medium">Roles</legend>
							<div class="flex flex-wrap gap-4">
								{#each data.admin.roles as role (role)}
									<label class="flex items-center gap-2 text-sm">
										<input type="checkbox" name="roles" value={role} class={box} checked={flag.roles.includes(role)} />
										{role}
									</label>
								{/each}
								{#each flag.roles.filter((r) => !data.admin.roles.includes(r)) as role (role)}
									<label class="flex items-center gap-2 text-sm">
										<input type="checkbox" name="roles" value={role} class={box} checked />
										{role}
										<Badge variant="destructive">undeclared</Badge>
									</label>
								{/each}
							</div>
							{#if errorsFor(flag.key, 'roles')}
								<p class="text-destructive text-sm">{errorsFor(flag.key, 'roles')}</p>
							{/if}
						</fieldset>
						<div class="grid max-w-xs gap-2">
							<label for="percentage-{flag.key}" class="text-sm font-medium">Percentage of signed-in users</label>
							<Input
								id="percentage-{flag.key}"
								name="percentage"
								type="number"
								min="0"
								max="100"
								step="1"
								value={flag.percentage}
								aria-invalid={errorsFor(flag.key, 'percentage') ? true : undefined}
							/>
							{#if errorsFor(flag.key, 'percentage')}
								<p class="text-destructive text-sm">{errorsFor(flag.key, 'percentage')}</p>
							{/if}
						</div>
						<div><Button type="submit">Save rule</Button></div>
					</form>

					<div class="grid gap-2">
						<h2 class="text-sm font-medium">User rules</h2>
						{#if flag.users.length > 0}
							<Table.Root>
								<Table.Body>
									{#each flag.users as user (user.user_id)}
										<Table.Row>
											<Table.Cell>{user.email}</Table.Cell>
											<Table.Cell>{user.enabled ? 'on' : 'off'}</Table.Cell>
											<Table.Cell class="text-right">
												<form method="POST" action="?/clearUser" use:enhance>
													<input type="hidden" name="key" value={flag.key} />
													<input type="hidden" name="email" value={user.email} />
													<Button type="submit" variant="outline" size="sm">
														Clear<span class="sr-only"> the rule of {user.email}</span>
													</Button>
												</form>
											</Table.Cell>
										</Table.Row>
									{/each}
								</Table.Body>
							</Table.Root>
						{:else}
							<p class="text-muted-foreground text-sm">No user has a rule.</p>
						{/if}
						<form method="POST" action="?/user" use:enhance class="flex flex-wrap items-end gap-2">
							<input type="hidden" name="key" value={flag.key} />
							<div class="grid gap-2">
								<label for="email-{flag.key}" class="text-sm font-medium">Email address</label>
								<Input
									id="email-{flag.key}"
									name="email"
									type="email"
									required
									aria-invalid={errorsFor(flag.key, 'email') ? true : undefined}
								/>
							</div>
							<label class="flex items-center gap-2 pb-2 text-sm">
								<input type="checkbox" name="enabled" class={box} checked />
								On
							</label>
							<Button type="submit" variant="outline">Add user rule</Button>
						</form>
						{#if errorsFor(flag.key, 'email')}
							<p class="text-destructive text-sm">{errorsFor(flag.key, 'email')}</p>
						{/if}
					</div>
				</Card.Content>
			</Card.Root>
		{/each}
	</div>
{/if}

{#if stale.length > 0}
	<Card.Root class="mt-6">
		<Card.Header>
			<Card.Title>Stale flags</Card.Title>
			<Card.Description>
				These flags have stored rules, but spinup.toml no longer declares them. Run
				<code>flags prune --yes</code> to remove the rules.
			</Card.Description>
		</Card.Header>
		<Card.Content class="flex flex-wrap gap-2">
			{#each stale as flag (flag.key)}
				<Badge variant="outline"><code>{flag.key}</code> stale</Badge>
			{/each}
		</Card.Content>
	</Card.Root>
{/if}
