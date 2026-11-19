<script lang="ts">
import { t } from '#lib/i18n/index.js';
import { resolve } from '$app/paths';

let { repo }: { repo: string } = $props();
</script>

<h2 id="sending">{t().guides.sections.sending}</h2>

<h3>Flick은 어디에 쓰나요?</h3>
<p>민감한 내용을 한 번 건네줄 때 써요. 임시 비밀번호, Wi-Fi 비밀번호, API 토큰, 계약서, 로그 파일 같은 것들이요. 한 번 열리면 더는 쓸 수 없는 링크가 만들어져요. Flick은 비밀번호 관리자나 파일 보관함이 아니에요. 전달이 끝나면 아무것도 남지 않아요.</p>

<h3>Flick 운영자가 내가 보낸 내용을 읽을 수 있나요?</h3>
<p>Flick이 공개한 코드로는 읽을 수 없어요. 내용은 기기를 떠나기 전에 브라우저에서 암호화되고, 내용을 여는 열쇠는 서버에 전달되지 않아요. 서버에는 서버도 열 수 없게 잠긴 데이터만 저장돼요. 어떻게 그렇게 되는지, 그리고 이 약속이 어디까지인지는 <a href="#privacy">개발자용 설명</a>에 있어요.</p>

<h3>어떻게 보내나요?</h3>
<ol>
	<li>보낼 것을 골라요. <strong>메모</strong>, <strong>파일</strong>(여러 파일이나 폴더는 하나로 묶어 압축해요), 또는 <strong>로그인</strong>, <strong>카드</strong>, <strong>개인 정보</strong>, <strong>직접 입력</strong> 템플릿 중에서 고를 수 있어요.</li>
	<li>얼마나 기다릴지 정해요. 10분, 1시간, 24시간, 7일 중에서 고르거나 5분에서 7일 사이로 직접 정할 수 있어요.</li>
	<li>필요하면 비밀번호를 걸어요.</li>
	<li><strong>링크 만들기</strong>를 누르고 링크 전체를 받는 사람에게 보내요. <code>#</code> 뒷부분이 열쇠라서 링크가 잘리면 열 수 없어요.</li>
</ol>

<h3>받는 사람에게는 무엇이 보이나요?</h3>
<p>링크를 열면 <strong>내용 보기</strong> 버튼이 있는 페이지가 나와요. 이 페이지를 불러오는 것만으로는 링크가 쓰이지 않아요. <strong>내용 보기</strong>를 누르면 내용이 한 번 열리고 서버에서 지워져요. 그 뒤에 새로고침하면 더는 열 수 없다고 나오니, 받는 사람은 필요한 내용을 바로 복사하거나 저장해 두세요.</p>

<h3>아무도 열지 않으면요?</h3>
<p>정한 시간이 지나면 지워져요. 어느 쪽이든 링크는 많아야 한 번 열려요.</p>

<h3>비밀번호는 언제 거나요?</h3>
<p>다른 사람이 링크를 볼 수도 있을 때 걸어요. 여러 사람이 있는 채팅방에 보낼 때가 그래요. 비밀번호를 걸면 링크만으로는 열 수 없어요. 비밀번호는 전화나 다른 메신저처럼 다른 방법으로 알려 주세요. 비밀번호를 다섯 번 틀리면 다시는 열 수 없게 잠겨요.</p>

<h3>실시간 전송은 뭔가요?</h3>
<p><strong>실시간 전송</strong>은 두 기기가 모두 열려 있을 때 내용을 옮기는 방법이에요. 보내는 사람은 6자리 코드를 받고, 받는 사람은 3분 안에 <strong>코드 입력</strong>에 그 코드를 넣어요. 기다리는 기기마다 3자리 확인 번호가 보여요. 받는 사람이 그 번호를 불러 주면 보내는 사람이 맞는 기기를 골라요. 데이터베이스나 파일 저장소에는 아무것도 기록되지 않아요. 내용은 전송되는 동안에만 서버를 거쳐 가요.</p>

