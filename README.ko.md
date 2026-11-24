# Flick

[English](README.md) | **한국어** | [日本語](README.ja.md)

Flick은 민감한 정보와 파일을 한 번만 열리는 링크로 건네는 셀프호스팅 오픈 소스 서비스예요. 내용은 올리기 전에 브라우저나 CLI에서 암호화되고, 한 번 열리거나 만료되면 서버에서 사라져요.

**라이브 데모:** https://flick.makana.cc/ · **사용 및 개발 안내:** https://flick.makana.cc/guides/

## 주요 기능

- **한 번만 열리는 암호화 링크:** 메모, 파일(여러 파일이나 폴더는 하나로 압축), 구조화된 템플릿(로그인, 카드, 개인 정보, 직접 입력)을 5분부터 7일 사이의 만료 시간으로 보낼 수 있어요.
- **비밀번호 선택 적용:** 비밀번호를 걸지 않으면 링크 키가 URL 프래그먼트(`#key=...`)에 담겨 서버로 전송되지 않아요. 비밀번호를 걸면 링크와 비밀번호가 모두 필요하며, 5번 틀리면 서버에서 삭제돼요.
- **실시간 전송:** 6자리 코드와 3자리 확인 번호로 두 기기 사이에 텍스트나 파일을 바로 옮겨요. 암호문은 API 메모리를 거쳐 스트리밍되며 데이터베이스나 파일 저장소에 기록되지 않아요.
- **링크로 요청하기:** 상대에게 줄 링크와 나만 보관하는 받기 링크를 함께 만들어, 상대가 처음 보낸 내용만 한 번 열어 볼 수 있어요. 세부 동작은 [요청 링크 명세](docs/architecture/request-links.md)에 정리돼 있어요.
- **계정 없는 관리 링크:** 로그인 없이도 상대가 아직 열지 않았는지 확인하거나 전송을 취소할 수 있어요. 관리 링크는 전달 만료와 함께 사라지고, 내용을 열거나 잃어버린 받는 사람 링크를 되찾을 수 없어요. Flick은 보낸 기록을 남기지 않으니 필요한 링크는 직접 저장해 두세요.
- **유연한 저장소와 비동기 정리:** 작은 암호화 데이터는 PostgreSQL에 직접 저장하고, 큰 파일은 S3 호환 스토리지에 올려요(OCI와 Cloudflare R2처럼 여러 버킷을 백엔드별 용량 한도로 병렬 운영할 수 있어요). 열렸거나 만료된 데이터는 비동기 워커가 지워요.
- **다국어 웹 UI:** 영어, 한국어, 일본어를 지원해요.

## 보안 모델

```text
The server should never know the plaintext secret, passphrase, or derived key.
```

