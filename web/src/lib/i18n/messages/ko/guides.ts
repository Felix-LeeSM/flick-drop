import type { guides as source } from '../en/guides';

export const guides: typeof source = {
	title: 'Flick 작동 방식',
	description:
		'자주 묻는 질문부터 시작해서, 보낸 내용을 서버가 읽을 수 없는 이유, 소스 코드 위치, 직접 운영하는 방법까지 차례로 설명해요.',
	cta: '한 번만 열리는 링크 만들기',
	toc: '이 페이지의 내용',
	sections: {
		sending: '보내고 열기',
		limits: 'Flick이 할 수 없는 일',
		privacy: '서버가 내용을 읽을 수 없는 이유',
		source: '소스 코드',
		selfHost: 'Flick 직접 운영하기'
	},
	footerNav: 'Flick',
	openFlick: 'Flick 열기',
	sourceCode: '소스 코드'
};
