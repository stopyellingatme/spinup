<script lang="ts">
	import { enhance, type SubmitFunction } from '$app/forms';
	import { afterNavigate, goto } from '$app/navigation';
	import { page } from '$app/state';
	import CopyIcon from '@lucide/svelte/icons/copy';
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
		if (!to?.url.searchParams.has('revoked')) return;
		toast.success('Key revoked.');
		const url = new URL(to.url);
		url.searchParams.delete('revoked');
		void goto(url, { replace: true, shallow: true, state: page.state });
	});

	const overview = $derived(data.overview);
	const plan = $derived(overview.plan);
	const used = $derived(Math.min(overview.today, plan.per_day));
	const percent = $derived(plan.per_day > 0 ? Math.round((used / plan.per_day) * 100) : 0);

	// The failed create form. The API field errors show at their inputs.
	const failed = $derived(form && 'form' in form ? form.form : undefined);
	const created = $derived(form && 'created' in form ? form.created : undefined);
	const revokeError = $derived(form && 'revokeError' in form ? form.revokeError : undefined);
	const nameErrors = $derived(failed?.errors.name?.join(' '));
	const scopeErrors = $derived(failed?.errors.scope?.join(' '));
	const daysErrors = $derived(failed?.errors.expires_in_days?.join(' '));

	// Dates show in UTC, so the server render and the browser render are the same.
	function when(value: string | null): string {
		if (!value) return 'never';
		const iso = new Date(value).toISOString();
		return `${iso.slice(0, 10)} ${iso.slice(11, 16)} UTC`;
	}
	function expires(value: string | null): string {
		return value ? when(value) : 'does not expire';
	}

	// The usage table: the newest day first. A key that is revoked has no name any more.
	const names = $derived(new Map(overview.keys.map((k) => [k.id, k.name])));
	const usage = $derived(
		overview.usage
			.map((u) => ({ ...u, name: names.get(u.key_id) ?? 'revoked key' }))
			.sort((a, b) => b.day.localeCompare(a.day) || a.name.localeCompare(b.name) || a.key_id - b.key_id)
	);

	async function copy(text: string) {
		try {
			await navigator.clipboard.writeText(text);
			toast.success('Key copied.');
		} catch {
			toast.error('The browser blocked the copy. Select the key and copy it by hand.');
		}
	}

	// The key that the revoke dialog asks about. It stays set while the dialog closes.
	let target: ApiKey | null = $state(null);
	let confirming = $state(false);
	let revoking = $state(false);
	let creating = $state(false);

	function confirmRevoke(key: ApiKey) {
		target = key;
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

	const create: SubmitFunction = () => {
		creating = true;
		return async ({ update }) => {
			await update();
			creating = false;
		};
	};
</script>

<svelte:head>
	<title>API keys</title>
</svelte:head>

<PageHeader
	title="API keys"
	description="Use a key to call the API from a program. Send it in the header Authorization: Bearer your-key."
/>

<div class="grid gap-6">
	{#if created}
		<Card.Root>
			<Card.Header>
				<Card.Title>Key “{created.name}” is ready</Card.Title>
				<Card.Description>Copy this key now. You cannot see it again.</Card.Description>
			</Card.Header>
			<Card.Content class="flex flex-wrap items-center gap-2">
				<Input
					readonly
					value={created.token}
					aria-label="The new API key"
					data-testid="new-key-token"
					class="max-w-xl font-mono"
					onfocus={(event) => event.currentTarget.select()}
				/>
				<Button type="button" variant="outline" onclick={() => copy(created.token)}>
					<CopyIcon aria-hidden="true" />
					Copy
				</Button>
			</Card.Content>
		</Card.Root>
	{/if}

	<Card.Root>
		<Card.Header>
			<Card.Title class="flex flex-wrap items-center gap-2">
				Plan <Badge variant="secondary" data-testid="plan-name">{plan.name}</Badge>
			</Card.Title>
			<Card.Description>
				{plan.per_minute} requests a minute and {plan.per_day} requests a day for all your keys together. Requests
				from this web app do not count.
			</Card.Description>
		</Card.Header>
		<Card.Content class="grid gap-2">
			<div class="flex items-baseline justify-between text-sm">
				<span class="font-medium">Used today (UTC)</span>
				<span data-testid="used-today">{overview.today} of {plan.per_day}</span>
			</div>
			<div
				class="bg-muted h-2 w-full overflow-hidden rounded-full"
				role="progressbar"
				aria-label="Requests used today"
				aria-valuemin="0"
				aria-valuemax={plan.per_day}
				aria-valuenow={used}
			>
				<div class="bg-primary h-full" style="width: {percent}%"></div>
			</div>
		</Card.Content>
	</Card.Root>

	<Card.Root>
		<Card.Header>
			<Card.Title>Keys</Card.Title>
			<Card.Description>
				{overview.keys.length} of {overview.max_keys} keys. A revoked key stops working at once.
			</Card.Description>
		</Card.Header>
		<Card.Content class="grid gap-4">
			{#if revokeError}
				<p role="alert" class="text-destructive text-sm">{revokeError}</p>
			{/if}
			{#if overview.keys.length === 0}
				<p class="text-muted-foreground text-sm">You have no keys. Create one below.</p>
			{:else}
				<div class="overflow-x-auto">
					<Table.Root>
						<Table.Header>
							<Table.Row>
								<Table.Head>Name</Table.Head>
								<Table.Head>Scope</Table.Head>
								<Table.Head>Key</Table.Head>
								<Table.Head>Created</Table.Head>
								<Table.Head>Last used</Table.Head>
								<Table.Head>Expires</Table.Head>
								<Table.Head class="text-right"><span class="sr-only">Actions</span></Table.Head>
							</Table.Row>
						</Table.Header>
						<Table.Body>
							{#each overview.keys as key (key.id)}
								<Table.Row>
									<Table.Cell class="font-medium">{key.name}</Table.Cell>
									<Table.Cell>
										<Badge variant={key.scope === 'write' ? 'default' : 'secondary'}>{key.scope}</Badge>
									</Table.Cell>
									<Table.Cell><code>{key.display}…</code></Table.Cell>
									<Table.Cell>{when(key.created_at)}</Table.Cell>
									<Table.Cell>{when(key.last_used_at)}</Table.Cell>
									<Table.Cell>{expires(key.expires_at)}</Table.Cell>
									<Table.Cell class="text-right">
										<Button type="button" variant="outline" size="sm" onclick={() => confirmRevoke(key)}>
											Revoke<span class="sr-only"> the key {key.name}</span>
										</Button>
									</Table.Cell>
								</Table.Row>
							{/each}
						</Table.Body>
					</Table.Root>
				</div>
			{/if}
		</Card.Content>
	</Card.Root>

	<Card.Root>
		<Card.Header>
			<Card.Title>Create a key</Card.Title>
			<Card.Description>
				A read key can call GET and HEAD. A write key can call all methods. A key has the role of your account.
			</Card.Description>
		</Card.Header>
		<Card.Content>
			<form method="POST" action="?/create" use:enhance={create} class="grid max-w-xl gap-4">
				{#if failed?.message}
					<p role="alert" class="text-destructive text-sm">{failed.message}</p>
				{/if}
				<div class="grid gap-2">
					<label for="key-name" class="text-sm font-medium">Name</label>
					<Input
						id="key-name"
						name="name"
						required
						maxlength={64}
						value={failed?.data.name ?? ''}
						aria-invalid={nameErrors ? true : undefined}
					/>
					{#if nameErrors}<p class="text-destructive text-sm">{nameErrors}</p>{/if}
				</div>
				<div class="grid gap-2">
					<label for="key-scope" class="text-sm font-medium">Scope</label>
					<select
						id="key-scope"
						name="scope"
						class="border-input dark:bg-input/30 h-8 rounded-lg border bg-transparent px-2.5 text-sm"
						aria-invalid={scopeErrors ? true : undefined}
					>
						<option value="read" selected={failed?.data.scope !== 'write'}>read</option>
						<option value="write" selected={failed?.data.scope === 'write'}>write</option>
					</select>
					{#if scopeErrors}<p class="text-destructive text-sm">{scopeErrors}</p>{/if}
				</div>
				<div class="grid gap-2">
					<label for="key-days" class="text-sm font-medium">Expires after (days, optional)</label>
					<Input
						id="key-days"
						name="expires_in_days"
						type="number"
						min="1"
						max="365"
						step="1"
						placeholder="Never"
						value={failed?.data.expires_in_days ?? ''}
						aria-invalid={daysErrors ? true : undefined}
					/>
					{#if daysErrors}<p class="text-destructive text-sm">{daysErrors}</p>{/if}
				</div>
				<div>
					<Button type="submit" disabled={creating}>
						{#if creating}<LoaderCircleIcon aria-hidden="true" class="animate-spin" />{/if}
						Create key
					</Button>
				</div>
			</form>
		</Card.Content>
	</Card.Root>

	<Card.Root>
		<Card.Header>
			<Card.Title>Use in the last 30 days</Card.Title>
			<Card.Description>The requests of your keys for each UTC day.</Card.Description>
		</Card.Header>
		<Card.Content>
			{#if usage.length === 0}
				<p class="text-muted-foreground text-sm">No key has made a request yet.</p>
			{:else}
				<Table.Root>
					<Table.Header>
						<Table.Row>
							<Table.Head>Day</Table.Head>
							<Table.Head>Key</Table.Head>
							<Table.Head class="text-right">Requests</Table.Head>
						</Table.Row>
					</Table.Header>
					<Table.Body>
						{#each usage as row (row.day + ':' + row.key_id)}
							<Table.Row>
								<Table.Cell>{row.day}</Table.Cell>
								<Table.Cell>{row.name}</Table.Cell>
								<Table.Cell class="text-right">{row.count}</Table.Cell>
							</Table.Row>
						{/each}
					</Table.Body>
				</Table.Root>
			{/if}
		</Card.Content>
	</Card.Root>
</div>

<AlertDialog.Root bind:open={confirming}>
	<AlertDialog.Content>
		<form method="POST" action="?/revoke" use:enhance={revoke} class="grid gap-4">
			<input type="hidden" name="id" value={target?.id} />
			<AlertDialog.Header>
				<AlertDialog.Title>Revoke this key?</AlertDialog.Title>
				<AlertDialog.Description>
					The key “{target?.name}” stops working at once. You cannot undo this.
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
