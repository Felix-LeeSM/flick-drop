<script lang="ts">
import CreateSecretPage from '#lib/components/CreateSecretPage.svelte';
import { afterNavigate } from '$app/navigation';

// 같은 URL로의 이동은 SvelteKit이 페이지를 다시 만들지 않으므로 key를 바꿔 홈 상태를 초기화한다.
let homeVisit = $state(0);

afterNavigate((navigation) => {
	const reenteredHome =
		navigation.type === 'link' &&
		navigation.from?.url.pathname === '/' &&
		navigation.to?.url.pathname === '/' &&
		navigation.to.url.hash === '';
	if (reenteredHome) {
		homeVisit += 1;
	}
});
</script>

{#key homeVisit}
	<CreateSecretPage />
{/key}
