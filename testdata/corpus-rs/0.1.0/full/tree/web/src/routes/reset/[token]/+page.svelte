<script lang="ts">
 import { enhance } from '$app/forms';
 import type { PageProps } from './$types';
 let { form }: PageProps = $props();
</script>

<svelte:head><title>Choose a new password</title><meta name="referrer" content="no-referrer" /></svelte:head>

<section class="mx-auto grid w-full max-w-sm gap-4">
 <h1 class="text-2xl font-semibold">Choose a new password</h1>
 <form method="POST" use:enhance class="grid gap-4">
  {#if form?.errors.form}<p role="alert">{form.errors.form}</p>{/if}
  <label for="password">New password</label>
  <input class="rounded-md border p-2" id="password" name="password" type="password" autocomplete="new-password" minlength={12} maxlength={512} required aria-describedby={form?.errors.password ? 'password-error' : undefined} />
  {#if form?.errors.password}<p role="alert" id="password-error">{form.errors.password}</p>{/if}
  <button class="rounded-md border p-2" type="submit">Save the new password</button>
 </form>
 <a href="/reset">Request a new link</a>
</section>
