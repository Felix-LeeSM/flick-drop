#!/usr/bin/env node
// 프로토타입용 녹화·재생 mock API 서버.
//
//   node scripts/dev/mock-api.mjs record <카세트>   실제 API로 넘기고 응답을 녹화
//   node scripts/dev/mock-api.mjs replay <카세트>   API 없이 녹화된 응답만 돌려줌
//
// 웹은 PUBLIC_FLICK_API_BASE_URL=http://localhost:8090 으로 이 서버를 가리킨다.
// 카세트는 var/mock-api/<이름>.json 이며 var/ 는 .gitignore 대상이다. 카세트에는
// 관리 토큰과 암호문이 들어가므로 저장소에 커밋하지 않는다. 예외로 화면 테스트용
// 카세트는 로컬 dev 스택에서 합성 데이터만으로 녹화해 tests/fixtures/cassettes/ 에
// 커밋하고(FLICK_MOCK_DIR=tests/fixtures/cassettes), routeCassette() 로 Playwright
// page.route 에 연결한다.
//
// 재생은 "메서드 + 경로"(쿼리 제외)로 짝을 찾고, 같은 짝이 여러 번 녹화됐으면
// 녹화 순서대로 돌려준 뒤 마지막 응답을 반복한다. 그래서 "첫 open 200, 두 번째
// 410" 같은 한 번 열기 흐름이 녹화 그대로 재현된다. POST /__mock/reset 은 순서를
// 처음으로 되돌리고, POST /__mock/use/<카세트> 는 다른 카세트로 바꾼다.
//
// 대용량 업로드: 녹화 때 JSON 응답의 presigned `url` 을 이 서버의
// /__storage/<base64url(원래 주소)> 로 바꿔, 브라우저의 오브젝트 스토리지 PUT 도 이
// 서버를 거쳐 녹화·재생되게 한다.
//
// 코드로 보내기(/api/pipe/*): 받는 브라우저는 시도마다 새 age 키를 만들므로 녹화한
// 암호문은 재생 때 풀리지 않는다. 카세트의 pipe 필드({ text, filename? })에 녹화한
// 합성 평문을 적어 두면, 재생 때 /api/pipe/join 요청의 새 recipient 로 그 평문을
// 다시 암호화해 /api/pipe/receive 응답으로 돌려준다. long-poll(/wait, /status,
// /receive)은 녹화에서 그보다 먼저 끝난 pipe 요청이 모두 재생될 때까지 붙잡는다.
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:http';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const defaultCassetteDir = join(repoRoot, 'var/mock-api');
const storagePrefix = '/__storage/';
// 상대 기기의 동작을 기다리는 코드로 보내기 long-poll 요청.
const longPollMaxWaitMs = 30_000;
const longPollKeys = new Set([
	'GET /api/pipe/wait',
	'GET /api/pipe/status',
	'GET /api/pipe/receive'
]);
// 녹화 중 사람이 카세트에 적어 넣고, 녹화기가 덮어쓰지 않는 필드.
const handWrittenFields = ['link', 'pipe'];

// 응답에서 버리는 헤더: 전송 계층 값과, 이 서버가 직접 붙이는 CORS 헤더.
const droppedResponseHeaders = new Set([
	'connection',
	'content-encoding',
	'content-length',
	'date',
	'keep-alive',
	'transfer-encoding',
	'vary'
]);
// 실제 API로 넘기지 않는 요청 헤더. Origin 을 빼야 실제 API 의 CORS 설정과 무관하게 녹화된다.
const droppedRequestHeaders = new Set([
	'connection',
	'host',
	'origin',
	'referer',
	'accept-encoding',
	'content-length'
]);

// open 응답은 메타데이터를 Flick-* 헤더로 싣는다 (internal/httpapi/router.go:cors).
const exposedHeaders =
	'Flick-Kind, Flick-Content-Type, Flick-Encrypted-Filename, Flick-Wrapped-Identity, Flick-Expires-At';

function cassettePath(dir, name) {
	if (!/^[\w.-]+$/.test(name)) {
		throw new Error(`카세트 이름은 영문·숫자·._- 만 쓴다: ${name}`);
	}
	return join(dir, `${name}.json`);
}

