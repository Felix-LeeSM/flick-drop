<script lang="ts">
import SubmitRequestPage from '#lib/components/SubmitRequestPage.svelte';
import { page } from '$app/state';
import type { PageData } from './$types';

let { data }: { data: PageData } = $props();
</script>
{#key `${data.requestId}:${page.url.hash}`}
	<SubmitRequestPage requestId={data.requestId} />
{/key}
