import type { errors as source } from '../en/errors';

export const errors: typeof source = {
	invalidPassphrase: '비밀번호가 맞지 않아요.',
	noLongerAvailable: '이 내용은 더 이상 열 수 없어요.',
	fileTooLarge: '파일이 너무 커요.',
	storageFull: '저장 공간이 가득 찼어요. 나중에 다시 시도해 주세요.',
	uploadFailed: '업로드하지 못했어요. 다시 시도해 주세요.',
	notReady: 'Flick이 아직 준비되지 않았어요. 잠시 후 다시 시도해 주세요.',
	serverFailed: 'Flick에서 문제가 생겼어요. 다시 시도해 주세요.',
	requestFailed: '처리하지 못했어요. 입력한 내용을 확인하고 다시 시도해 주세요.',
	rateLimited: '짧은 시간에 너무 많이 시도했어요. 잠시 후 다시 시도해 주세요.',
	largeUploadStartFailed: '파일을 올리기 시작하지 못했어요. 다시 시도해 주세요.',
	uploadSizeMismatch: '파일이 끝까지 올라가지 않았어요. 다시 시도해 주세요.',
	uploadCancelled: '업로드를 취소했어요.',
	uploadUnreachable:
		'파일을 올릴 곳에 연결하지 못했어요. 인터넷 연결을 확인하고 다시 시도해 주세요.',
	uploadStartFailed: '업로드를 시작하지 못했어요. 다시 시도해 주세요.',
	flickUnreachable: 'Flick에 연결하지 못했어요. 인터넷 연결을 확인하고 다시 시도해 주세요.',
	unreadableSecret: '받은 내용을 읽을 수 없어요.',
	managementUnavailable: '관리 링크를 사용할 수 없어요.',
	deliveryStatusUnverified: '링크 상태를 확인하지 못했어요. 다시 시도해 주세요.',
	deliveryStatusUnreachable:
		'링크 상태를 새로고침하지 못했어요. 인터넷 연결을 확인하고 다시 시도해 주세요.',
	notCancellable: '이 링크는 더 이상 취소할 수 없어요.',
	tooManyStatusChecks: '상태를 너무 자주 확인했어요. 잠시 후 다시 시도해 주세요.',
	page: {
		notFoundTitle: '페이지를 찾을 수 없음',
		errorTitle: (status: number) => `오류 ${status}`,
		label: (status: number) => `오류 ${status}`,
		notFoundHeading: '여기엔 아무것도 없어요.',
		failedHeading: '문제가 생겼어요.',
		notFoundBody:
			'이 주소에는 페이지가 없어요. 링크에 오타가 없는지 확인하거나, 첫 화면으로 돌아가 새로 시작해 주세요.',
		failedBody: '예기치 않은 오류로 페이지가 멈췄어요. 보통은 첫 화면에서 다시 시작하면 해결돼요.',
		cta: '링크 만들기'
	}
};
