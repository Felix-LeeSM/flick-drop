<script lang="ts">
import { t } from '#lib/i18n/index.js';
import { resolve } from '$app/paths';

let { repo }: { repo: string } = $props();
</script>

<h2 id="sending">{t().guides.sections.sending}</h2>

<h3>Flick은 언제 쓰나요?</h3>
<p>임시 비밀번호, Wi-Fi 비밀번호, API 토큰, 계약서, 로그 파일처럼 민감한 내용을 한 번만 건넬 때 써요. 링크를 한 번 열거나 정해진 시간이 지나면 서버에서 사라져요. 보내기 전에 브라우저에서 내용을 잠그고 여는 열쇠는 서버로 보내지 않기 때문에, 공개된 코드 기준으로는 운영자도 내용을 읽을 수 없어요. 이 구조와 한계는 <a href="#privacy">개발자용 설명</a>에서 자세히 다뤄요.</p>

<h3>링크는 어떻게 만드나요?</h3>
<ol>
	<li>보낼 종류를 고르세요. <strong>메모</strong>, <strong>파일</strong>(여러 파일이나 폴더는 하나로 압축돼요), 또는 <strong>로그인</strong>, <strong>카드</strong>, <strong>개인 정보</strong>, <strong>직접 입력</strong> 중에서 고를 수 있어요.</li>
	<li><strong>만료 시간</strong>을 정하세요. 10분, 1시간, 24시간, 7일 중에서 고르거나 5분부터 7일 사이로 직접 넣을 수 있어요.</li>
	<li>필요하면 <strong>비밀번호 걸기</strong>를 켜세요.</li>
	<li><strong>링크 만들기</strong>를 누르고, 만들어진 링크 전체를 상대에게 보내세요. <code>#</code> 뒤가 내용을 여는 열쇠라서 링크가 중간에 잘리면 열리지 않아요.</li>
</ol>

<h3>카카오톡 단톡방처럼 여럿이 보는 곳에 올릴 때는요?</h3>
<p><strong>비밀번호 걸기</strong>를 꼭 켜세요. 비밀번호를 걸면 링크만으로는 열 수 없어요. 비밀번호는 전화나 다른 메신저처럼 링크를 올린 곳과 다른 경로로 알려 주세요. 비밀번호를 5번 틀리면 링크는 영구히 잠겨요.</p>

<h3>상대와 지금 통화 중이거나 마주 보고 있다면요?</h3>
<p>링크 대신 <strong>실시간 전송</strong>을 써 보세요. 두 기기가 모두 열려 있을 때 데이터베이스나 파일 저장소에 아무것도 남기지 않고 바로 내용을 옮겨요. 보내는 쪽에 6자리 코드가 뜨면, 받는 쪽에서 3분 안에 <strong>코드 입력</strong>에 그 코드를 넣으세요. 양쪽 화면에 같은 3자리 확인 번호가 떴는지 말로 맞춰 보고 맞는 기기를 고르면 전송이 끝나요.</p>

<h3>링크를 열면 어떻게 되나요?</h3>
<p>링크를 누르면 먼저 <strong>내용 보기</strong> 버튼이 있는 화면이 열려요. 이 화면을 띄운 것만으로는 내용이 사라지지 않아요. <strong>내용 보기</strong>를 눌러야 내용이 한 번 열리고 서버에서 바로 지워져요. 새로고침하면 다시 볼 수 없으니, 받는 사람은 바로 복사하거나 파일로 저장하세요.</p>

<h3>링크가 열리지 않으면요?</h3>
<p><code>#</code> 뒷부분이 빠진 링크는 열리지 않아요. 아무도 열지 않은 채 <strong>만료 시간</strong>이 지나면 서버에서 자동으로 지워져요. 처음 여는 링크인데 <strong>이 내용은 더 이상 열 수 없어요</strong>라고 나오면 만료됐거나 취소됐거나 누군가 먼저 연 거예요. 누가 먼저 봤을 수 있으니 보낸 사람에게 알리고, 내용이 노출된 것으로 보고 비밀번호 등을 바꿔 달라고 하세요.</p>

