import type { common as source } from '../en/common';

export const common: typeof source = {
	enterCode: 'Enter Code',
	siteNav: 'サイト',
	languageRow: '言語',
	deliveryMethod: '送り方',
	tabLink: 'Link',
	tabLive: 'Live Transfer',
	theme: {
		useLight: 'ライトモードにする',
		useDark: 'ダークモードにする'
	},
	sponsor: {
		label: 'スポンサー',
		opensNewTab: '（新しいタブで開きます）'
	},
	urlField: {
		label: '共有リンク',
		copyToClipboard: 'クリップボードにコピー',
		copy: 'コピー',
		copied: 'コピーしました',
		copyFailed: 'コピーできません。上のリンクを選択してコピーしてください。'
	},
	share: {
		shareLink: 'リンクを共有',
		failed: '共有できませんでした。リンクをコピーするか、QRコードを使ってください。'
	},
	qr: {
		title: 'スキャンまたはコピー',
		description: 'カメラでコードを読み取るか、下のリンクをコピーしてください。',
		close: '閉じる',
		codeLabel: '共有リンクのQRコード'
	},
	stage: {
		creatingLink: 'リンクを作成中',
		openingSecret: '内容を開いています',
		cancel: 'キャンセル',
		progressSteps: '進行状況'
	},
	lifetime: {
		label: '有効時間',
		group: '有効時間',
		preset10Min: '10分',
		preset1Hour: '1時間',
		preset24Hours: '24時間',
		preset7Days: '7日',
		customValue: '有効時間の数値',
		customUnit: '有効時間の単位',
		unitMinutes: '分',
		unitHours: '時間',
		unitDays: '日',
		daysHours: (d: number, h: number) => `${d}日${h}時間`,
		hoursMinutes: (h: number, m: number) => `${h}時間${m}分`,
		minutesSeconds: (m: number, s: number) => `${m}分${s}秒`,
		days: (n: number) => `${n}日`,
		hours: (n: number) => `${n}時間`,
		minutes: (n: number) => `${n}分`,
		range: (min: string, max: string) => `有効時間は${min}から${max}の間で指定してください。`
	}
};