function loadCassette(path) {
	const cassette = JSON.parse(readFileSync(path, 'utf8'));
	if (!Array.isArray(cassette.exchanges)) {
		throw new Error(`exchanges 배열이 없다: ${path}`);
	}
	// 손으로 쓴 카세트의 "Content-Type" 도 녹화본처럼 소문자 이름으로 읽는다.
	for (const exchange of cassette.exchanges) {
		exchange.headers = Object.fromEntries(
			Object.entries(exchange.headers).map(([name, value]) => [name.toLowerCase(), value])
		);
	}
	return cassette;
}

// 링크의 #fragment 에는 복호화 키가 있으므로 로그에는 경로만 남긴다.
function linkForLog(link) {
	return link ? ` 링크 경로: ${new URL(link).pathname} (전체 링크는 카세트 link 필드)` : '';
}

function exchangeKey(method, url) {
	return `${method} ${new URL(url, 'http://mock').pathname}`;
}

function setCors(req, res) {
	const origin = req.headers.origin;
	if (!origin) {
		return;
	}
	res.setHeader('Access-Control-Allow-Origin', origin);
	res.setHeader('Access-Control-Allow-Methods', 'GET, POST, PUT, HEAD, OPTIONS');
	res.setHeader(
		'Access-Control-Allow-Headers',
		req.headers['access-control-request-headers'] ?? 'Content-Type, Authorization'
	);
	res.setHeader('Access-Control-Expose-Headers', exposedHeaders);
	res.setHeader('Vary', 'Origin');
}

async function readBody(req) {
	const chunks = [];
	for await (const chunk of req) {
		chunks.push(chunk);
	}
	return Buffer.concat(chunks);
}

function sendJson(res, status, body) {
	res.writeHead(status, { 'Content-Type': 'application/json' });
	res.end(JSON.stringify(body));
}

// RFC 3339 시각만 옮긴다. Date.parse 는 "1" 같은 값도 날짜로 읽기 때문이다.
function isTimestamp(value) {
	return typeof value === 'string' && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}/.test(value);
}

function shiftTime(value, offsetMs) {
	return new Date(Date.parse(value) + offsetMs).toISOString();
}

// 재생 시점에 맞춰 만료 시각을 옮긴다. 관리 페이지는 management_expires_at 을
// Date.now() 와 비교하므로(web/src/lib/components/ManageSecretPage.svelte), 옮기지
// 않으면 TTL 이 지난 카세트는 만료 화면만 보여 준다. JSON 의 *_at 필드와
// Flick-Expires-At 헤더를 "녹화 시점부터 흐른 시간"만큼 뒤로 민다.
function buildResponse(exchange, offsetMs) {
	const headers = { ...exchange.headers };
	if (isTimestamp(headers['flick-expires-at'])) {
		headers['flick-expires-at'] = shiftTime(headers['flick-expires-at'], offsetMs);
	}
	let body = Buffer.from(exchange.body, exchange.bodyEncoding);
	const isJson = /json/.test(headers['content-type'] ?? '') && exchange.body.length > 0;
	if (offsetMs !== 0 && isJson) {
		const shifted = JSON.parse(exchange.body, (key, value) =>
			key.endsWith('_at') && isTimestamp(value) ? shiftTime(value, offsetMs) : value
		);
		body = Buffer.from(JSON.stringify(shifted));
	}
	return { status: exchange.status, headers, body };
}

function sendResponse(res, { status, headers, body }) {
	res.writeHead(status, headers);
	res.end(body);
}

/**
 * 카세트 하나의 재생 순서를 들고 있는 재생기. mock 서버와 Playwright 어댑터가 함께 쓴다.
 * next() 는 녹화가 없으면 null 을 돌려준다.
 */
