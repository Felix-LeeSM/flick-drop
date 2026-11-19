<script lang="ts">
import { SearchXIcon } from '@lucide/svelte';
import DeliveryFrame from '#lib/components/DeliveryFrame.svelte';
import { buttonVariants } from '#lib/components/ui/button/index.js';
import { t } from '#lib/i18n/index.js';
import { cn } from '#lib/utils.js';
import { resolve } from '$app/paths';
import { page } from '$app/state';

const isNotFound = $derived(page.status === 404);
</script>

<svelte:head>
	<title>{isNotFound ? t().errors.page.notFoundTitle : t().errors.page.errorTitle(page.status)} - Flick</title>
	<meta name="robots" content="noindex, nofollow" />
</svelte:head>

<DeliveryFrame>
	<section
		class="grid min-h-[calc(100dvh-8.25rem)] content-center place-items-center gap-4 py-16 text-center sm:min-h-[calc(100dvh-9.25rem)] sm:py-24"
	>
		<p class="micro flex items-center gap-1.5 text-muted-foreground">
			<SearchXIcon class="size-3.5 text-primary" aria-hidden="true" />
			{t().errors.page.label(page.status)}
		</p>
		<h1 class="font-serif text-5xl leading-[1.06] sm:text-6xl">
			{isNotFound ? t().errors.page.notFoundHeading : t().errors.page.failedHeading}
		</h1>
		<p class="max-w-sm text-sm leading-relaxed text-muted-foreground">
			{isNotFound ? t().errors.page.notFoundBody : t().errors.page.failedBody}
		</p>
		<a
			href={resolve('/')}
			class={cn(buttonVariants({ variant: 'default' }), 'mt-2 h-11 px-5')}
		>
			{t().errors.page.cta}
		</a>
	</section>
</DeliveryFrame>
