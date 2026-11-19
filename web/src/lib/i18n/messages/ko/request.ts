import type { request as source } from '../en/request';

export const request: typeof source = {
	shared: {
		badge: '종단간 암호화 · 일회용 요청',
		timeRemaining: '남은 시간',
		statusUnknown: '요청 상태를 알 수 없어요.'
	},
	create: {
		pageTitle: '비밀 요청하기 - Flick',
		headerLink: 'Send a secret',
		heading: 'Request a secret',
		intro: '상대가 메모, 파일, 로그인 정보를 한 번 보낼 수 있는 링크를 만들어요.',
		noteLabel: '메모',
		optional: '선택',
		notePlaceholder: '보내 줄 내용을 적어 주세요 (예: Wi-Fi 비밀번호, SSH 키)',
		noteTooLong: (max: string, used: string) =>
			`메모는 ${max}바이트까지 쓸 수 있어요 (지금 ${used}바이트).`,
		noteHint: '종단간 암호화되며, 요청 링크를 여는 사람에게만 보여요.',
		lifetimeGroup: '요청 만료 시간',
		lifetimeHint: '링크를 만든 순간부터 시작돼요. 제출해도 늘어나지 않아요.',
		unconfirmed:
			'요청이 만들어졌는지 확인하지 못했어요. 쓰이지 않은 요청이 남아 있을 수 있지만 시간이 지나면 만료돼요. 그 링크는 되찾을 수 없고, 자동으로 다시 시도하지 않아요.',
		unsupported: '요청 암호화를 쓸 수 없어요. 최신 브라우저에서 HTTPS나 localhost로 접속해 주세요.',
		creating: 'Creating request…',
		createNew: 'Create a new request',
		create: 'Create request'
	},
	submit: {
		pageTitle: '비밀 제출하기 - Flick',
		headingAccepted: '제출이 접수됐어요',
		headingExpired: '요청이 만료됐어요',
		headingUnavailable: '요청을 쓸 수 없어요',
		headingReady: 'Send a secret',
		intro: '보낸 내용은 요청한 사람만 열 수 있어요.',
		resumed:
			'페이지를 떠나거나 새로고침해서 메모리에 있던 이전 시도는 버려졌어요. 이미 접수됐을 수도 있어요.',
		acceptedBody: '요청한 사람만 열 수 있어요.',
		expiredBody: '요청한 사람에게 새 링크를 달라고 하세요.',
		unavailableBody:
			'이 링크로는 더 이상 제출할 수 없어요. 아직 보낼 것이 있다면 요청한 사람에게 새 링크를 달라고 하세요.',
		outcomeRegion: '제출 결과',
		outcomeUnknown: '제출 결과를 알 수 없어요.',
		outcomeHint:
			'이 탭을 열어 둔 채, 다시 시도하기 전에 제출 상태를 확인하세요. 다시 시도하면 같은 암호화된 내용을 다시 보내고, 페이지를 떠나거나 새로고침하면 그 내용은 버려져요.',
		reservationRemaining: (minutes: number, seconds: number) =>
			`업로드 예약: ${minutes}분 ${seconds}초 남음.`,
		checkSubmission: '제출 확인',
		cancelUpload: '업로드 취소',
		retryCancellation: '취소 다시 시도',
		cancelHint:
			'다른 시도를 하기 전에 서버가 취소를 확인해야 해요. 전송을 멈춰도 이미 올라간 데이터는 지워지지 않아요. 서버가 나중에 정리해요.',
		retryReady: '서버가 이 시도를 접수하지 않았어요. 같은 제출을 다시 시도할 수 있어요.',
		retrySame: '같은 제출 다시 시도',
		requesterNote: '요청한 사람의 메모',
		messageLabel: '메시지',
		fileLabel: '파일',
		fileLimit: (size: string) => `파일 1개, 최대 ${size}.`,
		encrypting: 'Encrypting…',
		submit: 'Encrypt and submit',
		loading: '요청을 불러오는 중…',
		retryLoading: '요청 다시 불러오기',
		progress: {
			cancelling: '업로드 취소 중…',
			reserving: '암호화된 업로드를 예약하는 중…',
			verifying: '업로드 확인 중… 아직 접수가 확인되지 않았어요.',
			uploadingBytes: (loaded: string, total: string, percent: number) =>
				`암호화된 파일 업로드 중: ${total}바이트 중 ${loaded}바이트 (${percent}%). 접수 여부는 확인이 필요해요.`,
			uploading: '암호화된 파일 업로드 중…',
			submitting: '암호화된 내용 제출 중…'
		},
		errors: {
			reservationExpired:
				'업로드 예약이 만료됐어요. 다른 파일을 고르기 전에 이번 시도를 확인하세요.',
			keyMismatch:
				'요청 열쇠가 이 링크와 맞지 않아요. 요청한 사람에게 새 제출 링크를 달라고 하세요.',
			loadFailed: '요청을 불러오지 못했어요. 다시 시도해 주세요.',
			noteMismatch:
				'요청 메모가 이 링크와 맞지 않아요. 요청한 사람에게 전체 제출 링크를 달라고 하세요.',
			noteDecryptFailed:
				'요청한 사람의 메모를 복호화하지 못했어요. 요청한 사람에게 전체 제출 링크를 달라고 하세요.',
			submitUnconfirmed: '제출이 확인되지 않았어요.',
			fillField: '항목을 하나 이상 채워 주세요.',
			sizeRange: (max: string) => `1바이트에서 ${max}바이트 사이의 내용을 골라 주세요.`,
			encryptFailed:
				'내용을 암호화하지 못했어요. 파일 이름과 크기를 확인하고, 최신 브라우저에서 HTTPS나 localhost로 접속해 주세요.',
			verifyFailed: '제출 상태를 확인하지 못했어요.',
			noWebCrypto:
				'요청 암호화에는 Web Crypto를 지원하는 브라우저와 HTTPS 또는 localhost 접속이 필요해요.',
			invalidLink: '잘못된 제출 링크예요. 요청한 사람에게 전체 링크를 달라고 하세요.'
		},
		notices: {
			acceptedBeforeCancel:
				'취소하기 전에 서버가 제출을 접수했어요. 접수된 내용은 요청한 사람만 취소할 수 있어요.',
			attemptRenewed: '이전 업로드 시도가 끝났어요. 새로 시도하려면 파일을 고르세요.',
			cancelNotConfirmed: '취소가 확인되지 않았어요. 같은 시도로 취소를 다시 시도하세요.',
			instructionExpired:
				'업로드 주소가 만료됐어요. 다른 파일을 고르기 전에 이번 시도를 취소하세요.',
			cancelUnconfirmed:
				'취소가 확인되지 않았어요. 이 탭을 열어 둔 채 시도를 확인하세요. 전송을 멈춰도 이미 올라간 데이터는 지워지지 않아요.'
		}
	},
	receive: {
		pageTitle: '비공개 받기 - Flick',
		badge: '요청한 사람 전용 페이지',
		headingExpired: '요청이 만료됐어요',
		headingUnavailable: '요청을 쓸 수 없어요',
		headingOpened: '한 번 열었어요',
		headingOpenLost: '열기 결과를 알 수 없어요',
		headingDecryptFailed: '복호화하지 못했어요',
		headingReady: '받을 준비가 됐어요',
		headingConsumed: '이미 열린 요청이에요',
		headingCancelled: '요청이 취소됐어요',
		headingWaiting: '제출을 기다리는 중이에요',
		headingStatus: '요청 상태',
		statusRegion: '요청 상태',
		expiredBody: '요청과 여기 표시되던 내용은 더 이상 볼 수 없어요.',
		unavailableBody: '링크가 잘못됐거나 만료됐어요. 비공개 받기 링크 전체를 사용하세요.',
		lastKnown: '마지막으로 확인한 상태: ',
		uploading: '파일을 올리는 중이에요. 아직 열 수 없어요.',
		waiting: '첫 제출을 기다리는 중이에요.',
		accepted: '제출이 접수됐어요.',
		openOnce: '한 번 열 수 있어요.',
		cancelled: '더 이상 제출하거나 열 수 없어요.',
		saveNow: '지금 저장하세요. 페이지를 떠나거나 새로고침하거나 기한이 지나면 완전히 사라져요.',
		consumed: '이미 한 번 열려서 다시 열 수 없어요.',
		checking: '자동으로 확인하는 중',
		loadingStatus: '요청 상태를 불러오는 중…',
		refresh: '상태 새로고침',
		openLost:
			'열기 응답을 받지 못했거나 응답이 잘못됐어요. 서버에서는 이미 열린 것으로 처리됐을 수 있고, 이 페이지는 다시 시도하지 않아요.',
		decryptFailed:
			'내용을 받았지만 복호화에 실패했어요. 다시 열 수 없어요. 보낸 사람에게 새 요청으로 다시 보내 달라고 하세요.',
		decryptedSecret: '복호화한 비밀',
		download: (filename: string) => `${filename} 다운로드`,
		working: 'Working…',
		open: 'Open once',
		shareHeading: '제출 링크 공유',
		shareHint:
			'이 링크를 가진 누구나 한 번 제출할 수 있어요. 이 링크로는 요청을 열거나 취소할 수 없어요.',
		submissionLink: '제출 링크',
		copySubmissionLink: '제출 링크 복사',
		showSubmissionQr: '제출 QR 보기',
		submissionGone: '저장해 둔 제출 링크를 쓰세요. 여기서는 다시 보여 줄 수 없어요.',
		privateHeading: '받기 링크는 나만 보관하세요',
		privateBody:
			'이 링크에는 복호화 열쇠가 들어 있어요. 여기로 돌아오거나 다른 기기에서 열려면 저장해 두세요. 링크가 없으면 제출된 내용을 열 수 없어요. 링크를 가진 누구나 요청을 열거나 취소할 수 있어요. 브라우저 기록, 동기화, 클립보드 관리 앱, 스크린샷에 사본이 남을 수 있어요.',
		privateLink: '비공개 받기 링크',
		copyPrivateLink: '비공개 받기 링크 복사',
		cancelRequest: '요청 취소',
		createAnother: '새 요청 만들기',
		confirmTitle: '이 요청을 취소할까요?',
		confirmBody: '더 이상 제출하거나 열 수 없어요. 이미 열린 내용은 되돌릴 수 없어요.',
		errors: {
			missingKey:
				'비공개 열쇠가 없어요. 비공개 받기 링크 전체로 다시 여세요. Flick은 잃어버린 열쇠를 되찾을 수 없고, 열쇠 없이는 복호화할 수 없어요.',
			invalidKey:
				'비공개 열쇠가 잘못됐어요. 처음 받은 비공개 받기 링크 전체로 다시 여세요. 아무것도 열리지 않았어요. Flick은 없거나 손상된 열쇠를 되찾을 수 없어요.',
			statusFailed: '요청 상태를 확인하지 못했어요. 다시 시도해 주세요.',
			limitsFailed:
				'크기 제한을 확인하지 못했어요. 열기 전에 상태를 새로고침하세요. 아직 아무것도 열리지 않았어요.'
		},
		notices: {
			changedBeforeCancel: '취소하기 전에 요청이 바뀌었어요. 지금 서버의 상태를 보여 드려요.',
			notReleased:
				'서버가 이번 열기에 내용을 내주지 않았어요. 다른 탭에서 요청이 바뀌었을 수 있어요.'
		}
	},
	api: {
		invalidResponse: '서버 응답을 확인하지 못했어요.',
		linkUnavailable: '요청 링크를 쓸 수 없어요.',
		offline: '오프라인이거나 연결이 끊겼어요. 연결을 확인해 주세요.',
		unavailable: '요청을 쓸 수 없어요. 링크가 잘못됐거나 만료됐을 수 있어요.',
		conflict: '요청이 바뀌었어요. 상태를 새로고침하세요.',
		tooLarge: '내용이 허용 크기를 넘었어요.',
		rateLimited: '시도가 너무 많아요. 잠시 후 다시 시도해 주세요.',
		storageFull: '저장 공간이 가득 찼어요. 나중에 다시 시도해 주세요.',
		serverUnavailable: '서버를 잠시 쓸 수 없어요. 나중에 다시 시도해 주세요.',
		reservationExpired:
			'업로드 예약이 만료됐어요. 다른 파일을 고르기 전에 이번 시도를 확인하거나 취소하세요.'
	}
};
