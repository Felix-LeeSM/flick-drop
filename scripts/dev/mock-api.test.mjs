// biome-ignore-all lint/style/useNamingConvention: HTTP 헤더 이름은 전송 형식 그대로 쓴다.
// node --test scripts/dev/mock-api.test.mjs
import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { after, test } from 'node:test';
import { startMockApi } from './mock-api.mjs';

const cassetteDir = mkdtempSync(join(tmpdir(), 'flick-mock-api-'));
const servers = [];
after(() => {
	for (const server of servers) {
		server.close();
	}
	rmSync(cassetteDir, { recursive: true, force: true });
});

function baseUrl(server) {
	return `http://127.0.0.1:${server.address().port}`;
}

// 한 번 열면 끝나는 API 를 흉내 내는 upstream: 첫 open 은 200, 그 뒤는 410.
async function startOneTimeUpstream() {
	let opened = false;
	const server = createServer((req, res) => {
		if (req.method === 'POST' && req.url === '/api/secrets/s1/open') {
			if (opened) {
				res.writeHead(410, { 'Content-Type': 'application/json' });
				res.end('{"error":"gone"}');
				return;
			}
			opened = true;
			res.writeHead(200, { 'Content-Type': 'application/octet-stream', 'Flick-Kind': 'text' });
			res.end(Buffer.from([0, 1, 2, 255]));
			return;
		}
		res.writeHead(404);
		res.end();
	});
	await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
	servers.push(server);
	return server;
}

const silent = () => undefined;

test('녹화한 한 번 열기 순서를 API 없이 그대로 재생한다', async () => {
	// Given: 실제 API 를 거쳐 open 을 두 번 녹화한다
	const upstream = await startOneTimeUpstream();
	const recorder = await startMockApi({
		mode: 'record',
		cassette: 'open-once',
		cassetteDir,
		target: baseUrl(upstream),
		port: 0,
		log: silent
	});
	servers.push(recorder);
	const cassetteFile = join(cassetteDir, 'open-once.json');
	await fetch(`${baseUrl(recorder)}/api/secrets/s1/open`, { method: 'POST', body: '{}' });
	// 녹화 도중 사람이 카세트에 수신자 링크를 적어 넣는다
	const written = JSON.parse(readFileSync(cassetteFile, 'utf8'));
	writeFileSync(
		cassetteFile,
		JSON.stringify({ ...written, link: 'http://localhost:5173/s/s1#key=x' })
	);
	await fetch(`${baseUrl(recorder)}/api/secrets/s1/open`, { method: 'POST', body: '{}' });
	upstream.close();
	assert.equal(
		JSON.parse(readFileSync(cassetteFile, 'utf8')).link,
		'http://localhost:5173/s/s1#key=x'
	);

	// When: upstream 없이 재생 서버로 open 을 세 번 보낸다
	const player = await startMockApi({
		mode: 'replay',
		cassette: 'open-once',
		cassetteDir,
		port: 0,
		log: silent
	});
	servers.push(player);
	const open = () =>
		fetch(`${baseUrl(player)}/api/secrets/s1/open?ignored=1`, {
			method: 'POST',
			headers: { Origin: 'http://localhost:5173' }
		});
	const first = await open();
	const second = await open();
	const third = await open();

	// Then: 녹화 순서대로 200(바이너리 본문·Flick 헤더 유지) → 410, 이후 마지막 응답 반복
	assert.equal(first.status, 200);
	assert.deepEqual([...new Uint8Array(await first.arrayBuffer())], [0, 1, 2, 255]);
	assert.equal(first.headers.get('flick-kind'), 'text');
	assert.equal(first.headers.get('access-control-allow-origin'), 'http://localhost:5173');
	assert.equal(second.status, 410);
	assert.equal(third.status, 410);

	// reset 하면 다시 첫 응답부터 돌려준다
	await fetch(`${baseUrl(player)}/__mock/reset`, { method: 'POST' });
	assert.equal((await open()).status, 200);

	// 녹화에 없는 요청은 501 로 드러낸다
	const missing = await fetch(`${baseUrl(player)}/api/config`);
	assert.equal(missing.status, 501);
});

