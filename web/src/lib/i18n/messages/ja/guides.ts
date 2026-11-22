import type { guides as source } from '../en/guides';

export const guides: typeof source = {
	title: 'Flickの仕組み',
	description:
		'送り方・開き方の手順から、サーバーが内容を読めない理由、ソースコードの場所、自分で運用する方法まで順に説明します。',
	cta: '1回だけ開けるリンクを作成',
	toc: 'このページの内容',
	sections: {
		sending: '送る・開く',
		limits: 'Flickにできないこと',
		privacy: 'サーバーが内容を読めない理由',
		source: 'ソースコード',
		selfHost: 'Flickを自分で運用'
	},
	footerNav: 'Flick',
	openFlick: 'Flickを開く',
	sourceCode: 'ソースコード'
};