export function createReplayer(cassette) {
	let served = new Map();
	// 재생한 exchange 의 카세트 내 순번.
	let replayed = new Set();
	// reset 마다 올린다. 이전 세대의 long-poll 은 새 세대의 순서 기록을 건드리지 않는다.
	let generation = 0;
	// 재생 중 /api/pipe/join 이 보낸 받는 브라우저의 age recipient.
	let pipeRecipient = null;

	// long-poll 응답은 녹화에서 그보다 먼저 끝난 pipe 요청이 모두 재생될 때까지 붙잡는다.
	// 예: 보내는 쪽의 "받는 사람 접속" /wait 응답은 받는 쪽 /join 이 재생된 뒤에 나간다.
	// 녹화와 다른 횟수로 polling 하면 앞선 요청이 끝내 재생되지 않을 수 있어, 실제 long-poll
	// 처럼 longPollMaxWaitMs 가 지나면 기다리지 않고 응답한다.
	async function waitForEarlierPipeExchanges(position, startedGeneration) {
		const earlier = cassette.exchanges
			.map((exchange, index) => ({ exchange, index }))
			.filter(({ exchange, index }) => index < position && exchange.path.startsWith('/api/pipe/'))
			.map(({ index }) => index);
		const deadline = Date.now() + longPollMaxWaitMs;
		while (!earlier.every((index) => replayed.has(index)) && Date.now() < deadline) {
			await new Promise((resolveTick) => setTimeout(resolveTick, 25));
			if (generation !== startedGeneration) {
				throw new Error('/__mock/reset 으로 끊긴 long-poll');
			}
		}
	}

	return {
		/**
		 * @param {string} method
		 * @param {string} url
		 * @param {Buffer} requestBody
		 */
		async next(method, url, requestBody) {
			const key = exchangeKey(method, url);
			if (key === 'POST /api/pipe/join') {
				pipeRecipient = JSON.parse(requestBody.toString('utf8')).recipient;
			}
			const matches = cassette.exchanges.filter((e) => `${e.method} ${e.path}` === key);
			if (matches.length === 0) {
				return null;
			}
			const index = served.get(key) ?? 0;
			served.set(key, index + 1);
			const exchange = matches[Math.min(index, matches.length - 1)];
			const position = cassette.exchanges.indexOf(exchange);
			if (longPollKeys.has(key)) {
				await waitForEarlierPipeExchanges(position, generation);
			}
			replayed.add(position);
			// 녹화 세션이 길었어도 각 응답이 녹화된 시각 기준으로 옮긴다.
			const offsetMs = Date.now() - Date.parse(exchange.at ?? cassette.recordedAt);
			const response = buildResponse(exchange, offsetMs);
			const isPipeBody = key === 'GET /api/pipe/receive' && response.status === 200;
			if (!isPipeBody || cassette.pipe === undefined) {
				return response;
			}
			if (pipeRecipient === null) {
				throw new Error('/api/pipe/receive 재생 전에 /api/pipe/join 의 recipient 가 없다');
			}
			return reencryptPipe(response, cassette.pipe, pipeRecipient);
		},
		reset() {
			generation += 1;
			served = new Map();
			replayed = new Set();
			pipeRecipient = null;
		}
	};
}

async function encryptTo(recipient, bytes) {
	// age-encryption 은 web/ 의존성이다. 코드로 보내기 재생에서만 불러온다.
	const { Encrypter } = await import(
		pathToFileURL(join(repoRoot, 'web/node_modules/age-encryption/dist/index.js'))
	);
	const encrypter = new Encrypter();
	encrypter.addRecipient(recipient);
	return Buffer.from(await encrypter.encrypt(bytes));
}

// 녹화된 receive 응답의 본문과 파일 이름을 재생 중인 받는 브라우저의 recipient 로 다시 암호화한다.
async function reencryptPipe(response, pipe, recipient) {
	if (typeof pipe.text !== 'string') {
		throw new Error('카세트 pipe.text 에 녹화한 평문을 적어야 한다');
	}
	const headers = { ...response.headers };
	if (headers['flick-encrypted-filename']) {
		if (typeof pipe.filename !== 'string') {
			throw new Error('파일을 보낸 녹화는 카세트 pipe.filename 이 필요하다');
		}
		headers['flick-encrypted-filename'] = (
			await encryptTo(recipient, Buffer.from(pipe.filename))
		).toString('base64');
	}
	return { ...response, headers, body: await encryptTo(recipient, Buffer.from(pipe.text)) };
}

// 녹화 때 presigned 업로드 주소를 이 서버 경유 주소로 바꾼다.
function rewriteStorageUrls(text, mockOrigin) {
	const rewritten = JSON.parse(text, (key, value) =>
		key === 'url' && typeof value === 'string' && /^https?:\/\//.test(value)
			? `${mockOrigin}${storagePrefix}${Buffer.from(value).toString('base64url')}`
			: value
	);
	return JSON.stringify(rewritten);
}