- **기기 내 암호화:** 브라우저와 CLI는 [age](https://age-encryption.org/) 형식으로 전달마다 새 양자 내성 하이브리드 키(`ML-KEM-768` + `X25519`)를 만들어 암호화해요. 큰 파일은 64 KiB 청크 단위로 암호화하며 마지막 청크가 검증되기 전에는 아무것도 표시하지 않아요.
- **키 관리:** 비밀번호가 없으면 키는 URL 프래그먼트(`#key=AGE-SECRET-KEY-PQ-1…`)로만 전달되어 HTTP 요청이나 접속 로그에 남지 않아요. 비밀번호를 걸면 age의 `scrypt` 모드(work factor 18)로 키를 감싸고, 별도의 접근 증명(`PBKDF2-SHA-256`, 600,000회 반복)을 유도해 서버는 그 해시만 저장해요. 내장 생성기는 80비트 비밀번호를 만들어요.
- **실시간 전송:** 받는 사람의 브라우저가 일회용 키 쌍을 생성해 공개 키만 보내고, 보내는 사람이 그 공개 키로 암호화하면 서버는 암호문을 메모리에서 중계해요.
- **웹 암호화의 한계:** 브라우저를 쓸 때는 서버가 내려주는 JavaScript를 믿어야 해요. 이 설계는 데이터베이스, 로그, 백업 유출을 막지만 운영자가 악의를 품은 경우까지는 막지 못해요. `SHA256SUMS`로 검증한 명령줄 클라이언트를 쓰면 서버가 보내는 코드에 대한 신뢰가 필요 없어져요.

Flick은 임시 전달 도구이지 비밀번호 관리자나 장기 보관함이 아니에요. 전체 위협 모델과 포맷은 [security-model.md](docs/architecture/security-model.md)와 [age-format-v1.md](contracts/age-format-v1.md)에서 확인할 수 있어요.

## 명령줄 클라이언트 (`flick`)

`flick`은 브라우저와 동일한 로컬 암호화 방식으로 터미널에서 내용을 보내고 열 수 있는 CLI예요.

### 설치

macOS, Linux, Windows용 바이너리가 각 `cli/v*` [릴리스](https://github.com/Felix-LeeSM/flick-drop/releases)에 `SHA256SUMS` 파일과 함께 올라와 있어요. 실행 전에 체크섬을 꼭 검증하세요.

**macOS / Linux:**

```bash
version=v0.2.0
platform=darwin_arm64   # 또는 darwin_amd64, linux_amd64, linux_arm64
base=https://github.com/Felix-LeeSM/flick-drop/releases/download/cli/$version

curl -fsSLO "$base/flick_${version}_${platform}.tar.gz"
curl -fsSLO "$base/SHA256SUMS"
shasum -a 256 --ignore-missing -c SHA256SUMS   # Linux에서는 sha256sum -c

tar -xzf "flick_${version}_${platform}.tar.gz"
sudo install -m 0755 flick /usr/local/bin/flick
flick version
```

**Windows:** 같은 릴리스에서 `flick_<version>_windows_amd64.tar.gz`와 `SHA256SUMS`를 내려받고 `Get-FileHash flick_<version>_windows_amd64.tar.gz -Algorithm SHA256`으로 검증한 뒤, 압축을 풀어 `flick.exe`를 `PATH` 경로에 두세요.

**소스에서 빌드 (Go 1.25 이상):**

```bash
go install github.com/Felix-LeeSM/flick-drop/cmd/flick@latest
```

> `cli/v*` 태그는 Go 모듈 버전이 아니므로, 특정 버전을 고정하려면 릴리스 페이지에 적힌 커밋 해시로 설치하세요.

### 사용법

```bash
# 사용할 Flick 인스턴스 지정 (아래는 데모 주소이며, 직접 운영 시 본인 도메인으로 변경)
export FLICK_URL=https://flick.makana.cc

# 비밀번호 없이 보내기 (키가 #key=... 에 포함됨)
flick send "sk-live-example"
kubectl get secret db -o jsonpath='{.data.password}' | base64 -d | flick send

# 비밀번호 설정 또는 파일 보내기
flick send -passphrase -ttl 24h "the database password"
flick send -file ./credentials.json -ttl 30m

# 구조화 자격 증명 (웹의 로그인/카드/신원/사용자 정의 폼), JSON으로
flick send -credential login.json

# 대화형으로 입력하며 보내기
flick send

# 열기 (열면 서버에서 바로 사라짐): 페이로드는 0600 파일에 저장되고 경로만 출력돼요
flick open 'https://flick.makana.cc/s/abc123#key=...'
# 페이로드 자체를 출력하려면 (텍스트는 바이트 그대로, 자격 증명은 label: value 행):
flick open -reveal https://flick.makana.cc/s/abc123 > recovered.txt
```

`-reveal`을 넘기기 전에는 복호화된 값이 stdout에 나가지 않아서 터미널 스크롤백이나 에이전트·CI 로그에 비밀이 남지 않아요. 대신 저장된 파일 경로를 알려주고, `flick open -json`은 `-reveal`과 함께 쓰지 않는 한 비밀 필드 값을 빼고 구조만 내보내요.

플래그는 텍스트나 링크 앞뒤 어디에나 쓸 수 있어요. 명령 인자는 `ps`와 셸 히스토리에 남기 때문에 비밀번호는 플래그로 받지 않아요. 스크립트에서는 입력 프롬프트 대신 `FLICK_PASSPHRASE` 환경 변수를 쓰세요.

## 직접 운영하기 (Self-Hosting)

Flick은 관리형 데이터베이스 없이 작은 Kubernetes 클러스터나 OCI Always Free 리소스에서 돌아가도록 설계됐어요. OCI 할당량과 Always Free 정책은 바뀔 수 있으니 본인 테넌시의 현재 한도를 확인하세요.

- **필수 구성:** `flick-web`(빌드한 SvelteKit 앱을 서빙하는 nginx), `flick-api`(Go HTTP API), `flick-worker`(Go 정리 워커), `nats`(NATS JetStream), PostgreSQL 17(`flick_api`, `flick_worker` 데이터베이스).
- **선택 구성:** Valkey(실시간 전송을 쓰려면 필수), S3 호환 오브젝트 스토리지(없으면 파일 크기가 데이터베이스 인라인 한도인 1 MiB로 제한됨).
- **컨테이너 이미지:** `docker.io/haradwaith/flick-api`, `flick-worker`, `flick-web`으로 배포되며 버전 또는 커밋 태그만 제공해요(`latest` 태그 없음).
- **매니페스트와 설정:** Kubernetes 매니페스트는 [`deploy/base`](deploy/base)에 있어요. [k3s 런북](docs/runbook/k3s-base.md)이나 [OCI Always Free 런북](docs/runbook/oci-free-tier.md)을 참고하고, 전체 환경 변수는 [`.env.example`](.env.example)과 [env-contract.md](docs/architecture/env-contract.md)에서 확인하세요.

## 저장소 경계

이 저장소는 공개 저장소예요. 소스 코드, Dockerfile, 범용 Kubernetes 매니페스트, 예시 환경 파일, 문서만 담아요. 자격 증명, kubeconfig, 개인 키, 관리자 토큰, 운영 도메인(위 데모 주소 제외), 실제 버킷 이름, 데이터베이스·PVC·백업 덤프는 비공개 운영 저장소나 로컬 오버레이에 두세요.

## 로컬 개발

도구 버전은 `mise`로 고정돼 있어요.

```sh
mise install
mise trust
direnv allow
```

`.env.example`을 참고해 `.env.local`을 만든 뒤(`.envrc`가 자동 로드, 실제 자격 증명은 커밋 금지) 전체 개발 스택을 띄울 수 있어요.

```sh
mise run dev
```

주요 검증 명령어:

```sh
mise run check       # 로컬 CI 진입점
mise run smoke-nats  # NATS 스모크 테스트
mise run images      # 로컬 컨테이너 이미지 빌드
mise run manifests   # Kubernetes 매니페스트 검증
mise run smoke-k3d   # 로컬 k3d 배포 스모크 테스트 (선택)
```

> Docker, k3d, kubectl, Node 같은 로컬 도구가 빠진 항목은 실패 대신 `skipped`를 출력해요. 전체 통과로 판단하기 전에 출력에 `skipped`가 있는지 확인하세요.

## 문서 (영문)

**아키텍처**
- [Service topology](docs/architecture/service-topology.md) · [Security model](docs/architecture/security-model.md) · [Storage model](docs/architecture/storage-model.md) · [Database schema](docs/architecture/database-schema.md)
- [Event contract](docs/architecture/event-contract.md) · [Request-link contract](docs/architecture/request-links.md) · [Environment contract](docs/architecture/env-contract.md)
- [Deployment target](docs/architecture/deployment-target.md) · [Implementation choices](docs/architecture/implementation-choices.md) · [CI and testing](docs/architecture/ci-testing.md) · [Agent workflow](docs/architecture/agent-workflow.md)

**런북 및 로드맵**
- [Local development](docs/runbook/local-dev.md) · [Container images](docs/runbook/container-images.md) · [k3s base manifests](docs/runbook/k3s-base.md) · [k3d smoke](docs/runbook/k3d-smoke.md) · [OCI Free Tier deployment](docs/runbook/oci-free-tier.md) · [Roadmap](docs/ROADMAP.md)

## 라이선스

Flick은 [Apache License 2.0](LICENSE) 라이선스로 배포돼요.
