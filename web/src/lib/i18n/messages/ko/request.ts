import type { request as source } from '../en/request';

export const request: typeof source = {
	shared: {
		badge: '기기에서 암호화 · 한 번만 받는 요청',
		timeRemaining: '남은 시간',
		statusUnknown: '요청 상태를 알 수 없어요.'
	},
	create: {
		pageTitle: '요청하기 - Flick',
		headerLink: '보내기',
		heading: '링크로 요청하기',
		intro: '상대가 메모, 파일, 로그인 정보를 한 번 보낼 수 있는 링크를 만들어요.',
		noteLabel: '메모',
		optional: '선택',
		notePlaceholder: '보내 줄 내용을 적어 주세요 (예: Wi-Fi 비밀번호, SSH 키)',
		noteTooLong: (max: string, used: string) =>
			`메모는 ${max}바이트까지 쓸 수 있어요 (지금 ${used}바이트).`,
		noteHint: '암호화되어 요청 링크를 받은 사람에게만 보여요.',
		lifetimeGroup: '요청 만료 시간',
		lifetimeHint: '링크를 만든 순간부터 줄어들어요. 상대가 보내도 늘어나지 않아요.',
		unconfirmed:
			'요청이 만들어졌는지 확인하지 못했어요. 만들어졌더라도 링크는 되찾을 수 없고, 시간이 지나면 저절로 사라져요. 필요하면 새로 만들어 주세요.',
		unsupported:
			'이 브라우저에서는 요청을 만들 수 없어요. 최신 브라우저로 https 주소에 접속해 주세요.',
		creating: '요청 만드는 중…',
		createNew: '새 요청 만들기',
		create: '요청 링크 만들기'
	},
	submit: {
		pageTitle: '보내기 - Flick',
		headingAccepted: '보냈어요',
		headingExpired: '요청이 만료됐어요',
		headingUnavailable: '이 요청에는 보낼 수 없어요',
		headingReady: '요청받은 내용 보내기',
		intro: '보낸 내용은 요청한 사람만 열 수 있어요.',
		resumed: '페이지를 떠나거나 새로고침해서 보내던 내용은 사라졌어요. 이미 도착했을 수도 있어요.',
		acceptedBody: '요청한 사람만 열 수 있어요.',
		expiredBody: '요청한 사람에게 새 링크를 달라고 하세요.',
		unavailableBody:
			'이 링크로는 더 이상 보낼 수 없어요. 아직 보낼 것이 있다면 요청한 사람에게 새 링크를 달라고 하세요.',
		outcomeRegion: '보낸 결과',
		outcomeUnknown: '도착했는지 알 수 없어요.',
		outcomeHint:
			'이 탭을 열어 둔 채, 다시 보내기 전에 도착했는지 확인하세요. 다시 보내면 같은 내용을 한 번 더 보내고, 페이지를 떠나거나 새로고침하면 그 내용은 사라져요.',
		reservationRemaining: (minutes: number, seconds: number) =>
			`올릴 수 있는 시간: ${minutes}분 ${seconds}초 남음.`,
		checkSubmission: '도착 확인',
		cancelUpload: '업로드 취소',
		retryCancellation: '취소 다시 시도',
		cancelHint:
			'다시 보내기 전에 취소가 끝났는지 확인해야 해요. 올리기를 멈춰도 이미 올라간 부분은 바로 지워지지 않고 나중에 정리돼요.',
		retryReady: '아직 도착하지 않았어요. 같은 내용을 다시 보낼 수 있어요.',
		retrySame: '같은 내용 다시 보내기',
		requesterNote: '요청한 사람의 메모',
		messageLabel: '메시지',
		fileLabel: '파일',
		fileLimit: (size: string) => `파일 1개, 최대 ${size}.`,
		encrypting: '암호화하는 중…',
		submit: '암호화해서 보내기',
		loading: '요청을 불러오는 중…',
		retryLoading: '요청 다시 불러오기',
		progress: {
			cancelling: '올리기 취소 중…',
			reserving: '파일을 올릴 준비 중…',
			verifying: '도착했는지 확인하는 중… 아직 도착을 확인하지 못했어요.',
			uploadingBytes: (loaded: string, total: string, percent: number) =>
				`암호화한 파일을 올리는 중: ${total}바이트 중 ${loaded}바이트 (${percent}%). 도착했는지는 다 올린 뒤 확인해요.`,
			uploading: '암호화한 파일을 올리는 중…',
			submitting: '암호화한 내용을 보내는 중…'
		},
		errors: {
			reservationExpired:
				'올릴 수 있는 시간이 지났어요. 다른 파일을 고르기 전에 방금 보낸 것이 도착했는지 확인하세요.',
			keyMismatch: '이 링크가 요청과 맞지 않아요. 요청한 사람에게 새 링크를 달라고 하세요.',
			loadFailed: '요청을 불러오지 못했어요. 다시 시도해 주세요.',
			noteMismatch:
				'요청 메모가 이 링크와 맞지 않아요. 요청한 사람에게 링크 전체를 다시 달라고 하세요.',
			noteDecryptFailed:
				'요청한 사람의 메모를 열지 못했어요. 요청한 사람에게 링크 전체를 다시 달라고 하세요.',
			submitUnconfirmed: '도착했는지 확인하지 못했어요.',
			fillField: '항목을 하나 이상 채워 주세요.',
			sizeRange: (max: string) => `1바이트에서 ${max}바이트 사이의 내용을 골라 주세요.`,
			encryptFailed:
				'내용을 암호화하지 못했어요. 파일 이름과 크기를 확인하고, 최신 브라우저로 https 주소에 접속해 주세요.',
			verifyFailed: '도착했는지 확인하지 못했어요.',
			noWebCrypto: '이 브라우저에서는 보낼 수 없어요. 최신 브라우저로 https 주소에 접속해 주세요.',
			invalidLink: '잘못된 링크예요. 요청한 사람에게 링크 전체를 다시 달라고 하세요.'
		},
		notices: {
			acceptedBeforeCancel:
				'취소하기 전에 이미 도착했어요. 도착한 내용은 요청한 사람만 취소할 수 있어요.',
			attemptRenewed: '이전에 올리던 파일은 멈췄어요. 다시 보내려면 파일을 고르세요.',
			cancelNotConfirmed: '취소됐는지 확인하지 못했어요. 취소를 다시 눌러 주세요.',
			instructionExpired:
				'올릴 수 있는 시간이 지났어요. 다른 파일을 고르기 전에 지금 올리던 것을 취소하세요.',
			cancelUnconfirmed:
				'취소됐는지 확인하지 못했어요. 이 탭을 열어 둔 채 도착했는지 확인하세요. 올리기를 멈춰도 이미 올라간 부분은 바로 지워지지 않아요.'
		}
	},
	receive: {
		pageTitle: '내 요청 - Flick',
		badge: '요청한 사람 전용 페이지',
		headingExpired: '요청이 만료됐어요',
		headingUnavailable: '요청을 쓸 수 없어요',
		headingOpened: '한 번 열었어요',
		headingOpenLost: '열기 결과를 알 수 없어요',
		headingDecryptFailed: '내용을 열지 못했어요',
		headingReady: '받을 준비가 됐어요',
		headingConsumed: '이미 열었어요',
		headingCancelled: '요청이 취소됐어요',
		headingWaiting: '상대가 보내기를 기다리는 중이에요',
		headingStatus: '요청 상태',
		statusRegion: '요청 상태',
		expiredBody: '요청과 여기 표시되던 내용은 더 이상 볼 수 없어요.',
		unavailableBody: '링크가 잘못됐거나 만료됐어요. 저장해 둔 내 받기 링크 전체로 다시 여세요.',
		lastKnown: '마지막으로 확인한 상태: ',
		uploading: '파일을 올리는 중이에요. 아직 열 수 없어요.',
		waiting: '상대가 보내기를 기다리는 중이에요.',
		accepted: '도착했어요.',
		openOnce: '한 번 열 수 있어요.',
		cancelled: '더 이상 보내거나 열 수 없어요.',
		saveNow: '지금 저장하세요. 페이지를 떠나거나 새로고침하거나 기한이 지나면 완전히 사라져요.',
		consumed: '이미 한 번 열려서 다시 열 수 없어요.',
		checking: '자동으로 확인하는 중',
		loadingStatus: '요청 상태를 불러오는 중…',
		refresh: '상태 새로고침',
		openLost: '여는 도중에 연결이 끊겼어요. 이미 열린 것으로 처리됐을 수 있어서 다시 열지 않아요.',
		decryptFailed:
			'내용은 받았지만 열지 못했어요. 다시 열 수 없으니 새 요청을 만들어 다시 보내 달라고 하세요.',
		decryptedSecret: '받은 내용',
		download: (filename: string) => `${filename} 다운로드`,
		working: '처리하는 중…',
		open: '한 번만 열기',
		shareHeading: '상대에게 줄 링크',
		shareHint:
			'이 링크를 가진 사람은 누구나 한 번 보낼 수 있어요. 이 링크로는 받은 내용을 열거나 요청을 취소할 수 없어요.',
		submissionLink: '상대에게 줄 링크',
		copySubmissionLink: '상대에게 줄 링크 복사',
		showSubmissionQr: '상대에게 줄 링크 QR 보기',
		submissionGone: '상대에게 줄 링크는 여기서 다시 보여 줄 수 없어요. 저장해 둔 링크를 쓰세요.',
		privateHeading: '내 받기 링크는 나만 보관하세요',
		privateBody:
			'받은 내용은 이 링크로만 열 수 있어요. 여기로 돌아오거나 다른 기기에서 열려면 저장해 두세요. 링크를 가진 사람은 누구나 받은 내용을 열거나 요청을 취소할 수 있어요. 브라우저 기록, 동기화, 클립보드 관리 앱, 스크린샷에 사본이 남을 수 있어요.',
		privateLink: '내 받기 링크',
		copyPrivateLink: '내 받기 링크 복사',
		cancelRequest: '요청 취소',
		createAnother: '새 요청 만들기',
		confirmTitle: '이 요청을 취소할까요?',
		confirmBody: '더 이상 보내거나 열 수 없어요. 이미 연 내용은 되돌릴 수 없어요.',
		errors: {
			missingKey:
				'링크 뒷부분이 빠졌어요. 저장해 둔 내 받기 링크 전체로 다시 여세요. 링크를 잃어버리면 Flick도 받은 내용을 열 수 없어요.',
			invalidKey:
				'링크 뒷부분이 잘못됐어요. 저장해 둔 내 받기 링크 전체로 다시 여세요. 아직 아무것도 열리지 않았어요.',
			statusFailed: '요청 상태를 확인하지 못했어요. 다시 시도해 주세요.',
			limitsFailed:
				'열 수 있는 크기를 확인하지 못했어요. 열기 전에 상태를 새로고침하세요. 아직 아무것도 열리지 않았어요.'
		},
		notices: {
			changedBeforeCancel: '취소하기 전에 요청이 바뀌었어요. 지금 서버의 상태를 보여 드려요.',
			notReleased: '내용을 받지 못했어요. 다른 탭에서 요청이 바뀌었을 수 있어요.'
		}
	},
	api: {
		invalidResponse: 'Flick에서 받은 응답을 읽지 못했어요.',
		linkUnavailable: '요청 링크를 쓸 수 없어요.',
		offline: '오프라인이거나 연결이 끊겼어요. 연결을 확인해 주세요.',
		unavailable: '요청을 쓸 수 없어요. 링크가 잘못됐거나 만료됐을 수 있어요.',
		conflict: '요청이 바뀌었어요. 상태를 새로고침하세요.',
		tooLarge: '내용이 허용 크기를 넘었어요.',
		rateLimited: '시도가 너무 많아요. 잠시 후 다시 시도해 주세요.',
		storageFull: '저장 공간이 가득 찼어요. 나중에 다시 시도해 주세요.',
		serverUnavailable: '서버를 잠시 쓸 수 없어요. 나중에 다시 시도해 주세요.',
		reservationExpired:
			'올릴 수 있는 시간이 지났어요. 다른 파일을 고르기 전에 도착했는지 확인하거나 취소하세요.'
	}
};
