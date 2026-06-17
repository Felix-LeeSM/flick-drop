<script lang="ts">
	import OpenSecretPage from '$lib/components/OpenSecretPage.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();
</script>

<OpenSecretPage secretID={data.secretID} />
