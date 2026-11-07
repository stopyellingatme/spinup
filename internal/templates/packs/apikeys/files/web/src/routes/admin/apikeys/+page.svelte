<script lang="ts">
	import { enhance, type SubmitFunction } from '$app/forms';
	import { afterNavigate, goto } from '$app/navigation';
	import { page } from '$app/state';
	import LoaderCircleIcon from '@lucide/svelte/icons/loader-circle';
	import { toast } from 'svelte-sonner';
	import PageHeader from '#lib/components/app/page-header.svelte';
	import * as AlertDialog from '#lib/components/ui/alert-dialog/index.js';
	import { Badge } from '#lib/components/ui/badge/index.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import * as Table from '#lib/components/ui/table/index.js';
	import type { ApiKey } from '#lib/server/apikeys.js';
	import type { PageProps } from './$types';

	let { data, form }: PageProps = $props();

	// Show the toast of a redirect once, then remove its flag from the address bar.
	afterNavigate(({ to }) => {
		const params = to?.url.searchParams;
		if (!params || !(params.has('saved') || params.has('revoked'))) return;
		toast.success(params.has('saved') ? 'Plan saved.' : 'Key revoked.');
		const url = new URL(to.url);
		url.searchParams.delete('saved');
		url.searchParams.delete('revoked');
		void goto(url, { replace: true, shallow: true, state: page.state });
	});

	const admin = $derived(data.admin);
	const limits = $derived(new Map(admin.plans.map((p) => [p.name, p])));

	// The failed plan form. The API field errors show at their inputs.
	const failed = $derived(form && 'form' in form ? form.form : undefined);
	const revokeError = $derived(form && 'revokeError' in form ? form.revokeError : undefined);
	const emailErrors = $derived(failed?.errors.email?.join(' '));
	const planErrors = $derived(failed?.errors.plan?.join(' '));

	// Dates show in UTC, so the server render and the browser render are the same.
	function when(value: string | null): string {
		if (!value) return 'never';
		const iso = new Date(value).toISOString();
		return `${iso.slice(0, 10)} ${iso.slice(11, 16)} UTC`;
	}

	// The key that the revoke dialog asks about. It stays set while the dialog closes.
	let target: { key: ApiKey; email: string } | null = $state(null);
	let confirming = $state(false);
	let revoking = $state(false);
	let saving = $state(false);

	function confirmRevoke(key: ApiKey, email: string) {
		target = { key, email };
		confirming = true;
	}

	const revoke: SubmitFunction = () => {
		revoking = true;
		return async ({ update }) => {
			await update();
			revoking = false;
			confirming = false;
		};
	};

	const save: SubmitFunction = () => {
		saving = true;
		return async ({ update }) => {
			await update({ reset: false });
			saving = false;
		};
	};
</script>

<svelte:head>
	<title>API plans</title>
</svelte:head>

<PageHeader
	title="API plans"
	description="Change the plan of an account, and revoke keys. A plan applies to all keys of the account."
/>

