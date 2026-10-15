<script lang="ts">
 import { enhance } from '$app/forms';
 import type { PageProps } from './$types';
 let { form }: PageProps = $props();
</script>

<svelte:head><title>Reset your password</title><meta name="referrer" content="no-referrer" /></svelte:head>

<section class="mx-auto grid w-full max-w-sm gap-4">
 <h1 class="text-2xl font-semibold">Reset your password</h1>
 {#if form?.sent}
  <p role="status">If an account exists for this email address, you will receive a reset link.</p>
 {:else}
  <form method="POST" use:enhance class="grid gap-4">
   {#if form?.errors.form}<p role="alert">{form.errors.form}</p>{/if}
   <label for="email">Email</label>
   <input class="rounded-md border p-2" id="email" name="email" type="email" autocomplete="email" required />
   <button class="rounded-md border p-2" type="submit">Send a reset link</button>
  </form>
 {/if}
 <a href="/login">Return to sign in</a>
</section>
