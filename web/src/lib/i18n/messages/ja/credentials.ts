// biome-ignore-all lint/style/useNamingConvention: field keys are the stored credential field ids (full_name).
import type { credentials as source } from '../en/credentials';

export const credentials: typeof source = {
	templates: {
		login: {
			name: 'ログイン',
			description: 'ユーザー名、パスワード、URL、ワンタイムコードのシードです。',
			titlePlaceholder: '本番データベース',
			fields: {
				username: { label: 'ユーザー名', placeholder: 'alice@example.com' },
				password: { label: 'パスワード' },
				url: { label: 'URL', placeholder: 'https://example.com' },
				totp: { label: 'TOTPシード' }
			}
		},
		card: {
			name: 'カード',
			description: '一度だけ渡す決済カードの情報です。',
			titlePlaceholder: '法人カード',
			fields: {
				cardholder: { label: 'カード名義人', placeholder: 'YAMADA TARO' },
				number: { label: 'カード番号' },
				expires: { label: '有効期限', placeholder: 'MM/YY' },
				cvc: { label: 'セキュリティコード (CVC)' }
			}
		},
		identity: {
			name: '本人情報',
			description: '重要な識別番号を伏せて表示する本人情報です。',
			titlePlaceholder: '本人情報一式',
			fields: {
				full_name: { label: '氏名', placeholder: '山田 太郎' },
				email: { label: 'メールアドレス', placeholder: 'alice@example.com' },
				phone: { label: '電話番号' },
				identifier: { label: '識別番号' }
			}
		},
		custom: {
			name: 'カスタム',
			description: '項目名と値を自由に決め、必要な項目は非表示にできます。',
			titlePlaceholder: '共有するアカウント情報',
			fields: {}
		}
	},
	title: 'タイトル',
	label: '項目名',
	secret: '非表示',
	notes: 'メモ',
	notesPlaceholder: '任意',
	addField: '項目を追加',
	defaultFieldLabel: (n: number) => `項目 ${n}`,
	removeField: '項目を削除',
	removeNamed: (label: string) => `${label || '項目'}を削除`,
	showValue: '値を表示',
	hideValue: '値を隠す',
	showNamed: (label: string) => `${label}を表示`,
	hideNamed: (label: string) => `${label}を隠す`,
	copyNamed: (label: string) => `${label}をコピー`
};