<h3>다른 사람에게서 받을 수도 있나요?</h3>
<p>네. <a href={resolve('/request')}>링크로 요청하기</a>를 쓰면 링크가 두 개 만들어져요. 상대는 내가 건넨 <strong>상대에게 줄 링크</strong>로 메모, 파일, 로그인 정보를 보내고, 나는 나만 보관하는 <strong>내 받기 링크</strong>로 도착한 내용을 한 번 열어요. 여러 번 보내면 가장 먼저 보낸 내용만 받아요.</p>

<h3>링크가 새어 나가면요?</h3>
<p>링크 전체를 가진 사람은 누구나 한 번 열 수 있어요. 받는 사람이 열려고 했는데 이미 열린 상태라면 내용이 노출됐다고 보고 바꿔 주세요. 보내는 사람은 아직 열리지 않은 링크를 <strong>비공개 관리 링크</strong>에서 취소할 수도 있어요.</p>

<h2 id="limits">{t().guides.sections.limits}</h2>
<p>Flick은 받는 사람이 이미 본 내용을 되돌릴 수 없어요. 받는 사람은 내용을 복사하거나 화면을 캡처하거나 파일을 보관할 수 있어요. 한 번만 열리는 링크로 보냈다고 그 안의 비밀번호가 일회용이 되지는 않아요. 임시로 건넨 로그인 정보는 쓸 일이 끝나면 바꿔 주세요.</p>
<p>Flick은 누가 링크를 열었는지 증명하지 못하고, 이미 해킹된 기기에 있는 내용은 지킬 수 없어요. 내용을 암호화하는 페이지는 지금 쓰는 Flick 서버에서 내려받아요. 그러니 믿을 수 있는 사람이 운영하는 서버를 쓰거나 <a href="#self-host">직접 운영하세요</a>. 채팅 기록, 클립보드, 브라우저 방문 기록에 사본이 남을 수도 있어요.</p>

<p class="guide-divider">개발자·운영자용</p>

