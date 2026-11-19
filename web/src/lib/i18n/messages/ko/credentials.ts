// biome-ignore-all lint/style/useNamingConvention: field keys are the stored credential field ids (full_name).
import type { credentials as source } from '../en/credentials';

export const credentials: typeof source = {
	templates: {
		login: {
			name: '로그인',
			description: '아이디, 비밀번호, 주소, 2단계 인증 설정 키를 담아요.',
			titlePlaceholder: '운영 데이터베이스',
			fields: {
				username: { label: '아이디', placeholder: 'alice@example.com' },
				password: { label: '비밀번호' },
				url: { label: '주소', placeholder: 'https://example.com' },
				totp: { label: '2단계 인증 설정 키' }
			}
		},
		card: {
			name: '카드',
			description: '한 번만 전할 결제 카드 정보예요.',
			titlePlaceholder: '법인 카드',
			fields: {
				cardholder: { label: '카드 소유자', placeholder: '홍길동' },
				number: { label: '카드 번호' },
				expires: { label: '유효기간', placeholder: 'MM/YY' },
				cvc: { label: 'CVC' }
			}
		},
		identity: {
			name: '개인 정보',
			description: '이름, 연락처, 식별 번호를 담아요. 식별 번호는 가려서 보여 줘요.',
			titlePlaceholder: '내 개인 정보',
			fields: {
				full_name: { label: '이름', placeholder: '홍길동' },
				email: { label: '이메일', placeholder: 'alice@example.com' },
				phone: { label: '전화번호' },
				identifier: { label: '식별 번호' }
			}
		},
		custom: {
			name: '직접 입력',
			description: '항목 이름, 값, 가리기를 직접 정해요.',
			titlePlaceholder: '공유할 계정 정보',
			fields: {}
		}
	},
	title: '제목',
	label: '항목 이름',
	secret: '가리기',
	notes: '메모',
	notesPlaceholder: '선택 사항',
	addField: '항목 추가',
	defaultFieldLabel: (n: number) => `항목 ${n}`,
	removeField: '항목 삭제',
	removeNamed: (label: string) => `${label || '항목'} 삭제`,
	showValue: '값 보기',
	hideValue: '값 가리기',
	showNamed: (label: string) => `${label} 보기`,
	hideNamed: (label: string) => `${label} 가리기`,
	copyNamed: (label: string) => `${label} 복사`
};
