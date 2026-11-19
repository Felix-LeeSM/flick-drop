import type { common as source } from '../en/common';

export const common: typeof source = {
	enterCode: 'Enter Code',
	siteNav: '사이트',
	languageRow: '언어',
	deliveryMethod: '보내는 방법',
	tabLink: 'Link',
	tabLive: 'Live Transfer',
	theme: {
		useLight: '밝은 화면으로',
		useDark: '어두운 화면으로'
	},
	sponsor: {
		label: '후원',
		opensNewTab: ' (새 탭에서 열림)'
	},
	urlField: {
		label: '공유 링크',
		copyToClipboard: '클립보드에 복사',
		copy: '복사',
		copied: '복사했어요',
		copyFailed: '복사할 수 없어요. 위 링크를 선택해서 복사해 주세요.'
	},
	share: {
		shareLink: '링크 공유',
		failed: '공유하지 못했어요. 링크를 복사하거나 QR 코드를 써 주세요.'
	},
	qr: {
		title: '스캔하거나 복사하기',
		description: '카메라로 코드를 비추거나 아래 링크를 복사해 주세요.',
		close: '닫기',
		codeLabel: '공유 링크 QR 코드'
	},
	stage: {
		creatingLink: '링크를 만드는 중',
		openingSecret: '비밀을 여는 중',
		cancel: '취소',
		progressSteps: '진행 단계'
	},
	lifetime: {
		label: '만료 시간',
		group: '만료 시간',
		preset10Min: '10분',
		preset1Hour: '1시간',
		preset24Hours: '24시간',
		preset7Days: '7일',
		customValue: '직접 입력할 만료 시간 값',
		customUnit: '직접 입력할 만료 시간 단위',
		unitMinutes: '분',
		unitHours: '시간',
		unitDays: '일',
		daysHours: (d: number, h: number) => `${d}일 ${h}시간`,
		hoursMinutes: (h: number, m: number) => `${h}시간 ${m}분`,
		minutesSeconds: (m: number, s: number) => `${m}분 ${s}초`,
		days: (n: number) => `${n}일`,
		hours: (n: number) => `${n}시간`,
		minutes: (n: number) => `${n}분`,
		range: (min: string, max: string) => `만료 시간은 ${min}에서 ${max} 사이로 정해 주세요.`
	}
};
