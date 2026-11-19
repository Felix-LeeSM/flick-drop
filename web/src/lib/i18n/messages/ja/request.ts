import type { request as source } from '../en/request';

export const request: typeof source = {
	shared: {
		badge: 'エンドツーエンド暗号化 · 1回限りのリクエスト',
		timeRemaining: '残り時間',
		statusUnknown: 'リクエストの状態が分かりません。'
	},
	create: {
		pageTitle: 'リクエストする - Flick',
		headerLink: 'Send a secret',
		heading: 'Request a secret',
		intro: '相手がメモ・ファイル・認証情報を1回だけ送れるリンクを作成します。',
		noteLabel: 'メモ',
		optional: '任意',
		notePlaceholder: '送ってほしい内容を書いてください（例：Wi-Fiのパスワード、SSH鍵）',
		noteTooLong: (max: string, used: string) =>
			`メモは${max}バイト以内にしてください（現在${used}バイト）。`,
		noteHint: '暗号化され、相手に渡すリンクを開いた人にだけ表示されます。',
		lifetimeGroup: 'リクエストの有効時間',
		lifetimeHint: 'リンクを作成した時点から減り始めます。相手が送っても延長されません。',
		unconfirmed:
			'リクエストを作成できたか確認できませんでした。作成されていてもリンクは取り戻せず、期限が来ると自動で消えます。自動で作成し直すことはないため、必要なら新しく作成してください。',
		unsupported:
			'このブラウザではリクエストを作成できません。最新のブラウザで https:// から始まるアドレスを開いてください。',
		creating: 'Creating request…',
		createNew: 'Create a new request',
		create: 'Create request'
	},
	submit: {
		pageTitle: '内容を送る - Flick',
		headingAccepted: '送りました',
		headingExpired: 'リクエストの期限が切れました',
		headingUnavailable: 'このリクエストには送れません',
		headingReady: 'Send a secret',
		intro: '送った内容は依頼者だけが開けます。',
		resumed:
			'ページを離れたか再読み込みしたため、送っていた内容は消えました。すでに相手に届いている可能性があります。',
		acceptedBody: '依頼者だけが開けます。',
		expiredBody: '依頼者に新しいリンクを頼んでください。',
		unavailableBody:
			'このリンクではもう送れません。まだ送るものがある場合は、依頼者に新しいリンクを頼んでください。',
		outcomeRegion: '送った結果',
		outcomeUnknown: '届いたかどうか分かりません。',
		outcomeHint:
			'このタブを開いたまま、もう一度送る前に届いたかどうかを確認してください。再送すると、同じ内容をもう一度送ります。ページを離れるか再読み込みすると、その内容は消えます。',
		reservationRemaining: (minutes: number, seconds: number) =>
			`アップロードできる残り時間：${minutes}分${seconds}秒。`,
		checkSubmission: '届いたか確認',
		cancelUpload: 'アップロードをキャンセル',
		retryCancellation: 'もう一度キャンセル',
		cancelHint:
			'もう一度送る前に、キャンセルが完了したか確認する必要があります。アップロードを止めても、すでにアップロードされた部分はすぐには消えず、あとで自動で削除されます。',
		retryReady: 'まだ届いていません。同じ内容をもう一度送れます。',
		retrySame: '同じ内容をもう一度送る',
		requesterNote: '依頼者からのメモ',
		messageLabel: 'メッセージ',
		fileLabel: 'ファイル',
		fileLimit: (size: string) => `ファイル1つ、最大${size}。`,
		encrypting: 'Encrypting…',
		submit: 'Encrypt and submit',
		loading: 'リクエストを読み込み中…',
		retryLoading: 'リクエストを再読み込み',
		progress: {
			cancelling: 'アップロードをキャンセル中…',
			reserving: 'アップロードを準備中…',
			verifying: '届いたかどうかを確認中…',
			uploadingBytes: (loaded: string, total: string, percent: number) =>
				`暗号化したファイルをアップロード中：${total}バイト中${loaded}バイト（${percent}%）。届いたかどうかはこのあと確認します。`,
			uploading: '暗号化したファイルをアップロード中…',
			submitting: '暗号化した内容を送っています…'
		},
		errors: {
			reservationExpired:
				'アップロードの期限が切れました。別のファイルを選ぶ前に、今送った内容が届いたか確認してください。',
			keyMismatch: 'このリンクはリクエストと一致しません。依頼者に新しいリンクを頼んでください。',
			loadFailed: 'リクエストを読み込めませんでした。もう一度お試しください。',
			noteMismatch:
				'依頼者のメモがこのリンクと一致しません。依頼者にリンク全体を送り直してもらってください。',
			noteDecryptFailed:
				'依頼者のメモを開けませんでした。依頼者にリンク全体を送り直してもらってください。',
			submitUnconfirmed: '届いたかどうか確認できませんでした。',
			fillField: '少なくとも1つの項目を入力してください。',
			sizeRange: (max: string) => `1〜${max}バイトの内容を選んでください。`,
			encryptFailed:
				'内容を暗号化できませんでした。ファイル名とサイズを確認し、最新のブラウザで https:// から始まるアドレスを開いてください。',
			verifyFailed: '届いたかどうか確認できませんでした。',
			noWebCrypto:
				'このブラウザでは送れません。最新のブラウザで https:// から始まるアドレスを開いてください。',
			invalidLink: 'このリンクは使えません。依頼者にリンク全体を送り直してもらってください。'
		},
		notices: {
			acceptedBeforeCancel:
				'キャンセルする前に、内容はすでに届きました。届いた内容は依頼者だけがキャンセルできます。',
			attemptRenewed:
				'前回のアップロードは止まりました。もう一度送るには、ファイルを選んでください。',
			cancelNotConfirmed: 'キャンセルできたか確認できていません。もう一度キャンセルしてください。',
			instructionExpired:
				'アップロードの期限が切れました。別のファイルを選ぶ前に、今のアップロードをキャンセルしてください。',
			cancelUnconfirmed:
				'キャンセルできたか確認できませんでした。このタブを開いたまま、届いたかどうかを確認してください。アップロードを止めても、すでにアップロードされた部分はすぐには消えません。'
		}
	},
	receive: {
		pageTitle: '届いた内容 - Flick',
		badge: '自分専用のページ',
		headingExpired: 'リクエストの期限が切れました',
		headingUnavailable: 'リクエストを利用できません',
		headingOpened: '開封済み',
		headingOpenLost: '開けたかどうか分かりません',
		headingDecryptFailed: '内容を開けませんでした',
		headingReady: '受け取る準備ができました',
		headingConsumed: 'すでに開きました',
		headingCancelled: '取り消し済み',
		headingWaiting: '相手が送るのを待っています',
		headingStatus: 'リクエストの状態',
		statusRegion: 'リクエストの状態',
		expiredBody: 'リクエストとここに表示されていた内容は、もう利用できません。',
		unavailableBody:
			'リンクが無効か期限切れです。保存しておいた自分用の受け取りリンク全体で開き直してください。',
		lastKnown: '最後に確認した状態：',
		uploading: 'ファイルをアップロード中です。まだ開けません。',
		waiting: '相手が送るのを待っています。',
		accepted: '内容が届きました。',
		openOnce: '1回だけ開けます。',
		cancelled: 'これ以上、送ることも開くこともできません。',
		saveNow:
			'今すぐ保存してください。ページを離れたり再読み込みしたり、期限が来たりすると完全に消去されます。',
		consumed: 'すでに1回開いたため、もう開けません。',
		checking: '自動で確認中',
		loadingStatus: 'リクエストの状態を読み込み中…',
		refresh: '状態を更新',
		openLost:
			'開いている途中で接続が切れました。すでに開いたことになっている可能性があるため、このページはもう一度開こうとしません。',
		decryptFailed:
			'内容は届きましたが、開けませんでした。もう一度開くことはできないため、新しいリクエストを作成して送り直してもらってください。',
		decryptedSecret: '受け取った内容',
		download: (filename: string) => `${filename}をダウンロード`,
		working: 'Working…',
		open: 'Open once',
		shareHeading: '相手に渡すリンク',
		shareHint:
			'このリンクを知っている人なら誰でも1回だけ送れます。このリンクでは、届いた内容を開くこともリクエストをキャンセルすることもできません。',
		submissionLink: '相手に渡すリンク',
		copySubmissionLink: '相手に渡すリンクをコピー',
		showSubmissionQr: '相手に渡すリンクのQRを表示',
		submissionGone:
			'相手に渡すリンクはここでは再表示できません。保存しておいたリンクを使ってください。',
		privateHeading: '受け取りリンクは自分だけで保管してください',
		privateBody:
			'届いた内容はこのリンクでしか開けません。ここに戻るときや別の端末で開くときのために保存してください。このリンクを知っている人なら誰でも、届いた内容を開いたりリクエストをキャンセルしたりできます。ブラウザの履歴、同期、クリップボード管理アプリ、スクリーンショットにコピーが残る場合があります。',
		privateLink: '自分用の受け取りリンク',
		copyPrivateLink: '自分用の受け取りリンクをコピー',
		cancelRequest: 'リクエストをキャンセル',
		createAnother: '別のリクエストを作成',
		confirmTitle: 'このリクエストをキャンセルしますか？',
		confirmBody:
			'これ以上、送ることも開くこともできなくなります。すでに開いた内容は取り戻せません。',
		errors: {
			missingKey:
				'リンクの一部が欠けています。保存しておいた自分用の受け取りリンク全体で開き直してください。Flickはなくしたリンクを取り戻せず、リンク全体がないと届いた内容は開けません。',
			invalidKey:
				'リンクの一部が正しくありません。保存しておいた自分用の受け取りリンク全体で開き直してください。まだ何も開いていません。Flickはなくしたリンクや壊れたリンクを取り戻せません。',
			statusFailed: 'リクエストの状態を確認できませんでした。もう一度お試しください。',
			limitsFailed:
				'開けるサイズを確認できませんでした。開く前に状態を更新してください。まだ何も開いていません。'
		},
		notices: {
			changedBeforeCancel:
				'キャンセルする前にリクエストの状態が変わりました。現在の状態を表示しています。',
			notReleased:
				'内容を受け取れませんでした。別のタブでリクエストの状態が変わった可能性があります。'
		}
	},
	api: {
		invalidResponse: 'Flickから届いた応答を読み取れませんでした。',
		linkUnavailable: 'このリンクは使えません。',
		offline: 'オフラインか、接続が中断されました。接続を確認してください。',
		unavailable: 'リクエストを利用できません。リンクが無効か期限切れの可能性があります。',
		conflict: 'リクエストの状態が変わりました。状態を更新してください。',
		tooLarge: '内容がサイズ上限を超えています。',
		rateLimited: '試行回数が多すぎます。しばらく待ってからもう一度お試しください。',
		storageFull: 'Flickの保存容量がいっぱいです。しばらくしてからもう一度お試しください。',
		serverUnavailable: 'サーバーが一時的に利用できません。しばらくしてからもう一度お試しください。',
		reservationExpired:
			'アップロードの期限が切れました。別のファイルを選ぶ前に、届いたか確認するかキャンセルしてください。'
	}
};
