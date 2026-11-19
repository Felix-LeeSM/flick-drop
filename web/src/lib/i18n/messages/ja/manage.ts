import type { manage as source } from '../en/manage';

export const manage: typeof source = {
	headTitle: '送信の管理',
	statuses: {
		pendingUpload: {
			title: 'アップロード未完了',
			detail: '暗号化したファイルのアップロードがまだ完了していません。キャンセルできます。'
		},
		active: {
			title: '未開封',
			detail: '受信者は期限が切れる前に1回だけ開けます。'
		},
		locked: {
			title: 'ロック済み',
			detail:
				'パスワードを5回間違えたため、開けなくなりました。受信者が開封したという意味ではありません。'
		},
		cancelled: {
			title: '取り消し済み',
			detail:
				'受信者用リンクではもう開けません。すでに届いたコピーは、キャンセルしても回収できません。'
		},
		unavailable: {
			title: '利用できません',
			detail: 'アップロードが終了しましたが、開ける内容は作成されませんでした。'
		}
	},
	leavePrompt:
		'受信者用リンクをまだコピーも共有もしていません。このページを離れると二度と表示できません。それでも移動しますか？',
	changedBeforeCancel:
		'キャンセルする前に送信の状態が変わりました。現在の状態を下に表示しています。',
	noLongerCancellable: 'この送信はもうキャンセルできません。',
	privateSenderPage: '送信者専用ページ',
	titleCreated: 'リンクを作成しました',
	titleManage: 'Manage delivery',
	intro: '送信の状態を確認し、開封される前にキャンセルできます。',
	expired: '管理リンクの期限が切れました。',
	statusRegion: '送信の状態',
	lastKnown: '最後に確認した状態：',
	loadingStatus: '送信の状態を読み込んでいます…',
	unknownStatus: '送信の状態は不明です。',
	refresh: '状態を更新',
	sendToRecipient: '受信者に送る',
	copyRecipientLink: '受信者用リンクをコピー',
	showQr: 'QRを表示',
	sendPassphraseSeparately:
		'パスワードは別の方法で送ってください。受信者にはリンクとパスワードの両方が必要です。',
	anyoneWithLink:
		'このリンク全体を知っている人なら誰でも1回だけ開けます。受信者にだけ共有してください。',
	recipientLinkOnlyOriginal:
		'受信者用リンクは作成したときの画面でしか表示できません。保存したリンクを使うか、新しく作成してください。',
	keepPrivate: 'この管理リンクは他の人に教えないでください',
	keepPrivateDetail:
		'このリンクを知っている人なら誰でも、開封されるか期限が切れるまで、この送信の確認やキャンセルができます。なくすと復元できないため、今すぐ保存してください。このリンクで内容を復号したり、受信者用リンクを復元したりすることはできません。',
	privateLinkLabel: '非公開の管理リンク',
	copyManagementLink: '管理リンクをコピー',
	cancelDelivery: '送信をキャンセル',
	createAnother: 'もう1つ作成',
	confirmTitle: 'この送信をキャンセルしますか？',
	confirmDescription: '受信者用リンクは使えなくなります。すでに届いた内容は回収できません。',
	keepIt: 'そのままにする',
	timeUnits: { d: '日', h: '時間', m: '分', s: '秒' },
	remaining: (time: string) => `残り${time}`
};