<h3>상대에게 비밀번호나 파일을 보내 달라고 하려면요?</h3>
<p><a href={resolve('/request')}>링크로 요청하기</a>를 누르면 링크 두 개가 나와요. <strong>상대에게 줄 링크</strong>를 건네면 상대가 메모, 파일, 로그인 정보를 한 번 보낼 수 있어요. 도착한 내용은 내가 따로 보관해 둔 <strong>내 받기 링크</strong>로 한 번만 열 수 있고, 여러 번 보내려 해도 처음 보낸 것만 받아요.</p>

<h3>아직 안 열렸는데 보낸 걸 취소하고 싶어요.</h3>
<p>링크를 만든 직후 받은 <strong>비공개 관리 링크</strong>를 열어 보세요. 상대가 아직 열기 전이라면 그 페이지에서 바로 취소할 수 있어요.</p>

<h2 id="limits">{t().guides.sections.limits}</h2>
<p>Flick은 서버에서 내용을 한 번 지워 줄 뿐, 아래 상황까지는 막지 못해요.</p>
<ul>
	<li><strong>이미 본 내용 회수 불가:</strong> 받는 사람이 내용을 복사하거나 화면을 캡처하거나 파일을 저장하면 되돌릴 수 없어요. 임시 비밀번호나 토큰은 쓰임이 끝나면 꼭 바꾸세요.</li>
	<li><strong>열람자 확인과 감염 기기 보호 불가:</strong> 링크 전체를 가진 사람은 누구나 한 번 열 수 있고, Flick은 누가 열었는지 증명하지 않아요. 이미 악성코드에 감염된 기기도 지킬 수 없어요.</li>
	<li><strong>사본 잔류 주의:</strong> 메신저 대화창, 클립보드, 브라우저 방문 기록에 링크나 복사한 내용이 남을 수 있어요.</li>
	<li><strong>서버 신뢰 필요:</strong> 내용을 잠그는 코드는 접속한 Flick 서버에서 내려와요. 운영자를 믿을 수 있는 곳을 쓰거나 <a href="#self-host">직접 운영</a>해야 해요.</li>
</ul>

<p class="guide-divider">개발자·운영자용</p>

