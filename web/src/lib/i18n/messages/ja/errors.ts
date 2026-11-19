import type { errors as source } from '../en/errors';

export const errors: typeof source = {
	invalidPassphrase: 'パスワードが正しくありません。',
	noLongerAvailable: 'このシークレットはもう利用できません。',
	fileTooLarge: 'ファイルが大きすぎます。',
	storageFull: 'ストレージの容量がいっぱいです。しばらくしてからもう一度お試しください。',
	uploadFailed: 'アップロードに失敗しました。もう一度お試しください。',
	notReady: 'Flickの準備ができていません。少し待ってからもう一度お試しください。',
	serverFailed: 'Flickが処理を完了できませんでした。もう一度お試しください。',
	requestFailed: '処理を完了できませんでした。入力内容を確認して、もう一度お試しください。',
	largeUploadStartFailed: '大容量アップロードを開始できませんでした。もう一度お試しください。',
	uploadSizeMismatch: 'アップロードサイズが一致しません。もう一度お試しください。',
	uploadCancelled: 'アップロードをキャンセルしました。',
	uploadUnreachable:
		'アップロード先に接続できませんでした。接続を確認して、もう一度お試しください。',
	uploadStartFailed: 'アップロードを開始できませんでした。もう一度お試しください。',
	flickUnreachable: 'Flickに接続できませんでした。接続を確認して、もう一度お試しください。',
	unreadableSecret: 'Flickから読み取れないシークレットが届きました。',
	managementUnavailable: '管理リンクは利用できません。',
	deliveryStatusUnverified: '送信の状態を確認できませんでした。もう一度お試しください。',
	deliveryStatusUnreachable:
		'送信の状態を更新できませんでした。接続を確認して、もう一度お試しください。',
	notCancellable: 'この送信はもうキャンセルできません。',
	tooManyStatusChecks: '状態の確認が多すぎます。少し待ってからもう一度お試しください。',
	page: {
		notFoundTitle: 'ページが見つかりません',
		errorTitle: (status: number) => `エラー ${status}`,
		label: (status: number) => `エラー ${status}`,
		notFoundHeading: 'ここには何もありません。',
		failedHeading: '問題が発生しました。',
		notFoundBody:
			'このアドレスにページはありません。リンクに入力ミスがないか確認するか、トップに戻って新しく始めてください。',
		failedBody:
			'予期しないエラーでページが中断されました。たいていはトップページからやり直せば解決します。',
		cta: 'Create a one-time link'
	}
};
