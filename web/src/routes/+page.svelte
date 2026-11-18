<script lang="ts">
import CreateSecretPage from '#lib/components/CreateSecretPage.svelte';
import { afterNavigate } from '$app/navigation';

// 홈에서 로고(href="/")를 누르면 URL이 같아 SvelteKit이 페이지를 다시 만들지 않는다.
// 같은 경로로의 링크 이동마다 key를 바꿔 작성 중인 폼·실시간 전송 대기·받은 비밀을 처음 상태로 되돌린다.
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
