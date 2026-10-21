<!--
	The actions menu of the rows of a list. A list renders one RowMenu outside its {#each}.
	Each row has a plain button from menu.button(). The menu opens at the button that was used.
	One menu for all rows keeps the server render of a long list cheap.

	<script>
		const menu = new RowMenuState();
	</script>
	{#each items as item (item.id)}
		<button {...menu.button(item.id, 'Actions for ' + title(item))}>...</button>
	{/each}
	<RowMenu {menu} {items} href="/todos" ondelete={(id) => ...} />
-->
<script lang="ts" module>
	import { buttonVariants } from '#lib/components/ui/button/index.js';

	/** The id of the open menu. The row button that opened the menu points to it with aria-controls. */
	const contentId = 'row-menu';

	/**
	 * The look of a row button: a ghost icon button. While a menu is open, bits-ui turns off the
	 * pointer events of the page. The row buttons keep them, so a click on a row button can close the
	 * menu or move it to another row.
	 */
	const buttonClass = buttonVariants({ variant: 'ghost', size: 'icon-sm' }) + ' pointer-events-auto';

	/** RowMenuState is the state of one row menu: which row it is open for, and the button that opened it. */
	export class RowMenuState {
		open = $state(false);
		id: number | null = $state(null);
		anchor: HTMLElement | null = $state(null);

		/** openFor opens the menu for the row of a button. */
		openFor(id: number, anchor: HTMLElement) {
			this.id = id;
			this.anchor = anchor;
			this.open = true;
		}

		/** close closes the menu. With focus, the row button gets the focus back. */
		close(focus = false) {
			this.open = false;
			if (focus) this.anchor?.focus();
		}

		/**
		 * button returns the attributes of the button of a row. Spread them on a plain button.
		 * The label names the action and the row, such as "Actions for Buy milk".
		 */
		button(id: number, label: string) {
			const active = this.open && this.id === id;
			return {
				type: 'button' as const,
				class: buttonClass,
				'data-row-menu': '',
				'aria-label': label,
				'aria-haspopup': 'menu' as const,
				'aria-expanded': active,
				'aria-controls': active ? contentId : undefined,
				'data-state': active ? 'open' : 'closed',
				// Enter and Space click the button. A click on the button of the open row closes the menu.
				onclick: (event: MouseEvent) => {
					if (this.open && this.id === id) this.close();
					else this.openFor(id, event.currentTarget as HTMLElement);
				},
				// ArrowDown opens the menu, and the first item gets the focus.
				onkeydown: (event: KeyboardEvent) => {
					if (event.key !== 'ArrowDown') return;
					event.preventDefault();
					this.openFor(id, event.currentTarget as HTMLElement);
				}
			};
		}
	}
</script>

<script lang="ts">
	import EyeIcon from '@lucide/svelte/icons/eye';
	import PencilIcon from '@lucide/svelte/icons/pencil';
	import Trash2Icon from '@lucide/svelte/icons/trash-2';
	import * as DropdownMenu from '#lib/components/ui/dropdown-menu/index.js';

	let {
		menu,
		items,
		href,
		ondelete
	}: {
		/** The state that the row buttons use. */
		menu: RowMenuState;
		/** The rows of the list. The menu closes when its row leaves the list. */
		items: { id: number }[];
		/** The address of the list, such as /todos. The View and Edit items add the id. */
		href: string;
		/** Called with the id of the row when the user chooses Delete. */
		ondelete: (id: number) => void;
	} = $props();

	// A delete, a page change, or a navigation can remove the row of the open menu.
	$effect(() => {
		if (menu.open && !items.some((item) => item.id === menu.id)) menu.close();
	});
</script>

<DropdownMenu.Root bind:open={menu.open}>
	<DropdownMenu.Content
		id={contentId}
		align="end"
		class="w-40"
		customAnchor={menu.anchor}
		onCloseAutoFocus={(event) => {
			event.preventDefault();
			menu.anchor?.focus();
		}}
		onInteractOutside={(event) => {
			// A click on a row button toggles the menu, and the button does it.
			if ((event.target as Element | null)?.closest('[data-row-menu]')) event.preventDefault();
		}}
		onkeydown={(event) => {
			// Tab closes the menu. The next Tab continues from the row button.
			if (event.key !== 'Tab') return;
			event.preventDefault();
			menu.close(true);
		}}
	>
		{#if menu.id !== null}
			{@const id = menu.id}
			<DropdownMenu.Item>
				{#snippet child({ props })}
					<a {...props} href="{href}/{id}">
						<EyeIcon aria-hidden="true" />
						View
					</a>
				{/snippet}
			</DropdownMenu.Item>
			<DropdownMenu.Item>
				{#snippet child({ props })}
					<a {...props} href="{href}/{id}/edit">
						<PencilIcon aria-hidden="true" />
						Edit
					</a>
				{/snippet}
			</DropdownMenu.Item>
			<DropdownMenu.Separator />
			<DropdownMenu.Item variant="destructive" onSelect={() => ondelete(id)}>
				<Trash2Icon aria-hidden="true" />
				Delete
			</DropdownMenu.Item>
		{/if}
	</DropdownMenu.Content>
</DropdownMenu.Root>
