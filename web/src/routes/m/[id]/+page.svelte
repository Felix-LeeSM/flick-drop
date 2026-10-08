<script lang="ts">
import { page } from '$app/state';
import ManageSecretPage from '$lib/components/ManageSecretPage.svelte';
import type { PageData } from './$types';

let { data }: { data: PageData } = $props();
</script>

{#key `${data.secretId}:${page.url.hash}`}
	<ManageSecretPage secretId={data.secretId} />
{/key}
