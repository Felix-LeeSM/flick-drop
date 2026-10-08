<script lang="ts">
import { page } from '$app/state';
import SubmitRequestPage from '$lib/components/SubmitRequestPage.svelte';
import type { PageData } from './$types';

let { data }: { data: PageData } = $props();
</script>
{#key `${data.requestId}:${page.url.hash}`}
	<SubmitRequestPage requestId={data.requestId} />
{/key}