/**
 * Playwright 화면 테스트에서 /api/* 요청을 카세트 파일로 응답한다.
 * 카세트의 link 는 녹화 때의 호스트를 떼고 경로와 #fragment 만 돌려준다.
 * @param {import('playwright').Page | import('playwright').BrowserContext} target
 * @param {string | URL} file 카세트 JSON 경로
 * @returns {Promise<{ linkPath: string }>}
 */
export async function routeCassette(target, file) {
	const cassette = loadCassette(file);
	const replayer = createReplayer(cassette);
	// '**/api/**' 글롭은 Vite 개발 서버의 /src/lib/api/*.ts 모듈까지 잡으므로 경로 시작으로 고른다.
	await target.route(
		(url) => url.pathname.startsWith('/api/') || url.pathname.startsWith(storagePrefix),
		async (route) => {
			const request = route.request();
			const response = await replayer.next(
				request.method(),
				request.url(),
				request.postDataBuffer() ?? Buffer.alloc(0)
			);
			if (response === null) {
				await route.fulfill({
					status: 501,
					json: {
						error: 'mock_no_recording',
						request: exchangeKey(request.method(), request.url())
					}
				});
				return;
			}
			await route.fulfill({
				status: response.status,
				// 브라우저는 교차 출처 응답의 Flick-* 헤더를 노출 목록에 있을 때만 읽는다.
				headers: { ...response.headers, 'access-control-expose-headers': exposedHeaders },
				body: response.body
			});
		}
	);
	const link = cassette.link ? new URL(cassette.link) : null;
	return { linkPath: link ? `${link.pathname}${link.hash}` : '' };
}

function encodeBody(buffer, contentType) {
	const isText = /json|text|xml|javascript/.test(contentType ?? '');
	return isText
		? { body: buffer.toString('utf8'), bodyEncoding: 'utf8' }
		: { body: buffer.toString('base64'), bodyEncoding: 'base64' };
}

/**
 * mock 서버를 띄운다.
 * @param {{ mode: 'record' | 'replay', cassette: string, cassetteDir?: string,
 *   target?: string, port?: number, log?: (line: string) => void }} options
 * @returns {Promise<import('node:http').Server>}
 */
