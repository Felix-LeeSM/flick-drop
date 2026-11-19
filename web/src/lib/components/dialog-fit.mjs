// biome-ignore-all lint/suspicious/noMisplacedAssertion: 브라우저 테스트가 await하는 검사 헬퍼다.
import assert from 'node:assert/strict';

// scrollWidth는 position: fixed 대화상자의 넘침을 보지 못하므로 따로 잰다.
export async function assertDialogFits(page, message) {
	const fit = await page.locator('[role="dialog"]').evaluate(async (dialog) => {
		// 열기·transition-all 애니메이션 종료 후 잰다; 무한 반복 애니메이션은 기다리지 않는다.
		await Promise.allSettled(
			dialog
				.getAnimations({ subtree: true })
				.filter(
					(animation) => animation.effect.getComputedTiming().endTime !== Number.POSITIVE_INFINITY
				)
				.map((animation) => animation.finished)
		);
		const box = dialog.getBoundingClientRect();
		return {
			inside: box.left >= 0 && box.top >= 0 && box.right <= innerWidth && box.bottom <= innerHeight,
			contentWidth: dialog.scrollWidth,
			boxWidth: dialog.clientWidth,
			// 줄을 바꾸지 않는 버튼 글자는 대화상자 여백 안에 머물러도 버튼 테두리를 넘는다.
			spilledLabels: [...dialog.querySelectorAll('button, a')]
				.filter((control) => control.clientWidth > 0 && control.scrollWidth > control.clientWidth)
				.map((control) => control.textContent.trim())
		};
	});
	assert.equal(fit.inside, true, `${message}: dialog leaves the viewport`);
	assert.ok(
		fit.contentWidth <= fit.boxWidth,
		`${message}: content ${fit.contentWidth}px is wider than the dialog ${fit.boxWidth}px`
	);
	assert.deepEqual(fit.spilledLabels, [], `${message}: labels overflow their controls`);
}