<h2 id="privacy">{t().guides.sections.privacy}</h2>
<p>브라우저는 오픈 암호화 형식인 <a href="https://age-encryption.org/">age</a>로 내용을 암호화해요. 전달할 때마다 새 양자 내성 하이브리드 키(ML-KEM-768 + X25519)를 만들며, 큰 파일은 64 KiB 청크 단위로 암호화해 마지막 청크까지 검증되기 전에는 아무것도 표시하지 않아요.</p>
<p>비밀번호를 걸지 않으면 링크 키가 URL 프래그먼트(<code>#key=AGE-SECRET-KEY-PQ-1…</code>)에 들어가요. 브라우저는 프래그먼트를 서버로 전송하지 않으므로 HTTP 요청이나 웹 접속 로그에 키가 남지 않아요.</p>
<p>비밀번호를 걸면 키를 age의 scrypt 모드(work factor 18)로 감싸고, 감싼 사본만 서버에 올려요. 동시에 비밀번호에서 별도의 접근 증명(PBKDF2-SHA-256, 600,000회 반복)을 유도하고, 서버는 이 증명의 해시만 저장해 내용을 내주기 전에 대조해요. 데이터베이스 사본이 유출되면 공격자가 감싼 키를 상대로 오프라인에서 비밀번호를 추측할 수 있으므로 긴 비밀번호를 써야 해요. 내장 생성기는 80비트 비밀번호를 만들어요.</p>
<p>서버는 전달 ID, 암호화된 바이트, 크기, 파일 형식, 암호화된 파일 이름, 타임스탬프, 비밀번호 오입력 횟수를 저장해요. 클라이언트 IP는 저장하지 않아요. 웹 접속 로그에서는 전달 ID를 <code>&lt;id&gt;</code>로 가리고, 클라이언트 IP, 리퍼러, 쿼리 문자열을 기록하지 않아요.</p>
<p>실시간 전송은 다르게 동작해요. 받는 사람의 브라우저가 일회용 키 쌍을 생성해 공개 키만 보내면, 보내는 사람이 그 공개 키로 암호화하고 서버는 암호문을 메모리에서 중계해요. 코드 해시와 기기 표시 이름 같은 대기실 정보는 영구 저장하지 않는 캐시에 잠깐만 머물러요.</p>
<aside>
	<p><strong>웹 암호화의 한계:</strong> 사용자는 결국 서버가 내려주는 JavaScript를 믿어야 해요. 악의적인 서버는 키나 비밀번호를 빼돌리는 코드를 보낼 수 있고, 실시간 전송에서는 자신의 공개 키로 바꿔치기할 수도 있어요. 이 구조는 데이터베이스, 로그, 백업 유출을 방어하지만 운영자가 악의를 품은 경우까지는 막지 못해요. <code>SHA256SUMS</code>로 검증한 명령줄 클라이언트를 쓰면 서버가 보낸 JavaScript를 믿지 않아도 돼요.</p>
</aside>

<h2 id="source">{t().guides.sections.source}</h2>
<p>Flick은 Apache 2.0 라이선스 오픈 소스예요. 저장소는 <a href={repo}>github.com/Felix-LeeSM/flick-drop</a>이며, 구조를 파악할 때 아래 경로를 먼저 보면 좋아요.</p>
<ul>
	<li><code>web/src/lib/crypto/</code>: 브라우저 암호화, 링크 키, 비밀번호 접근 증명</li>
	<li><code>contracts/age-format-v1.md</code>: 암호화 포맷과 API 호출별 페이로드 명세</li>
	<li><code>docs/architecture/security-model.md</code>: 범위 밖 위협까지 포함한 전체 위협 모델</li>
	<li><code>cmd/flick</code>: 브라우저와 동일한 방식으로 암호화하는 명령줄 클라이언트</li>
</ul>
<p>명령줄 클라이언트를 설치하면 터미널에서 바로 보내고 열 수 있어요.</p>
<pre><code>go install github.com/Felix-LeeSM/flick-drop/cmd/flick@latest
flick send -ttl 1h -file report.pdf
flick open 'https://flick.example/s/…#key=…'</code></pre>
<p>macOS, Linux, Windows용 빌드와 <code>SHA256SUMS</code> 파일은 각 <code>cli/v*</code> 릴리스에 올라와요. <code>FLICK_URL</code> 환경 변수에 사용할 인스턴스 주소를 지정하세요.</p>

<h2 id="self-host">{t().guides.sections.selfHost}</h2>
<p>Flick은 직접 제어하는 작은 서버에서 운영하도록 설계됐어요. 전체 구성은 웹 프런트엔드(빌드한 SvelteKit 앱을 서빙하는 nginx), Go API, Go 정리 워커, NATS JetStream, PostgreSQL 17로 이루어져요. Valkey는 선택 사항이지만 실시간 전송을 쓰려면 필요해요. S3 호환 버킷도 선택 사항이며, 없으면 파일 크기가 데이터베이스에 저장할 수 있는 1 MiB로 제한돼요.</p>
<p>컨테이너 이미지는 <code>docker.io/haradwaith/flick-api</code>, <code>flick-worker</code>, <code>flick-web</code>으로 배포되며, 버전이나 커밋 태그만 제공해요(<code>latest</code> 태그는 없어요). 소스에서 직접 빌드할 수도 있어요.</p>
<p>저장소 안의 <code>deploy/base</code>에 Kubernetes 매니페스트가 있고, k3s 클러스터용 런북(<code>docs/runbook/k3s-base.md</code>)과 OCI Always Free 리소스용 런북(<code>docs/runbook/oci-free-tier.md</code>)을 따라 구축할 수 있어요. 모든 환경 설정은 <code>.env.example</code>과 <code>docs/architecture/env-contract.md</code>에 정리돼 있어요.</p>