export async function startMockApi({
	mode,
	cassette,
	cassetteDir = defaultCassetteDir,
	target = 'http://localhost:8080',
	port = 8090,
	log = (line) => process.stderr.write(`${line}\n`)
}) {
	if (mode !== 'record' && mode !== 'replay') {
		throw new Error(`알 수 없는 모드: ${mode}`);
	}

	let currentPath = cassettePath(cassetteDir, cassette);
	let current;
	if (mode === 'record') {
		// 커밋한 테스트 카세트를 실수로 덮어쓰지 않도록, 있는 파일은 지운 뒤에만 다시 녹화한다.
		if (existsSync(currentPath)) {
			throw new Error(`이미 있는 카세트다. 다시 녹화하려면 먼저 지운다: ${currentPath}`);
		}
		mkdirSync(cassetteDir, { recursive: true });
		current = { target, recordedAt: new Date().toISOString(), link: '', exchanges: [] };
		writeFileSync(currentPath, JSON.stringify(current, null, '\t'));
	} else {
		current = loadCassette(currentPath);
	}
	let replayer = createReplayer(current);

	function switchCassette(name) {
		currentPath = cassettePath(cassetteDir, name);
		current = loadCassette(currentPath);
		replayer = createReplayer(current);
		log(`[mock-api] 카세트 ${name} (${current.exchanges.length}건)${linkForLog(current.link)}`);
	}

	async function record(req, res, requestBody) {
		const headers = Object.fromEntries(
			Object.entries(req.headers).filter(([name]) => !droppedRequestHeaders.has(name))
		);
		const pathname = new URL(req.url, 'http://mock').pathname;
		const upstreamUrl = pathname.startsWith(storagePrefix)
			? Buffer.from(pathname.slice(storagePrefix.length), 'base64url').toString('utf8')
			: new URL(req.url, target);
		const upstream = await fetch(upstreamUrl, {
			method: req.method,
			headers,
			body: requestBody.length > 0 ? requestBody : undefined,
			redirect: 'manual'
		});
		const responseHeaders = Object.fromEntries(
			[...upstream.headers].filter(
				([name]) => !droppedResponseHeaders.has(name) && !name.startsWith('access-control-')
			)
		);
		const encoded = encodeBody(
			Buffer.from(await upstream.arrayBuffer()),
			upstream.headers.get('content-type')
		);
		if (/json/.test(responseHeaders['content-type'] ?? '') && encoded.body.length > 0) {
			encoded.body = rewriteStorageUrls(encoded.body, `http://${req.headers.host}`);
		}
		const exchange = {
			method: req.method,
			path: pathname,
			at: new Date().toISOString(),
			status: upstream.status,
			headers: responseHeaders,
			...encoded
		};
		current.exchanges.push(exchange);
		// 녹화 중에 사람이 카세트 파일에 적어 넣은 link(#fragment 의 키 포함)와 pipe 평문을 보존한다.
		const onDisk = JSON.parse(readFileSync(currentPath, 'utf8'));
		for (const field of handWrittenFields) {
			current[field] = onDisk[field] ?? current[field];
		}
		// 요청마다 파일에 써서 Ctrl-C 로 끝내도 녹화가 남게 한다.
		writeFileSync(currentPath, JSON.stringify(current, null, '\t'));
		sendResponse(res, buildResponse(exchange, 0));
	}

	async function replay(req, res, requestBody) {
		const response = await replayer.next(req.method, req.url, requestBody);
		if (response === null) {
			const key = exchangeKey(req.method, req.url);
			log(`[mock-api] 녹화 없음: ${key}`);
			sendJson(res, 501, { error: 'mock_no_recording', request: key });
			return;
		}
		sendResponse(res, response);
	}

	// /__mock/* 제어 요청을 처리했으면 true.
	function handleControl(req, res, pathname) {
		if (req.method !== 'POST') {
			return false;
		}
		if (pathname === '/__mock/reset') {
			replayer.reset();
			sendJson(res, 200, { reset: true });
			return true;
		}
		if (!pathname.startsWith('/__mock/use/')) {
			return false;
		}
		if (mode !== 'replay') {
			throw new Error('카세트 전환은 replay 모드에서만 된다');
		}
		switchCassette(decodeURIComponent(pathname.slice('/__mock/use/'.length)));
		sendJson(res, 200, { cassette: currentPath, exchanges: current.exchanges.length });
		return true;
	}

	const server = createServer(async (req, res) => {
		setCors(req, res);
		if (req.method === 'OPTIONS') {
			res.writeHead(204);
			res.end();
			return;
		}
		const { pathname } = new URL(req.url, 'http://mock');
		try {
			if (handleControl(req, res, pathname)) {
				return;
			}
			const requestBody = await readBody(req);
			if (mode === 'record') {
				await record(req, res, requestBody);
			} else {
				await replay(req, res, requestBody);
			}
			log(`[mock-api] ${req.method} ${pathname} -> ${res.statusCode}`);
		} catch (error) {
			log(`[mock-api] 실패 ${req.method} ${pathname}: ${error.message}`);
			if (!res.headersSent) {
				sendJson(res, 502, { error: 'mock_failed', message: error.message });
			}
		}
	});

	await new Promise((resolveListen) => server.listen(port, '127.0.0.1', resolveListen));
	const { port: boundPort } = server.address();
	log(
		mode === 'record'
			? `[mock-api] 녹화 중 http://localhost:${boundPort} -> ${target}, 저장: ${currentPath}`
			: `[mock-api] 재생 중 http://localhost:${boundPort}, 카세트: ${currentPath} (${current.exchanges.length}건)${linkForLog(current.link)}`
	);
	return server;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
	const [mode, cassette] = process.argv.slice(2);
	if (!mode || !cassette) {
		process.stderr.write(
			'사용법: node scripts/dev/mock-api.mjs <record|replay> <카세트 이름>\n' +
				'환경변수: FLICK_MOCK_PORT (기본 8090), FLICK_MOCK_TARGET (기본 http://localhost:8080),\n' +
				'          FLICK_MOCK_DIR (기본 var/mock-api, 커밋할 테스트 카세트는 tests/fixtures/cassettes)\n'
		);
		process.exit(2);
	}
	await startMockApi({
		mode,
		cassette,
		cassetteDir: process.env.FLICK_MOCK_DIR ? resolve(process.env.FLICK_MOCK_DIR) : undefined,
		port: Number(process.env.FLICK_MOCK_PORT ?? 8090),
		target: process.env.FLICK_MOCK_TARGET ?? 'http://localhost:8080'
	});
}
