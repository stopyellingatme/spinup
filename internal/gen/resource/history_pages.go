package resource

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

func planHistoryPages(spec Spec) (vfs.Plan, error) {
	web, err := hasWeb(spec.Root)
	if err != nil || !web {
		return vfs.Plan{}, err
	}
	lock, err := manifest.Load(filepath.Join(spec.Root, "spinup.lock"))
	if err != nil {
		return vfs.Plan{}, err
	}
	if lock.Answers["frontend_mode"] == "embed" {
		return vfs.Plan{}, fmt.Errorf("cannot generate history pages in embed mode\nTry: use an app with --frontend-mode node")
	}
	if err := checkPageInputs(spec.Root, true); err != nil {
		return vfs.Plan{}, err
	}
	data := newPageData(spec)
	data.Roles = spec.Audit.HistoryRoles
	data.RoleLiterals = roleLiterals(data.Roles)
	var plan vfs.Plan
	for _, file := range []struct{ path, source string }{{"+page.server.ts", historyServerTS}, {"+page.svelte", historySvelte}} {
		path := pagesDir(spec.Name) + "/[id]/history/" + file.path
		if _, err := os.Stat(filepath.Join(spec.Root, path)); err == nil {
			return vfs.Plan{}, fmt.Errorf("cannot add history because %s already exists\nTry: review the existing history page", path)
		} else if !os.IsNotExist(err) {
			return vfs.Plan{}, fmt.Errorf("check %s: %w\nTry: check the file permissions", path, err)
		}
		out, err := renderPage(path, file.source, data)
		if err != nil {
			return vfs.Plan{}, err
		}
		if err := plan.Add(vfs.File{Path: path, Data: out, Class: string(manifest.Scaffold)}); err != nil {
			return vfs.Plan{}, err
		}
	}
	return plan, nil
}

const historyServerTS = `import { error } from '@sveltejs/kit';
import { api } from '#lib/api/client.js';
[[ if .Roles -]]
import { requireRole } from '#lib/server/auth.js';
[[ else -]]
import { requireUser } from '#lib/server/auth.js';
[[ end -]]
import type { PageServerLoad } from './$types';

export const load: PageServerLoad = async ({ fetch, locals, url, params, setHeaders }) => {
[[- if .Roles ]]
	requireRole(locals, url, [[ .RoleLiterals ]]);
[[- else ]]
	requireUser(locals, url);
[[- end ]]
	setHeaders({ 'cache-control': 'no-store' });
	const id = Number(params.id);
	if (!Number.isSafeInteger(id) || id < 1) error(400, 'Invalid record ID. Try: use a positive whole number.');
	const cursor = url.searchParams.get('before_id');
	const before = cursor === null ? undefined : Number(cursor);
	if (before !== undefined && (!Number.isSafeInteger(before) || before < 1)) {
		error(422, 'Invalid history cursor. Try: open the newest events.');
	}
	const { data, response } = await api.GET('[[ .APIPath ]]/{id}/history', {
		fetch,
		baseUrl: __PUBLIC_ORIGIN__,
		params: { path: { id }, query: { limit: 50, before_id: before } }
	});
	if (!data) error(response.status, 'Cannot read history. Try: retry the request.');
	const events = data.items;
	return { id, events, nextBeforeID: events.length === 50 ? events[events.length - 1].id : null };
};
`

const historySvelte = `<script lang="ts">
	import HistoryList from '#lib/components/app/history-list.svelte';
	import PageHeader from '#lib/components/app/page-header.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();
</script>

<svelte:head>
	<title>History of [[ .Words ]] #{data.id}</title>
</svelte:head>

<div class="space-y-6">
	<PageHeader title="History of [[ .Words ]] #{data.id}" />
	<HistoryList events={data.events} />
	<nav aria-label="History pages" class="flex gap-4">
		<a href="[[ .Route ]]/{data.id}/history">Newest events</a>
		{#if data.nextBeforeID !== null}
			<a href="[[ .Route ]]/{data.id}/history?before_id={data.nextBeforeID}">Older events</a>
		{/if}
	</nav>
</div>
`
