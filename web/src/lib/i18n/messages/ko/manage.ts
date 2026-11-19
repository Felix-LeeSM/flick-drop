import type { manage as source } from '../en/manage';

export const manage: typeof source = {
	headTitle: '보낸 링크 관리',
	statuses: {
		pendingUpload: {
			title: '올리는 중',
			detail: '파일을 아직 다 올리지 못했어요. 지금 취소할 수 있어요.'
		},
		active: {
			title: '열 수 있음',
			detail: '받는 사람이 만료 전에 한 번 열 수 있어요.'
		},
		locked: {
			title: '잠김',
			detail: '비밀번호가 다섯 번 틀려서 더 이상 열 수 없어요. 받는 사람이 열었다는 뜻은 아니에요.'
		},
		cancelled: {
			title: '취소됨',
			detail:
				'받는 사람 링크로 더 이상 열 수 없어요. 이미 연 사람이 가진 내용은 취소해도 되돌릴 수 없어요.'
		},
		unavailable: {
			title: '열 수 없음',
			detail: '파일은 다 올렸지만 열 수 있는 내용이 만들어지지 않았어요.'
		}
	},
	leavePrompt:
		'아직 받는 사람 링크를 복사하거나 공유하지 않았어요. 이 페이지를 떠나면 다시 볼 수 없어요. 그래도 떠날까요?',
	changedBeforeCancel: '취소하기 전에 링크 상태가 바뀌었어요. 지금 상태를 아래에 보여 드려요.',
	noLongerCancellable: '이 링크는 더 이상 취소할 수 없어요.',
	privateSenderPage: '보낸 사람 전용 페이지',
	titleCreated: '링크를 만들었어요',
	titleManage: '링크 관리',
	intro: '링크가 열렸는지 확인하고, 열리기 전이면 취소할 수 있어요.',
	expired: '관리 링크가 만료됐어요.',
	statusRegion: '링크 상태',
	lastKnown: '마지막으로 확인한 상태: ',
	loadingStatus: '링크 상태를 불러오는 중…',
	unknownStatus: '링크 상태를 알 수 없어요.',
	refresh: '상태 새로고침',
	sendToRecipient: '받는 사람에게 보내기',
	copyRecipientLink: '받는 사람 링크 복사',
	showQr: 'QR 보기',
	sendPassphraseSeparately:
		'비밀번호는 따로 보내 주세요. 받는 사람에게는 링크와 비밀번호가 모두 필요해요.',
	anyoneWithLink:
		'이 전체 링크를 가진 사람은 누구나 한 번 열 수 있어요. 받는 사람에게만 공유하세요.',
	recipientLinkOnlyOriginal:
		'받는 사람 링크는 처음 만든 화면에서만 볼 수 있어요. 저장해 둔 링크를 쓰거나 새로 만드세요.',
	keepPrivate: '관리 링크는 나만 보관하세요',
	keepPrivateDetail:
		'이 링크를 가진 사람은 누구나 받는 사람 링크가 열리거나 만료되기 전까지 상태를 확인하고 취소할 수 있어요. 잃어버리면 되찾을 수 없으니 지금 저장하세요. 이 링크로 내용을 열거나 받는 사람 링크를 되찾을 수는 없어요.',
	privateLinkLabel: '비공개 관리 링크',
	copyManagementLink: '관리 링크 복사',
	cancelDelivery: '링크 취소',
	createAnother: '하나 더 만들기',
	confirmTitle: '이 링크를 취소할까요?',
	confirmDescription:
		'받는 사람 링크가 더 이상 열리지 않아요. 이미 연 사람이 가진 내용은 되돌릴 수 없어요.',
	keepIt: '그대로 두기',
	remaining: (time: string) => `${time} 남음`
};