test('하루 전에 녹화한 만료 시각을 재생 시점 기준으로 옮긴다', async () => {
	// Given: 하루 전에 녹화했고 녹화 시점 1시간 뒤에 만료되는 응답
	const hour = 60 * 60 * 1000;
	const recordedAt = Date.now() - 24 * hour;
	const recordedExpiry = new Date(recordedAt + hour).toISOString();
	writeFileSync(
		join(cassetteDir, 'stale.json'),
		JSON.stringify({
			recordedAt: new Date(recordedAt).toISOString(),
			link: '',
			exchanges: [
				{
					method: 'GET',
					path: '/api/secrets/s1/management',
					status: 200,
					headers: { 'content-type': 'application/json', 'flick-expires-at': recordedExpiry },
					body: JSON.stringify({ id: 's1', expires_at: recordedExpiry, state: 'active' }),
					bodyEncoding: 'utf8'
				}
			]
		})
	);
	const player = await startMockApi({
		mode: 'replay',
		cassette: 'stale',
		cassetteDir,
		port: 0,
		log: silent
	});
	servers.push(player);

	// When: 지금 재생한다
	const response = await fetch(`${baseUrl(player)}/api/secrets/s1/management`);
	const body = await response.json();

	// Then: 만료는 지금부터 약 1시간 뒤이고, 시각이 아닌 값은 그대로다
	const remaining = Date.parse(body.expires_at) - Date.now();
	assert.ok(Math.abs(remaining - hour) < 5000, `남은 시간 ${remaining}ms`);
	assert.equal(response.headers.get('flick-expires-at'), body.expires_at);
	assert.deepEqual({ id: body.id, state: body.state }, { id: 's1', state: 'active' });
});

test('presigned 업로드 주소를 mock 경유로 바꿔 스토리지 PUT 까지 녹화·재생한다', async () => {
	// Given: API 는 스토리지 주소를 돌려주고, 스토리지는 PUT 을 받는다
	const puts = [];
	const storage = createServer(async (req, res) => {
		const chunks = [];
		for await (const chunk of req) {
			chunks.push(chunk);
		}
		puts.push({ url: req.url, length: Buffer.concat(chunks).length });
		res.writeHead(200, { ETag: '"synthetic"' });
		res.end();
	});
	await new Promise((resolve) => storage.listen(0, '127.0.0.1', resolve));
	servers.push(storage);
	const signedUrl = `${baseUrl(storage)}/bucket/object?X-Amz-Signature=abc`;
	const api = createServer((_req, res) => {
		res.writeHead(201, { 'Content-Type': 'application/json' });
		res.end(JSON.stringify({ id: 's2', upload: { url: signedUrl, method: 'PUT' } }));
	});
	await new Promise((resolve) => api.listen(0, '127.0.0.1', resolve));
	servers.push(api);
	const recorder = await startMockApi({
		mode: 'record',
		cassette: 'upload',
		cassetteDir,
		target: baseUrl(api),
		port: 0,
		log: silent
	});
	servers.push(recorder);

	// When: 녹화 중 생성 응답의 주소로 PUT 하고, 둘 다 끈 뒤 재생한다
	const created = await (
		await fetch(`${baseUrl(recorder)}/api/secrets`, { method: 'POST', body: '{}' })
	).json();
	const recordedPut = await fetch(created.upload.url, { method: 'PUT', body: 'x'.repeat(10) });
	storage.close();
	api.close();
	const player = await startMockApi({
		mode: 'replay',
		cassette: 'upload',
		cassetteDir,
		port: 0,
		log: silent
	});
	servers.push(player);
	const replayed = await (
		await fetch(`${baseUrl(player)}/api/secrets`, { method: 'POST', body: '{}' })
	).json();
	const replayedPath = new URL(replayed.upload.url).pathname;
	const replayedPut = await fetch(`${baseUrl(player)}${replayedPath}`, {
		method: 'PUT',
		body: 'x'
	});

	// Then: 녹화 때는 원래 스토리지가 원래 주소로 본문을 받았고, 재생 때도 같은 응답이 온다
	assert.deepEqual(puts, [{ url: '/bucket/object?X-Amz-Signature=abc', length: 10 }]);
	assert.equal(new URL(created.upload.url).origin, baseUrl(recorder));
	assert.equal(recordedPut.status, 200);
	assert.equal(replayedPut.status, 200);
	assert.equal(replayedPut.headers.get('etag'), '"synthetic"');
});

