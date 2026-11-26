<script lang="ts">
	import { invalidate } from '$app/navigation';
	import { toast } from 'svelte-sonner';
	import { FLAGS_DEP } from '#lib/auth.js';
	import { FLAGS_ADMIN_DEP, clearUser, putRule, putUser, type FlagErrors, type FlagResult } from '#lib/flags-admin.js';
	import { session } from '#lib/session.svelte.js';
	import PageHeader from '#lib/components/app/page-header.svelte';
	import { Badge } from '#lib/components/ui/badge/index.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import * as Card from '#lib/components/ui/card/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import * as Table from '#lib/components/ui/table/index.js';
	import { clearSchema, ruleSchema, userSchema } from './schema';
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();

	// Each form checks its values with the schema of node mode, then calls the Go API from the
	// browser. The errors of the last failed form show at that form.
	type Kind = 'rule' | 'user' | 'clearUser';
	type Failed = { kind: Kind; key: string; fields: Record<string, string>; message?: string };
	let failed: Failed | undefined = $state();
	// The forms that wait for the API. A form clears its entry on every outcome.
	let pending: Record<string, true> = $state({});

	// The inputs of each form. A field error of another field shows as the form message.
	const inputs: Record<Kind, string[]> = {
		rule: ['enabled', 'roles', 'percentage'],
		user: ['email', 'enabled'],
		clearUser: []
	};

	function values(form: HTMLFormElement): Record<string, unknown> {
		const fd = new FormData(form);
		return {
			key: String(fd.get('key') ?? ''),
			email: String(fd.get('email') ?? ''),
			enabled: fd.get('enabled') === 'on',
			roles: fd.getAll('roles').map(String),
			percentage: fd.has('percentage') ? Number(fd.get('percentage')) : 0
		};
	}

	function fail(kind: Kind, key: string, errors: FlagErrors) {
		const fields = Object.fromEntries(Object.entries(errors.fields).filter(([field]) => inputs[kind].includes(field)));
		const message =
			Object.keys(fields).length === 0 ? (errors.form ?? Object.values(errors.fields).join(' ')) : errors.form;
		failed = { kind, key, fields, message };
	}

	function send(kind: Kind, raw: Record<string, unknown>): Promise<FlagResult<unknown>> | Record<string, string> {
		const schema = kind === 'rule' ? ruleSchema : kind === 'user' ? userSchema : clearSchema;
		const parsed = schema.safeParse(raw);
		if (!parsed.success) {
			const fields: Record<string, string> = {};
			for (const issue of parsed.error.issues) fields[String(issue.path[0])] ??= issue.message;
			return fields;
		}
		if (kind === 'rule') {
			const v = ruleSchema.parse(raw);
			return putRule(fetch, v.key, { enabled: v.enabled, roles: v.roles, percentage: v.percentage });
		}
		if (kind === 'user') {
			const v = userSchema.parse(raw);
			return putUser(fetch, v.key, v.email, v.enabled);
		}
		const v = clearSchema.parse(raw);
		return clearUser(fetch, v.key, v.email);
	}

	async function submit(event: SubmitEvent, kind: Kind) {
		event.preventDefault();
		const raw = values(event.currentTarget as HTMLFormElement);
		const key = String(raw.key);
		const id = `${kind}:${key}:${String(raw.email)}`;
		if (pending[id]) return;
		const started = session.generation;
		pending[id] = true;
		try {
			const call = send(kind, raw);
			if (!(call instanceof Promise)) {
				fail(kind, key, { fields: call });
				return;
			}
			const result = await call;
			// A sign-out or a departure started during the call: the answer belongs to the old session.
			if (started !== session.generation) return;
			if (!result.ok) {
				fail(kind, key, result.errors);
				return;
			}
			failed = undefined;
			toast.success('Flag saved.');
			// One invalidation reads the admin list and the public flags of the root load again. The
			// API store refreshes on its own interval, so /api/flags can still give the old value.
			await invalidate((url) => url.href === FLAGS_ADMIN_DEP || url.href === FLAGS_DEP);
		} catch (err) {
			console.error('flags: the save failed:', err);
			fail(kind, key, { fields: {}, form: 'The server is not available. Try again later.' });
		} finally {
			delete pending[id];
		}
	}

	function busy(kind: Kind, key: string, email = ''): boolean {
		return pending[`${kind}:${key}:${email}`] === true;
	}

	const live = $derived(data.admin.flags.filter((f) => !f.stale));
	const stale = $derived(data.admin.flags.filter((f) => f.stale));

	function errorsFor(kind: Kind, key: string, field: string): string | undefined {
		return failed?.kind === kind && failed.key === key ? failed.fields[field] : undefined;
	}
	function messageFor(kind: Kind, key: string): string | undefined {
		return failed?.kind === kind && failed.key === key ? failed.message : undefined;
	}

	// The look of a native checkbox. The forms of embed mode need JavaScript to post.
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
					<form method="POST" onsubmit={(e) => submit(e, 'rule')} class="grid gap-4">
						{#if messageFor('rule', flag.key)}
							<p role="alert" class="text-destructive text-sm">{messageFor('rule', flag.key)}</p>
						{/if}
						<input type="hidden" name="key" value={flag.key} />
						<label class="flex items-center gap-2 text-sm font-medium">
							<input type="checkbox" name="enabled" class={box} checked={flag.enabled} />
							On for everyone, signed-out visitors included
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
							{#if errorsFor('rule', flag.key, 'roles')}
								<p class="text-destructive text-sm">{errorsFor('rule', flag.key, 'roles')}</p>
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
								aria-invalid={errorsFor('rule', flag.key, 'percentage') ? true : undefined}
							/>
							{#if errorsFor('rule', flag.key, 'percentage')}
								<p class="text-destructive text-sm">{errorsFor('rule', flag.key, 'percentage')}</p>
							{/if}
						</div>
						<div><Button type="submit" disabled={busy('rule', flag.key)}>Save rule</Button></div>
					</form>

					<div class="grid gap-2">
						<h2 class="text-sm font-medium">User rules</h2>
						{#if messageFor('clearUser', flag.key)}
							<p role="alert" class="text-destructive text-sm">{messageFor('clearUser', flag.key)}</p>
						{/if}
						{#if flag.users.length > 0}
							<Table.Root>
								<Table.Body>
									{#each flag.users as user (user.user_id)}
										<Table.Row>
											<Table.Cell>{user.email}</Table.Cell>
											<Table.Cell>{user.enabled ? 'on' : 'off'}</Table.Cell>
											<Table.Cell class="text-right">
												<form method="POST" onsubmit={(e) => submit(e, 'clearUser')}>
													<input type="hidden" name="key" value={flag.key} />
													<input type="hidden" name="email" value={user.email} />
													<Button
														type="submit"
														variant="outline"
														size="sm"
														disabled={busy('clearUser', flag.key, user.email)}
													>
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
						<form method="POST" onsubmit={(e) => submit(e, 'user')} class="flex flex-wrap items-end gap-2">
							<input type="hidden" name="key" value={flag.key} />
							<div class="grid gap-2">
								<label for="email-{flag.key}" class="text-sm font-medium">Email address</label>
								<Input
									id="email-{flag.key}"
									name="email"
									type="email"
									required
									aria-invalid={errorsFor('user', flag.key, 'email') ? true : undefined}
								/>
							</div>
							<label class="flex items-center gap-2 pb-2 text-sm">
								<input type="checkbox" name="enabled" class={box} checked />
								On
							</label>
							<Button type="submit" variant="outline" disabled={busy('user', flag.key)}>Add user rule</Button>
						</form>
						{#if errorsFor('user', flag.key, 'email')}
							<p class="text-destructive text-sm">{errorsFor('user', flag.key, 'email')}</p>
						{/if}
						{#if messageFor('user', flag.key)}
							<p role="alert" class="text-destructive text-sm">{messageFor('user', flag.key)}</p>
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
