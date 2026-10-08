<script lang="ts">
import { page } from '$app/state';
import ReceiveRequestPage from '$lib/components/ReceiveRequestPage.svelte';
import type { PageData } from './$types';

let { data }: { data: PageData } = $props();
</script>
{#key `${data.requestId}:${page.url.hash}`}
	<ReceiveRequestPage requestId={data.requestId} />
{/key}
