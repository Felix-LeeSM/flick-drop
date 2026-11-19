import { describe, expect, it } from 'vitest';
import { deviceLabelText } from './device';
import { ko } from './messages/ko';

describe('deviceLabelText', () => {
	it('서버가 보낸 영어 기기 이름을 카탈로그의 기기 이름 문구로 바꾼다', () => {
		const pending = ko.live.pending;

		expect(deviceLabelText('Safari on iPhone', ko)).toBe(pending.device('Safari', 'iPhone'));
		expect(deviceLabelText('Unknown browser on Android', ko)).toBe(
			pending.device(pending.unknownBrowser, 'Android')
		);
		expect(deviceLabelText('Unknown browser', ko)).toBe(pending.unknownBrowser);
		expect(deviceLabelText('Chrome', ko)).toBe('Chrome');
	});
});
