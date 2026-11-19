import type { request as source } from '../en/request';

export const request: typeof source = {
	shared: {
		badge: 'エンドツーエンド暗号化 · 1回限りのリクエスト',
		timeRemaining: '残り時間',
		statusUnknown: 'リクエストの状態が不明です。'
	},
	create: {
		pageTitle: 'シークレットをリクエスト - Flick',
		headerLink: 'Send a secret',
		heading: 'Request a secret',
		intro: '相手がメモ・ファイル・認証情報を1回だけ送れるリンクを作成します。',
		noteLabel: 'メモ',
		optional: '任意',
		notePlaceholder: '送ってほしい内容を書いてください（例：Wi-Fiのパスワード、SSH鍵）',
		noteTooLong: (max: string, used: string) =>
			`メモは${max}バイト以内にしてください（現在${used}バイト）。`,
		noteHint: 'エンドツーエンドで暗号化され、リクエストのリンクを開いた人にだけ表示されます。',
		lifetimeGroup: 'リクエストの有効時間',
		lifetimeHint: 'リンクを作成した時点から始まります。提出しても延長されません。',
		unconfirmed:
			'リクエストの作成を確認できませんでした。未使用のリクエストが残っている可能性がありますが、期限が来ると失効します。そのリンクは復元できません。自動での再試行は行いません。',
		unsupported:
			'リクエストの暗号化を利用できません。最新のブラウザでHTTPSまたはlocalhostから開いてください。',
		creating: 'Creating request…',
		createNew: 'Create a new request',
		create: 'Create request'
	},
	submit: {
		pageTitle: 'シークレットを提出 - Flick',
		headingAccepted: '提出を受け付けました',
		headingExpired: 'リクエストの期限が切れました',
		headingUnavailable: 'リクエストを利用できません',
		headingReady: 'Send a secret',
		intro: '送った内容は依頼者だけが開けます。',
		resumed:
			'ページを離れたか再読み込みしたため、メモリ上の前回の試行は破棄されました。すでに受け付けられている可能性があります。',
		acceptedBody: '依頼者だけが開けます。',
		expiredBody: '依頼者に新しいリンクを頼んでください。',
		unavailableBody:
			'このリンクでは提出を受け付けていません。まだ送るものがある場合は、依頼者に新しいリンクを頼んでください。',
		outcomeRegion: '提出結果',
		outcomeUnknown: '提出結果が不明です。',
		outcomeHint:
			'このタブを開いたまま、再試行する前に提出状況を確認してください。再試行では同じ暗号化済みの内容を送り直します。ページを離れるか再読み込みすると、その内容は破棄されます。',
		reservationRemaining: (minutes: number, seconds: number) =>
			`アップロード予約：残り${minutes}分${seconds}秒。`,
		checkSubmission: '提出を確認',
		cancelUpload: 'アップロードをキャンセル',
		retryCancellation: 'キャンセルを再試行',
		cancelHint:
			'次の試行の前に、サーバーがキャンセルを確認する必要があります。転送を止めてもアップロード済みのデータは削除されず、サーバー側のクリーンアップで処理されます。',
		retryReady: 'サーバーはこの試行を受け付けていません。同じ提出を再試行できます。',
		retrySame: '同じ提出を再試行',
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
			reserving: '暗号化アップロードを予約中…',
			verifying: 'アップロードを確認中… まだ受け付けは確認されていません。',
			uploadingBytes: (loaded: string, total: string, percent: number) =>
				`暗号化ファイルをアップロード中：${total}バイト中${loaded}バイト（${percent}%）。受け付けには確認が必要です。`,
			uploading: '暗号化ファイルをアップロード中…',
			submitting: '暗号化した内容を提出中…'
		},
		errors: {
			reservationExpired:
				'アップロードの予約が期限切れになりました。別のファイルを選ぶ前に、今回の試行を確認してください。',
			keyMismatch:
				'リクエストの鍵がこのリンクと一致しません。依頼者に新しい提出用リンクを頼んでください。',
			loadFailed: 'リクエストを読み込めませんでした。もう一度お試しください。',
			noteMismatch:
				'リクエストのメモがこのリンクと一致しません。依頼者に完全な提出用リンクを頼んでください。',
			noteDecryptFailed:
				'依頼者のメモを復号できませんでした。依頼者に完全な提出用リンクを頼んでください。',
			submitUnconfirmed: '提出が確認されませんでした。',
			fillField: '少なくとも1つの項目を入力してください。',
			sizeRange: (max: string) => `1〜${max}バイトの内容を選んでください。`,
			encryptFailed:
				'内容を暗号化できませんでした。ファイル名とサイズを確認し、最新のブラウザでHTTPSまたはlocalhostから開いてください。',
			verifyFailed: '提出状況を確認できませんでした。',
			noWebCrypto:
				'リクエストの暗号化には、Web Cryptoに対応したブラウザとHTTPSまたはlocalhostでの接続が必要です。',
			invalidLink: '提出用リンクが無効です。依頼者に完全なリンクを頼んでください。'
		},
		notices: {
			acceptedBeforeCancel:
				'キャンセルの前にサーバーが提出を受け付けました。受け付け済みの内容は依頼者だけがキャンセルできます。',
			attemptRenewed:
				'前回のアップロードの試行は終了しました。新しく試行するにはファイルを選んでください。',
			cancelNotConfirmed:
				'キャンセルが確認されていません。同じ試行でキャンセルを再試行してください。',
			instructionExpired:
				'アップロード先の有効期限が切れました。別のファイルを選ぶ前に、この試行をキャンセルしてください。',
			cancelUnconfirmed:
				'キャンセルが確認されませんでした。このタブを開いたまま試行を確認してください。転送を止めてもアップロード済みのデータは削除されません。'
		}
	},
	receive: {
		pageTitle: '非公開の受け取り - Flick',
		badge: '依頼者専用ページ',
		headingExpired: 'リクエストの期限が切れました',
		headingUnavailable: 'リクエストを利用できません',
		headingOpened: '開封済み',
		headingOpenLost: '開封結果を確認できません',
		headingDecryptFailed: '復号できませんでした',
		headingReady: '受け取る準備ができました',
		headingConsumed: 'すでに開封されています',
		headingCancelled: '取り消し済み',
		headingWaiting: '提出を待っています',
		headingStatus: 'リクエストの状態',
		statusRegion: 'リクエストの状態',
		expiredBody: 'リクエストとここに表示されていた内容は、もう利用できません。',
		unavailableBody:
			'リンクが無効か期限切れです。非公開の受け取り用リンクを省略せずに使ってください。',
		lastKnown: '最後に確認した状態：',
		uploading: 'ファイルをアップロード中です。まだ開けません。',
		waiting: '最初の提出を待っています。',
		accepted: '提出が受け付けられました。',
		openOnce: '1回だけ開けます。',
		cancelled: 'これ以上、提出も開封もできません。',
		saveNow:
			'今すぐ保存してください。ページを離れたり再読み込みしたり、期限が来たりすると完全に消去されます。',
		consumed: 'すでに1回開封されたため、もう開けません。',
		checking: '自動で確認中',
		loadingStatus: 'リクエストの状態を読み込み中…',
		refresh: '状態を更新',
		openLost:
			'開封の応答が失われたか、無効でした。サーバー側ではすでに開封済みになっている可能性があり、このページは再試行しません。',
		decryptFailed:
			'内容は受け取りましたが、復号に失敗しました。もう開けません。送信者に新しいリクエストで送り直してもらってください。',
		decryptedSecret: '復号したシークレット',
		download: (filename: string) => `${filename}をダウンロード`,
		working: 'Working…',
		open: 'Open once',
		shareHeading: '提出用リンクを共有',
		shareHint:
			'このリンクを知っている人なら誰でも1回提出できます。このリンクではリクエストを開くこともキャンセルすることもできません。',
		submissionLink: '提出用リンク',
		copySubmissionLink: '提出用リンクをコピー',
		showSubmissionQr: '提出用QRを表示',
		submissionGone: '保存しておいた提出用リンクを使ってください。ここでは再表示できません。',
		privateHeading: '受け取り用リンクは自分だけで保管してください',
		privateBody:
			'このリンクには復号用の鍵が含まれています。ここに戻るときや別の端末で開くときのために保存してください。リンクがないと、提出された内容は開けません。リンクを知っている人なら誰でもリクエストを開いたりキャンセルしたりできます。ブラウザの履歴、同期、クリップボード管理アプリ、スクリーンショットにコピーが残る場合があります。',
		privateLink: '非公開の受け取り用リンク',
		copyPrivateLink: '非公開の受け取り用リンクをコピー',
		cancelRequest: 'リクエストをキャンセル',
		createAnother: '別のリクエストを作成',
		confirmTitle: 'このリクエストをキャンセルしますか？',
		confirmBody: 'これ以上、提出も開封もできなくなります。すでに開封された内容は取り戻せません。',
		errors: {
			missingKey:
				'秘密鍵がありません。非公開の受け取り用リンクを省略せずに開き直してください。Flickは失った鍵を復元できず、鍵なしでは復号できません。',
			invalidKey:
				'秘密鍵が無効です。元の非公開の受け取り用リンクを省略せずに開き直してください。何も開封されていません。Flickは失われた鍵や壊れた鍵を復元できません。',
			statusFailed: 'リクエストの状態を確認できませんでした。もう一度お試しください。',
			limitsFailed:
				'サイズ上限を確認できませんでした。開く前に状態を更新してください。まだ何も開封されていません。'
		},
		notices: {
			changedBeforeCancel:
				'キャンセルの前にリクエストが変わりました。現在のサーバーの状態を表示しています。',
			notReleased:
				'サーバーは今回の開封で内容を渡しませんでした。別のタブでリクエストが変わった可能性があります。'
		}
	},
	api: {
		invalidResponse: 'サーバーの応答を確認できませんでした。',
		linkUnavailable: 'リクエストのリンクを利用できません。',
		offline: 'オフラインか、接続が中断されました。接続を確認してください。',
		unavailable: 'リクエストを利用できません。リンクが無効か期限切れの可能性があります。',
		conflict: 'リクエストが変わりました。状態を更新してください。',
		tooLarge: '内容がサイズ上限を超えています。',
		rateLimited: '試行回数が多すぎます。しばらく待ってからもう一度お試しください。',
		storageFull: 'ストレージの容量がいっぱいです。しばらくしてからもう一度お試しください。',
		serverUnavailable: 'サーバーが一時的に利用できません。しばらくしてからもう一度お試しください。',
		reservationExpired:
			'アップロードの予約が期限切れになりました。別のファイルを選ぶ前に、この試行を確認するかキャンセルしてください。'
	}
};