<div class="grid gap-6">
	<Card.Root>
		<Card.Header>
			<Card.Title>Plans</Card.Title>
			<Card.Description>The plans come from [apikeys.plans] in spinup.toml.</Card.Description>
		</Card.Header>
		<Card.Content>
			<Table.Root>
				<Table.Header>
					<Table.Row>
						<Table.Head>Plan</Table.Head>
						<Table.Head class="text-right">Requests a minute</Table.Head>
						<Table.Head class="text-right">Requests a day</Table.Head>
					</Table.Row>
				</Table.Header>
				<Table.Body>
					{#each admin.plans as plan (plan.name)}
						<Table.Row>
							<Table.Cell class="font-medium">
								{plan.name}
								{#if plan.name === admin.default_plan}<Badge variant="secondary">default</Badge>{/if}
							</Table.Cell>
							<Table.Cell class="text-right">{plan.per_minute}</Table.Cell>
							<Table.Cell class="text-right">{plan.per_day}</Table.Cell>
						</Table.Row>
					{/each}
				</Table.Body>
			</Table.Root>
		</Card.Content>
	</Card.Root>

	<Card.Root>
		<Card.Header>
			<Card.Title>Set a plan</Card.Title>
			<Card.Description>
				Enter the email address of any account. An account without a plan has the default plan.
			</Card.Description>
		</Card.Header>
		<Card.Content>
			<form method="POST" action="?/plan" use:enhance={save} class="flex flex-wrap items-start gap-4">
				{#if failed?.message}
					<p role="alert" class="text-destructive w-full text-sm">{failed.message}</p>
				{/if}
				<div class="grid gap-2">
					<label for="plan-email" class="text-sm font-medium">Email address</label>
					<Input
						id="plan-email"
						name="email"
						type="email"
						required
						list="plan-emails"
						value={failed?.data.email ?? ''}
						aria-invalid={emailErrors ? true : undefined}
					/>
					<datalist id="plan-emails">
						{#each admin.accounts as account (account.user_id)}
							<option value={account.email}></option>
						{/each}
					</datalist>
					{#if emailErrors}<p class="text-destructive text-sm">{emailErrors}</p>{/if}
				</div>
				<div class="grid gap-2">
					<label for="plan-name" class="text-sm font-medium">Plan</label>
					<select
						id="plan-name"
						name="plan"
						class="border-input dark:bg-input/30 h-8 rounded-lg border bg-transparent px-2.5 text-sm"
						aria-invalid={planErrors ? true : undefined}
					>
						{#each admin.plans as plan (plan.name)}
							<option value={plan.name} selected={(failed?.data.plan ?? admin.default_plan) === plan.name}>
								{plan.name}
							</option>
						{/each}
					</select>
					{#if planErrors}<p class="text-destructive text-sm">{planErrors}</p>{/if}
				</div>
				<div class="pt-6">
					<Button type="submit" disabled={saving}>
						{#if saving}<LoaderCircleIcon aria-hidden="true" class="animate-spin" />{/if}
						Save plan
					</Button>
				</div>
			</form>
		</Card.Content>
	</Card.Root>

	<Card.Root>
		<Card.Header>
			<Card.Title>Accounts</Card.Title>
			<Card.Description>The accounts that have a key or a plan. Use is for today, UTC.</Card.Description>
		</Card.Header>
		<Card.Content class="grid gap-4">
			{#if revokeError}
				<p role="alert" class="text-destructive text-sm">{revokeError}</p>
			{/if}
			{#if admin.accounts.length === 0}
				<p class="text-muted-foreground text-sm">No account has a key or a plan yet.</p>
			{:else}
				<div class="grid gap-4">
					{#each admin.accounts as account (account.user_id)}
						{@const limit = limits.get(account.plan)}
						<div class="grid gap-2 rounded-lg border p-3">
							<div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm">
								<span class="font-medium">{account.email}</span>
								<Badge variant="outline">{account.role}</Badge>
								<span>Plan <Badge variant="secondary">{account.plan}</Badge></span>
								<span>
									Use today {account.today}{#if limit} of {limit.per_day}{/if}
								</span>
							</div>
							{#if account.keys.length === 0}
								<p class="text-muted-foreground text-sm">No keys.</p>
							{:else}
								<div class="overflow-x-auto">
									<Table.Root>
										<Table.Header>
											<Table.Row>
												<Table.Head>Name</Table.Head>
												<Table.Head>Scope</Table.Head>
												<Table.Head>Key</Table.Head>
												<Table.Head>Last used</Table.Head>
												<Table.Head class="text-right"><span class="sr-only">Actions</span></Table.Head>
											</Table.Row>
										</Table.Header>
										<Table.Body>
											{#each account.keys as key (key.id)}
												<Table.Row>
													<Table.Cell class="font-medium">{key.name}</Table.Cell>
													<Table.Cell>
														<Badge variant={key.scope === 'write' ? 'default' : 'secondary'}>{key.scope}</Badge>
													</Table.Cell>
													<Table.Cell><code>{key.display}…</code></Table.Cell>
													<Table.Cell>{when(key.last_used_at)}</Table.Cell>
													<Table.Cell class="text-right">
														<Button
															type="button"
															variant="outline"
															size="sm"
															onclick={() => confirmRevoke(key, account.email)}
														>
															Revoke<span class="sr-only"> the key {key.name} of {account.email}</span>
														</Button>
													</Table.Cell>
												</Table.Row>
											{/each}
										</Table.Body>
									</Table.Root>
								</div>
							{/if}
						</div>
					{/each}
				</div>
			{/if}
		</Card.Content>
	</Card.Root>
</div>

<AlertDialog.Root bind:open={confirming}>
	<AlertDialog.Content>
		<form method="POST" action="?/revoke" use:enhance={revoke} class="grid gap-4">
			<input type="hidden" name="id" value={target?.key.id} />
			<AlertDialog.Header>
				<AlertDialog.Title>Revoke this key?</AlertDialog.Title>
				<AlertDialog.Description>
					The key “{target?.key.name}” of {target?.email} stops working at once. You cannot undo this.
				</AlertDialog.Description>
			</AlertDialog.Header>
			<AlertDialog.Footer>
				<AlertDialog.Cancel type="button" disabled={revoking}>Cancel</AlertDialog.Cancel>
				<Button type="submit" variant="destructive" disabled={revoking}>
					{#if revoking}<LoaderCircleIcon aria-hidden="true" class="animate-spin" />{/if}
					Revoke
				</Button>
			</AlertDialog.Footer>
		</form>
	</AlertDialog.Content>
</AlertDialog.Root>
