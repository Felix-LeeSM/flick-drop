# Design prototype handoff — 2026-09-09

> 이 문서는 완료된 작업 기록이다. 아래 본문은 시안을 넘길 때 쓴 원문이고, 이식
> 결과는 마지막 §구현 결과에 있다. 본문이 가리키는 `web/src/routes/prototype/`과
> `/tmp` 스크린샷은 저장소에 없다 — 시안 라우트는 데모 링크
> `https://example.invalid/s/design-preview`를 들고 있어서 일부러 커밋하지 않았다.
> 확정된 디자인은 `web/src/lib/components/CreateSecretPage.svelte`에서 본다.

## Goal / 현재 상태

Flick 생성·공유 화면의 디자인 검토를 마쳤다. 사용자가 **2번 Expandable Action Bar**를 최종 선택했다. 다음 작업은 확정 시안을 실제 생성·공유 흐름에 이식하는 것이다.

- 확정 시안: `web/src/routes/prototype/+page.svelte`
- 로컬 URL: `http://127.0.0.1:5173/prototype`
- 기존 화면: `web/src/routes/+page.svelte` → `web/src/lib/components/CreateSecretPage.svelte`
- **프로토타입만 수정했다. 실제 생성·암호화·업로드·열람 로직에 디자인을 반영하지 않았다.**
- 이 문서를 작성할 때 checkout은 `main`이며 프로토타입은 untracked다. 이 디자인 작업의 commit/push/PR은 없다.
- 동일 checkout에 CLI·배포 등 별도 작업 변경이 다수 있다. 다음 작업자는 `git status --short`를 다시 확인하고 다른 작업을 포함하거나 되돌리지 않는다. 실제 제품 구현 전 `docs/architecture/agent-workflow.md`에 따라 issue와 topic branch를 준비한다.

## 확정된 사용자 결정

1. **2번을 채택한다.** 아이콘들이 하나의 둥근 바 안에 있고, 데스크톱 호버·키보드 포커스 시 이름이 펼쳐진다. 1번 Overflow Actions 및 비교용 제목·상태·CSS는 삭제했다.
2. **모바일에서는 유형을 전부 노출한다.** 유형 선택용 `select`를 다시 도입하지 않는다. `Text / File / Login / Card / Identity / Custom` 6개를 아이콘과 이름으로 표시하고 필요하면 줄바꿈한다.
3. **만료 시간 Custom은 기존 버튼 디자인을 유지한다.** `2 days`처럼 숫자와 단위를 둥근 버튼 안에서 직접 수정한다. 별도 Custom 버튼을 누른 뒤 입력창을 펼치는 안은 폐기했다. 유형의 `Custom`과 만료 시간의 사용자 지정 입력은 서로 다른 기능이다.
4. **QR 공유는 유지한다.** 최초 시안에서 실수로 누락했다가 복원했다. 사용자가 QR 제거를 승인한 적은 없다.

최종 선택 근거가 된 레퍼런스:

