import type { manage as source } from '../en/manage';

export const manage: typeof source = {
	headTitle: '送ったリンクの管理',
	statuses: {
		pendingUpload: {
			title: 'アップロード中',
			detail: 'ファイルのアップロードがまだ終わっていません。今ならキャンセルできます。'
		},
		active: {
			title: '未開封',
			detail: '受け取る人は期限が切れる前に1回だけ開けます。'
		},
		locked: {
			title: 'ロック済み',
			detail:
				'パスワードを5回間違えたため、開けなくなりました。受け取る人が開いたという意味ではありません。'
		},
		cancelled: {
			title: '取り消し済み',
			detail:
				'受け取る人用のリンクではもう開けません。すでに開いた人の手元にある内容は、キャンセルしても取り戻せません。'
		},
		unavailable: {
			title: '利用できません',
			detail: 'ファイルはアップロードされましたが、開ける内容は作成されませんでした。'
		}
	},
	leavePrompt:
		'受け取る人用のリンクをまだコピーも共有もしていません。このページを離れると二度と表示できません。それでも移動しますか？',
	changedBeforeCancel:
		'キャンセルする前にリンクの状態が変わりました。現在の状態を下に表示しています。',
	noLongerCancellable: 'このリンクはもうキャンセルできません。',
	privateSenderPage: '自分専用のページ',
	titleCreated: 'リンクを作成しました',
	titleManage: 'Manage delivery',
	intro: 'リンクが開かれたかを確認し、開かれる前ならキャンセルできます。',
	expired: '管理リンクの期限が切れました。',
	statusRegion: 'リンクの状態',
	lastKnown: '最後に確認した状態：',
	loadingStatus: 'リンクの状態を読み込んでいます…',
	unknownStatus: 'リンクの状態が分かりません。',
	refresh: '状態を更新',
	sendToRecipient: '受け取る人に送る',
	copyRecipientLink: '受け取る人用のリンクをコピー',
	showQr: 'QRを表示',
	sendPassphraseSeparately:
		'パスワードは別の方法で送ってください。受け取る人にはリンクとパスワードの両方が必要です。',
	anyoneWithLink:
		'このリンク全体を知っている人なら誰でも1回だけ開けます。受け取る人にだけ共有してください。',
	recipientLinkOnlyOriginal:
		'受け取る人用のリンクは作成したときの画面でしか表示できません。保存したリンクを使うか、新しく作成してください。',
	keepPrivate: 'この管理リンクは他の人に教えないでください',
	keepPrivateDetail:
		'このリンクを知っている人なら誰でも、受け取る人用のリンクが開かれるか期限が切れるまで、状態の確認やキャンセルができます。なくすと取り戻せないため、今すぐ保存してください。このリンクで内容を開いたり、受け取る人用のリンクを取り戻したりすることはできません。',
	privateLinkLabel: '自分用の管理リンク',
	copyManagementLink: '管理リンクをコピー',
	cancelDelivery: 'リンクをキャンセル',
	createAnother: 'もう1つ作成',
	confirmTitle: 'このリンクをキャンセルしますか？',
	confirmDescription:
		'受け取る人用のリンクでは開けなくなります。すでに開いた人の手元にある内容は取り戻せません。',
	keepIt: 'そのままにする',
	remaining: (time: string) => `残り${time}`
};
