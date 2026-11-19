import type { create as source } from '../en/create';

export const create: typeof source = {
	headTitle: '링크 만들기 - Flick',
	tagline: '종단간 암호화 · 열면 사라져요',
	titleLink: 'Create a secret',
	titleCode: 'Send a secret',
	receivedCode: '6자리 코드를 받았나요?',
	enterItHere: '여기에 입력',
	needOne: '다른 사람에게서 받아야 하나요?',
	requestSecret: '요청하기',
	typeTablist: '보낼 내용 종류',
	types: {
		text: '메모',
		file: '파일',
		login: '로그인',
		card: '카드',
		identity: '개인 정보',
		custom: '직접 입력'
	},
	text: {
		label: '내용',
		placeholder: '텍스트를 붙여 넣으세요'
	},
	files: {
		label: '파일',
		dropLead: '파일을 끌어다 놓거나',
		dropRest: ' 눌러서 고르세요',
		limitLead: '여러 파일과 폴더는 하나로 압축돼요 · 최대 ',
		addFiles: '올릴 파일 추가',
		chooseFolder: '폴더 선택',
		removeFolder: (name: string) => `${name} 폴더 빼기`,
		removeFile: (path: string) => `${path} 빼기`,
		count: (n: number) => `파일 ${n}개`,
		zipping: (n: number) => `파일 ${n}개 압축 중…`,
		tooLarge: (size: string) => `파일은 합쳐서 ${size}까지 고를 수 있어요.`
	},
	passphrase: {
		toggle: '비밀번호 걸기',
		infoLabel: '서버가 이 내용을 볼 수 없는 이유',
		whyTitle: 'Flick도 내용을 볼 수 없는 이유',
		whyLocked: '보내기 전에 이 기기에서 잠가요.',
		whyNeverSent: '비밀번호는 서버로 전송되지 않아요.',
		placeholder: '비밀번호 입력',
		fieldLabel: '비밀번호',
		generate: '비밀번호 만들기',
		hide: '비밀번호 숨기기',
		show: '비밀번호 보기'
	},
	expiresAfter: '만료 시간',
	lifetimeGroup: '만료 시간',
	cta: {
		creating: 'Creating',
		createLink: 'Create Link',
		startLive: 'Start Live Transfer'
	},
	linkCaption: '한 번 열면 사라져요',
	liveCaption: '서버에 저장하지 않아요',
	blocked: {
		passphrase: '걸어 둘 비밀번호를 입력해 주세요',
		empty: '메모, 파일, 로그인 정보 중 하나를 먼저 넣어 주세요'
	},
	stage: {
		caption: '링크 준비 중',
		ready: '링크 준비 완료'
	},
	status: {
		encrypting: '브라우저에서 암호화하는 중…',
		creatingLink: '링크를 만드는 중…',
		preparingUpload: '암호화한 파일을 올릴 준비 중…',
		finalizing: '링크를 마무리하는 중…',
		uploading: '암호화한 파일을 올리는 중…',
		uploadingProgress: (loaded: string, total: string, percent: number) =>
			`암호화한 파일을 올리는 중 · ${total} 중 ${loaded} (${percent}%)`,
		uploadCancelled: '업로드를 취소했어요.',
		createFailed: '링크를 만들지 못했어요. 다시 시도해 주세요.',
		zipFailed: '파일을 압축하지 못했어요. 다시 시도해 주세요.',
		folderEmpty: '빈 폴더예요.',
		folderReadFailed: '폴더를 읽지 못했어요.',
		skipped: (n: number) => `읽을 수 없는 항목 ${n}개를 건너뛰었어요.`
	},
	result: {
		oneTimeLink: '한 번만 열리는 링크',
		title: '링크를 만들었어요',
		opensOnce: '한 번 열면 사라져요. 서둘러 전달해 주세요.',
		expiresIn: '남은 시간',
		openManagement: '비공개 관리 페이지 열기',
		showQr: 'QR 보기',
		passphraseTitle: '비밀번호는 따로 보내 주세요.',
		passphraseBody: '받는 사람이 열려면 링크와 비밀번호가 모두 필요해요.',
		linkOnlyTitle: '받는 사람에게만 공유해 주세요.',
		linkOnlyBody: '이 링크 전체를 가진 사람은 누구나 한 번 열 수 있어요.',
		createAnother: '하나 더 만들기'
	},
	guideNav: '안내',
	howItWorks: 'Flick 작동 방식'
};