- 채택: [beUI — Expandable Action Bar](https://beui.dev/components/blocks/expandable-action-bar)
- 비교 후 미채택: [beUI — Overflow Actions](https://beui.dev/components/blocks/overflow-actions)

레퍼런스의 시각적 동작을 참고했으며 React/Motion 의존성을 추가하지 않았다.

## 프로토타입 구현

- 기존 `layout.css`의 크림색 배경·청록색 포인트·Instrument Serif 제목을 사용한다.
- `.action-bar`의 데스크톱 조건은 `(min-width: 768px) and (hover: hover) and (pointer: fine)`이다. 그 외 환경은 이름을 항상 표시한다.
- 데스크톱 기본 상태는 아이콘만 표시한다. 바 전체 `:hover` 또는 `:focus-within`에서 모든 라벨을 펼친다.
- 라벨 폭과 간격은 `350ms cubic-bezier(.22,1,.36,1)`, 불투명도는 `180ms`, blur는 `240ms`로 전환한다. `prefers-reduced-motion: reduce`에서는 전환을 끈다.
- 선택된 유형은 어두운 배경 및 `aria-pressed`로 표시한다. 버튼의 `aria-label`이 이름을 제공하며 아이콘과 시각적 라벨은 `aria-hidden`으로 중복 낭독을 피한다.
- `CredentialForm`, `ThemeToggle`, `QrModal`은 기존 컴포넌트를 재사용한다.
- `Message`, `Protect with a password`, `Expires after`처럼 읽기 쉬운 라벨을 쓴다.
- 공유 완료 화면에는 `Copy link`, `Show QR`, 비밀번호 별도 전달 안내가 있다. 비밀번호 보호를 끄면 링크 소지자 접근 안내로 바뀐다.
- 링크 복사는 성공·실패 문구를 `role="status"`로 표시한다.

## 데모 경계 — 제품 코드로 그대로 옮기지 말 것

`preview()`는 폼 제출 후 `complete = true`로 바꾸는 UI 데모다. 암호화나 API 요청을 하지 않는다.

- 링크와 QR 값은 항상 `https://example.invalid/s/design-preview`다. QR 모달은 렌더링되지만 실제 비밀을 열 수 있는 링크가 아니다.
- 만료 시간 문구는 선택값 표시이며 실제 만료·카운트다운이 아니다.
- 파일 선택은 업로드·묶기·용량 검사·진행률·취소를 구현하지 않는다.
- 비밀번호와 입력 내용은 페이지 메모리에만 둔다. `Back to editing`은 메모리의 초안을 다시 보여주는 데모 동작이다. 실제 생성 완료 후 비밀정보를 유지하라는 요구가 아니다.
- 실제 `CreateSecretPage.svelte`의 검증, 서버 설정 로딩, 오류 처리, 생성 완료 후 정보 정리, 만료 처리와 `Create another` 동작을 보존한다.
- 현재 헤드라인·문구와 350ms 타이밍은 시안 값이다. 사용자가 명시 확정한 핵심은 2번 인터랙션, 모바일 전체 노출, 기존 Custom 형태, QR 유지다.

## 다음 구현 순서

1. root `AGENTS.md`, `web/AGENTS.md`, 이 문서와 관련 아키텍처 문서를 읽고 작업 branch/issue를 준비한다. 보안·데이터 생명주기를 건드리면 `docs/architecture/security-model.md`, `docs/architecture/storage-model.md`를 먼저 확인한다.
2. 기존 `CreateSecretPage.svelte`의 유형 선택 UI에 2번 바를 이식한다. `switchMode()` 및 호출부를 확인하고 기능별 입력·파일 처리 경계를 유지한다. 프로토타입 전체를 제품 화면으로 교체하지 않는다.
3. 생성 폼의 라벨·비밀번호 안내와 공유 완료 화면의 복사 피드백을 반영한다. 실제 비밀번호 입력의 연결된 Label/accessible name을 확인한다.
4. 기존 `QrModal.svelte`, `QrCode.svelte`, `UrlField.svelte`를 활용해 실제 `shareUrl`로 QR·복사를 제공한다. QR 모달 내부 복사 버튼은 기존 `UrlField` 동작이므로 성공·실패 피드백 개선 여부도 별도로 확인한다.
5. 실제 생성 → 공유 → 한 번 열기 흐름으로 회귀 검증한다. 적용 완료 후 `/prototype`과 데모 문구·고정 링크의 배포 포함 여부를 정리한다.

## 검증 기록과 한계

최종 QR 복원 직후 같은 세션에서 수행한 결과:

- `pnpm --dir web check`: 0 errors, 0 warnings.
- `pnpm --dir web exec biome check --write src/routes/prototype/+page.svelte`: 통과, import 정렬 반영.
- 로컬 Chrome: 데스크톱 1440×1050, 모바일 viewport 390×844에서 화면 확인.
- 데스크톱 호버 시 바 폭 확장, 유형 선택 상태, 모바일 가로 넘침 없음과 유형 버튼 6개 존재를 확인했다.
- Custom 입력 포커스 시 선택 상태 전환, 공유 완료 시 제목 포커스 이동, 비밀번호 전달 안내, 편집 복귀 시 텍스트 초안 유지를 확인했다.
- `Show QR` 모달 표시와 데모 URL, Escape 닫기를 확인하고 QR 화면을 육안 확인했다. QR 스캔·디코딩 테스트는 하지 않았다.
- 실제 API/암호화/업로드/한 번 열기, 다크 모드 전체 흐름, 모든 유형의 초안 보존, 클립보드 실패, QR 닫기 후 포커스 복귀, 모션 감소 환경은 이번 프로토타입 검증으로 보장하지 않는다.
- 전체 CI/build/e2e는 실행하지 않았다. 제품 반영 시 `docs/architecture/ci-testing.md`와 `scripts/ci/all.sh`의 적용 가능한 검증을 수행한다.

세션 임시 증거는 `/tmp`에 있어 삭제될 수 있다. 저장소에 포함된 테스트나 배포 산출물이 아니다.

- `/tmp/flick-prototype-check.mjs`: Node assert + Chrome DevTools Protocol 검증 스크립트. dev server 5173 및 별도 headless Chrome의 debugging port 9227이 필요하다. 검사 후 headless Chrome은 종료했다. 스크립트 내부 `Second bar`/`Selection shared` assertion 이름은 비교 시안에서 남은 표현이다.
- `/tmp/flick-prototype-desktop.png`: 최종 아이콘 바 기본 화면.
- `/tmp/flick-second-expanded.png`: 최종 아이콘 바 펼침 화면.
- `/tmp/flick-prototype-mobile.png`: 최종 모바일 화면.
- `/tmp/flick-prototype-result.png`: QR 버튼 복원 후 공유 완료 화면.
- `/tmp/flick-prototype-qr.png`: QR 모달 화면.

## 재개 명령

저장소 루트에서 실행한다. 5173에 dev server가 살아 있으면 중복 실행하지 않는다.

```sh
pnpm --dir web dev --host 127.0.0.1
```

`http://127.0.0.1:5173/prototype`에서 임의의 데모 메시지와 비밀번호를 입력하고 `Create one-time link` → `Show QR`로 확인한다. QR은 `example.invalid` 데모 링크다.

## 구현 결과 — issue #171 (2026-09-09)

확정 시안을 실제 생성·공유 화면에 이식했다. 변경 파일은
`web/src/lib/components/CreateSecretPage.svelte`와
`web/src/lib/components/UrlField.svelte` 두 개다.

- 유형 선택 6개를 `.type-bar` 하나에 넣고, 데스크톱
  (`(min-width: 768px) and (hover: hover) and (pointer: fine)`)에서는 아이콘만
  보이다가 바 전체 `:hover`·`:focus-within`에서 라벨이 함께 펼쳐진다. 전환값은
  시안과 같은 `350ms cubic-bezier(.22,1,.36,1)` / opacity `180ms` / blur
  `240ms`이며 `prefers-reduced-motion: reduce`에서 끈다.
- 모바일·터치에서는 6개 라벨을 항상 노출하고 줄바꿈한다. 390×844에서 가로 넘침이
  없다.
- 만료 시간 사용자 지정은 기존 인라인 `2 days` 필(값 입력 + 단위 `select`)을
  그대로 유지했다. `switchMode()`와 텍스트·파일·크리덴셜 페이로드 경계, 파일
  드래그·zip 묶기·용량 검사·취소는 손대지 않았다.
- 폼 라벨을 `Message` / `Files` / `Protect with a passphrase` / `Expires after`로
  바꾸고, 암호 보호를 켰을 때 별도 전달 안내를 폼에 넣었다. 공유 완료 화면에는
  같은 안내를 `aside`로 두고 `Show QR`을 유지했다.
- **시안과 다르게 간 부분**: 시안의 `password`를 `passphrase`로 썼다. 열람 화면
  (`web/src/lib/components/OpenSecretPage.svelte:392`)과
  `docs/architecture/security-model.md`가 전부 passphrase를 쓰므로, 생성 화면만
  password로 바꾸면 제품 용어가 갈라진다. 시안의 문구는 확정 항목이 아니라
  제안값이라는 이 문서 §데모 경계 기준을 따랐다.
- 복사 피드백은 `UrlField.svelte`에 넣었다. 공유 완료 화면과 QR 모달이 같은
  컴포넌트를 쓰므로 두 곳이 함께 고쳐진다. 성공은 `Copied`, 실패는
  `Copy unavailable. Select the link above and copy it.`을 `role="status"`로
  알린다. 실패 문구는 지시문이므로 자동으로 지우지 않는다.
- `/prototype` 라우트는 저장소에 넣지 않았다. untracked로 남겨 두면 빌드·배포에
  포함되지 않으므로, `example.invalid` 고정 링크와 데모 문구가 운영에 나갈 일이
  없다.

검증(로컬 dev 스택 + headless Chrome CDP, `/tmp/flick-171-check.mjs` 13개 검사
전부 통과):

- `pnpm --dir web check` 0 errors / 0 warnings, `pnpm --dir web lint`,
  `pnpm --dir web test` 67 passed, `pnpm --dir web build` 통과.
- 데스크톱 1440×1050: 기본 상태 라벨 `max-width: 0`, 바 hover 시 6개 라벨 전부
  확장, 포인터 없이 키보드 포커스만으로도 확장, 확장 후 바·문서 가로 넘침 없음.
- 모바일 390×844: 라벨 6개 노출, 가로 넘침 없음.
- 실제 흐름: 생성 → `#share-url` 발급 → 복사 버튼 클릭 시 클립보드 값 일치 +
  `Copied` 표시 → QR 모달 표시·Escape 닫기 → 링크 열기에서 passphrase 입력 후
  평문 복호화 → 재요청 시 평문 미노출(한 번 열기 소진).
- 다크 모드 육안 확인. QR 스캔·디코딩 테스트, 클립보드 실패 경로의 실물 재현은
  하지 않았다.