<h2 id="privacy">{t().guides.sections.privacy}</h2>
<p>내용은 공개된 파일 암호화 형식인 <a href="https://age-encryption.org/">age</a>로 브라우저에서 암호화돼요. 전달할 때마다 새로 만든 양자 내성 하이브리드 키(ML-KEM-768과 X25519)를 써요. 큰 파일은 64 KiB 조각으로 나눠 암호화하고, 마지막 조각까지 검증되기 전에는 아무것도 보여 주지 않아요.</p>
<p>비밀번호가 없으면 키는 링크의 프래그먼트(<code>#</code> 뒷부분)인 <code>#key=AGE-SECRET-KEY-PQ-1…</code>에 담겨요. 브라우저는 프래그먼트를 서버로 보내지 않아서 서버로 가는 통신이나 접속 로그에 남지 않아요.</p>
<p>비밀번호를 걸면 키를 age의 scrypt 모드(work factor 18)로 감싸고, 감싼 사본만 업로드해요. 비밀번호로는 별도의 접근 증명(PBKDF2-SHA-256, 600,000회 반복)도 만들어요. 서버는 그 증명의 해시만 보관하고, 내용을 내주기 전에 확인해요. 데이터베이스 사본을 가진 사람은 감싼 키를 두고 오프라인에서 비밀번호를 계속 추측해 볼 수 있으니 긴 비밀번호를 쓰세요. 비밀번호 생성기는 80비트 비밀번호를 만들어요.</p>
<p>서버에는 ID, 암호화된 데이터, 그 크기, 파일 형식, 암호화된 파일 이름, 시각 정보, 비밀번호를 틀린 횟수가 저장돼요. IP 주소는 저장하지 않아요. 웹 접속 로그는 링크 ID를 <code>&lt;id&gt;</code>로 가리고, 클라이언트 IP, 리퍼러, 쿼리 문자열을 기록하지 않아요.</p>
<p>실시간 전송은 방식이 달라요. 받는 사람의 브라우저가 한 번만 쓰는 키 쌍을 만들고 공개 키만 보내요. 보내는 사람은 그 공개 키로 암호화하고, 서버는 암호문을 메모리에서 중계해요. 코드의 해시나 기기 표시 이름 같은 대기 정보는 영구 저장하지 않는 캐시에 잠깐만 머물러요.</p>
<aside>
	<p><strong>모든 웹 암호화의 솔직한 한계:</strong> 서버가 보내 주는 JavaScript를 믿어야 해요. 작정하고 악의를 품은 서버라면 키나 비밀번호를 훔치는 코드를 보낼 수 있고, 실시간 전송에서는 자기 키로 바꿔치기할 수도 있어요. 이 설계는 데이터베이스, 로그, 백업이 유출되는 상황을 막지만, 운영자가 악의를 품는 상황까지 막지는 못해요. <code>SHA256SUMS</code>로 검증한 명령줄 클라이언트를 쓰면 서버가 보내 주는 코드를 믿지 않아도 돼요.</p>
</aside>

<h2 id="source">{t().guides.sections.source}</h2>
<p>Flick은 Apache 2.0 라이선스로 공개된 오픈 소스예요. 저장소는 <a href={repo}>github.com/Felix-LeeSM/flick-drop</a>이고, 처음 읽기 좋은 곳은 다음과 같아요.</p>
<ul>
	<li><code>web/src/lib/crypto/</code>: 브라우저 암호화, 링크 키, 비밀번호 접근 증명.</li>
	<li><code>contracts/age-format-v1.md</code>: 암호화 형식과 API 호출마다 담기는 내용.</li>
	<li><code>docs/architecture/security-model.md</code>: 다루지 않는 범위까지 포함한 전체 위협 모델.</li>
	<li><code>cmd/flick</code>: 브라우저와 같은 방식으로 암호화하는 명령줄 클라이언트.</li>
</ul>
<p>명령줄 클라이언트로 터미널에서 보내고 열 수 있어요.</p>
<pre><code>go install github.com/Felix-LeeSM/flick-drop/cmd/flick@latest
flick send -ttl 1h -file report.pdf
flick open 'https://flick.example/s/…#key=…'</code></pre>
<p>macOS, Linux, Windows용 릴리스 빌드는 <code>cli/v*</code> 릴리스마다 <code>SHA256SUMS</code> 파일과 함께 첨부돼요. <code>FLICK_URL</code>에는 사용하는 인스턴스 주소를 넣어 주세요.</p>

<h2 id="self-host">{t().guides.sections.selfHost}</h2>
<p>Flick은 직접 관리하는 작은 서버에서 돌아가도록 만들었어요. 배포는 웹 프런트엔드(빌드한 SvelteKit 앱을 제공하는 nginx), Go API, Go 정리 워커, NATS JetStream, PostgreSQL 17로 이루어져요. Valkey는 선택이지만 실시간 전송을 쓰려면 필요해요. S3 호환 버킷도 선택이에요. 버킷이 없으면 파일은 데이터베이스에 들어가는 1 MiB까지로 제한돼요.</p>
<p>이미지는 <code>docker.io/haradwaith/flick-api</code>, <code>flick-worker</code>, <code>flick-web</code>으로 공개되고, 버전이나 커밋으로 태그가 붙어요(<code>latest</code> 태그는 없어요). 소스에서 직접 빌드할 수도 있어요.</p>
<p>저장소에는 <code>deploy/base</code>의 Kubernetes 매니페스트와 함께 k3s 클러스터(<code>docs/runbook/k3s-base.md</code>)와 OCI Always Free 리소스(<code>docs/runbook/oci-free-tier.md</code>)용 단계별 런북이 들어 있어요. 모든 설정은 <code>.env.example</code>과 <code>docs/architecture/env-contract.md</code>에 정리돼 있어요.</p>