test('코드로 받기 재생은 받는 브라우저의 새 recipient 로 평문을 다시 암호화한다', async () => {
	// Given: 녹화 당시 다른 키로 암호화된 receive 응답과, 사람이 적어 둔 합성 평문
	const { Decrypter, generateHybridIdentity, identityToRecipient } = await import(
		'../../web/node_modules/age-encryption/dist/index.js'
	);
	writeFileSync(
		join(cassetteDir, 'pipe.json'),
		JSON.stringify({
			recordedAt: new Date().toISOString(),
			link: '',
			pipe: { text: 'synthetic pipe text', filename: 'synthetic.txt' },
			exchanges: [
				{
					method: 'POST',
					path: '/api/pipe/join',
					status: 200,
					headers: { 'content-type': 'application/json' },
					body: '{"connection_id":"c1"}',
					bodyEncoding: 'utf8'
				},
				{
					method: 'GET',
					path: '/api/pipe/receive',
					status: 200,
					headers: {
						'content-type': 'application/octet-stream',
						'flick-kind': 'file',
						'flick-encrypted-filename': 'b2xk'
					},
					body: Buffer.from('old ciphertext').toString('base64'),
					bodyEncoding: 'base64'
				}
			]
		})
	);
	const player = await startMockApi({
		mode: 'replay',
		cassette: 'pipe',
		cassetteDir,
		port: 0,
		log: silent
	});
	servers.push(player);
	const identity = await generateHybridIdentity();

	// When: 새 키의 recipient 로 join 한 뒤 receive 한다
	await fetch(`${baseUrl(player)}/api/pipe/join`, {
		method: 'POST',
		body: JSON.stringify({ recipient: await identityToRecipient(identity) })
	});
	const received = await fetch(`${baseUrl(player)}/api/pipe/receive`);

	// Then: 그 키로 본문과 파일 이름이 풀린다
	const decrypter = new Decrypter();
	decrypter.addIdentity(identity);
	const text = await decrypter.decrypt(new Uint8Array(await received.arrayBuffer()), 'text');
	const filename = await decrypter.decrypt(
		Buffer.from(received.headers.get('flick-encrypted-filename'), 'base64'),
		'text'
	);
	assert.deepEqual([text, filename], ['synthetic pipe text', 'synthetic.txt']);
	assert.equal(received.headers.get('flick-kind'), 'file');
});

test('long-poll 응답은 녹화에서 앞선 상대 기기의 요청이 재생될 때까지 기다린다', async () => {
	// Given: 받는 쪽 join 다음에 보내는 쪽 wait 가 "접속됨"으로 끝난 녹화
	const exchange = (method, path, body) => ({
		method,
		path,
		status: 200,
		headers: { 'content-type': 'application/json' },
		body,
		bodyEncoding: 'utf8'
	});
	writeFileSync(
		join(cassetteDir, 'pipe-order.json'),
		JSON.stringify({
			recordedAt: new Date().toISOString(),
			link: '',
			exchanges: [
				exchange('POST', '/api/pipe/join', '{}'),
				exchange('GET', '/api/pipe/wait', '{"status":"ready"}')
			]
		})
	);
	const player = await startMockApi({
		mode: 'replay',
		cassette: 'pipe-order',
		cassetteDir,
		port: 0,
		log: silent
	});
	servers.push(player);

	// When: 보내는 쪽이 먼저 wait 한다
	let waitDone = false;
	const wait = fetch(`${baseUrl(player)}/api/pipe/wait`).then((response) => {
		waitDone = true;
		return response.json();
	});
	await new Promise((resolve) => setTimeout(resolve, 200));
	const doneBeforeJoin = waitDone;
	await fetch(`${baseUrl(player)}/api/pipe/join`, {
		method: 'POST',
		body: '{"recipient":"age1synthetic"}'
	});

	// Then: join 전에는 응답하지 않고, join 뒤에 "접속됨"을 돌려준다
	assert.equal(doneBeforeJoin, false);
	assert.deepEqual(await wait, { status: 'ready' });
});

test('재생 로그에는 링크의 복호화 키가 남지 않고, 날짜가 아닌 *_at 값과 헤더 대소문자를 견딘다', async () => {
	// Given: 키가 든 링크와, 손으로 쓴 대문자 헤더·날짜가 아닌 *_at 값을 가진 카세트
	const recordedAt = new Date(Date.now() - 60_000).toISOString();
	writeFileSync(
		join(cassetteDir, 'edge.json'),
		JSON.stringify({
			recordedAt,
			link: 'http://localhost:5173/s/s3#key=AGE-SECRET-KEY-PQ-1SYNTHETIC',
			exchanges: [
				{
					method: 'GET',
					path: '/api/secrets/s3',
					status: 200,
					headers: { 'Content-Type': 'application/json', 'Flick-Expires-At': 'never' },
					body: JSON.stringify({ format_at: '1', expires_at: recordedAt }),
					bodyEncoding: 'utf8'
				}
			]
		})
	);
	const lines = [];

	// When: 재생 서버를 띄우고 응답을 받는다
	const player = await startMockApi({
		mode: 'replay',
		cassette: 'edge',
		cassetteDir,
		port: 0,
		log: (line) => lines.push(line)
	});
	servers.push(player);
	const response = await fetch(`${baseUrl(player)}/api/secrets/s3`);
	const body = await response.json();

	// Then: 로그에는 경로만 있고, 날짜가 아닌 값은 그대로이며, 시각은 1분 옮겨진다
	assert.equal(lines.join('\n').includes('AGE-SECRET-KEY'), false);
	assert.ok(lines.join('\n').includes('/s/s3'));
	assert.equal(response.status, 200);
	assert.equal(response.headers.get('flick-expires-at'), 'never');
	assert.equal(body.format_at, '1');
	assert.ok(Math.abs(Date.parse(body.expires_at) - Date.now()) < 5000);
});
