import type { create as source } from '../en/create';

export const create: typeof source = {
	headTitle: 'リンクを作成 - Flick',
	tagline: 'エンドツーエンド暗号化 · 開くと消えます',
	titleLink: 'Create a secret',
	titleCode: 'Send a secret',
	receivedCode: '6桁のコードを受け取りましたか？',
	enterItHere: 'こちらで入力',
	needOne: '相手から受け取りたいときは',
	requestSecret: 'リクエストする',
	typeTablist: '送る内容の種類',
	types: {
		text: 'メモ',
		file: 'ファイル',
		login: 'ログイン',
		card: 'カード',
		identity: '個人情報',
		custom: 'カスタム'
	},
	text: {
		label: '内容',
		placeholder: 'テキストを貼り付け'
	},
	files: {
		label: 'ファイル',
		dropLead: 'ファイルをドロップ',
		dropRest: '、またはクリックして選択',
		limitLead: '複数のファイルやフォルダーは1つに圧縮されます · 最大 ',
		addFiles: 'アップロードするファイルを追加',
		chooseFolder: 'フォルダーを選択',
		removeFolder: (name: string) => `フォルダー ${name} を削除`,
		removeFile: (path: string) => `${path} を削除`,
		count: (n: number) => `${n}件のファイル`,
		zipping: (n: number) => `${n}件のファイルを圧縮しています…`,
		tooLarge: (size: string) => `ファイルは合計${size}まで選べます。`
	},
	passphrase: {
		toggle: 'パスワードをかける',
		infoLabel: 'サーバーが内容を見られない理由',
		whyTitle: 'Flickが内容を読めない理由',
		whyLocked: '送る前にこの端末で暗号化されます。',
		whyNeverSent: 'パスワードがサーバーに送られることはありません。',
		placeholder: 'パスワードを入力',
		fieldLabel: 'パスワード',
		generate: 'パスワードを生成',
		hide: 'パスワードを隠す',
		show: 'パスワードを表示'
	},
	expiresAfter: '有効時間',
	lifetimeGroup: '有効時間の選択',
	cta: {
		creating: 'Creating',
		createLink: 'Create Link',
		startLive: 'Start Live Transfer'
	},
	linkCaption: '一度開くと消えます',
	liveCaption: 'サーバーには保存されません',
	blocked: {
		passphrase: 'かけるパスワードを入力してください',
		empty: '先にメモ、ファイル、認証情報のいずれかを入力してください'
	},
	stage: {
		caption: 'リンクを準備中',
		ready: 'リンクの準備ができました'
	},
	status: {
		encrypting: 'この端末で暗号化しています…',
		creatingLink: 'リンクを作成しています…',
		preparingUpload: '暗号化したファイルのアップロードを準備しています…',
		finalizing: 'リンクを仕上げています…',
		uploading: '暗号化したファイルをアップロードしています…',
		uploadingProgress: (loaded: string, total: string, percent: number) =>
			`暗号化したファイルをアップロード中 · ${loaded} / ${total} (${percent}%)`,
		uploadCancelled: 'アップロードをキャンセルしました。',
		createFailed: 'リンクを作成できませんでした。もう一度お試しください。',
		zipFailed: 'ファイルを圧縮できませんでした。もう一度お試しください。',
		folderEmpty: 'このフォルダーは空です。',
		folderReadFailed: 'フォルダーを読み込めませんでした。',
		skipped: (n: number) => `読み込めない項目を${n}件スキップしました。`
	},
	result: {
		oneTimeLink: '1回限りのリンク',
		title: 'リンクを作成しました',
		opensOnce: '一度開くと消えます。早めに送ってください。',
		expiresIn: '残り時間',
		openManagement: '自分用の管理ページを開く',
		showQr: 'QRを表示',
		passphraseTitle: 'パスワードは別の方法で送ってください。',
		passphraseBody: '受け取る人が開くには、リンクとパスワードの両方が必要です。',
		linkOnlyTitle: '受け取る人にだけ共有してください。',
		linkOnlyBody: 'このリンク全体を知っている人なら誰でも1回だけ開けます。',
		createAnother: 'もう1つ作成'
	},
	guideNav: 'ガイド',
	howItWorks: 'Flickの仕組み'
};
