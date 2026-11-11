import { describe, expect, it } from 'vitest';
import {
	cancelPipe,
	computePipePassphrase,
	declinePipeConnection,
	decryptPipeStream,
	encryptPipeStream,
	formatPipeCode,
	initPipeRendezvous,
	joinPipe,
	normalizePipeCode,
	PipeRelayError,
	receivePipeStream,
	selectPipeConnection,
	sendPipeStream,
	streamToBytes,
	streamToText,
	waitForPipeSelection,
	waitForPipeUpdate
} from './pipe';

describe('pipe code normalization and formatting', () => {
	it('normalizes valid 6-digit codes with spaces and hyphens', () => {
		expect(normalizePipeCode('123456')).toBe('123456');
		expect(normalizePipeCode(' 123456 ')).toBe('123456');
		expect(normalizePipeCode('123-456')).toBe('123456');
		expect(normalizePipeCode(' 123 - 456 ')).toBe('123456');
		expect(normalizePipeCode('987 654')).toBe('987654');
	});

	it('rejects invalid pipe codes', () => {
		for (const invalid of ['', '12345', '1234567', 'abcdef', '123-45a', '12 34 5', '   ']) {
			expect(() => normalizePipeCode(invalid)).toThrow('Code must be a 6-digit number');
		}
	});

	it('formats 6-digit codes with hyphen separator', () => {
		expect(formatPipeCode('123456')).toBe('123-456');
		expect(formatPipeCode('987-654')).toBe('987-654');
		expect(formatPipeCode(' 555 444 ')).toBe('555-444');
	});
});

describe('computePipePassphrase', () => {
	it('combines normalized code and salt', () => {
		expect(computePipePassphrase('123-456', 'randomsalt123')).toBe('123456:randomsalt123');
		expect(computePipePassphrase(' 987654 ', '  salt_val  ')).toBe('987654:salt_val');
	});

	it('appends optional passphrase when provided', () => {
		expect(computePipePassphrase('123456', 'salt123', 'my-secret')).toBe(
			'123456:salt123:my-secret'
		);
		expect(computePipePassphrase('123456', 'salt123', '  my-secret  ')).toBe(
			'123456:salt123:my-secret'
		);
		expect(computePipePassphrase('123456', 'salt123', '')).toBe('123456:salt123');
		expect(computePipePassphrase('123456', 'salt123', '   ')).toBe('123456:salt123');
	});
});

describe('streaming pipe crypto roundtrip', () => {
	const passphrase = computePipePassphrase('456789', 'test_salt_hex_value');

	it('encrypts and decrypts string input', async () => {
		const plaintext = 'Direct live relay test secret 🚀';
		const encryptedStream = await encryptPipeStream(plaintext, passphrase);
		const decryptedStream = await decryptPipeStream(encryptedStream, passphrase);
		const result = await streamToText(decryptedStream);
		expect(result).toBe(plaintext);
	});

	it('encrypts and decrypts Uint8Array input', async () => {
		const originalBytes = new Uint8Array([0, 1, 2, 3, 254, 255, 42, 99]);
		const encryptedStream = await encryptPipeStream(originalBytes, passphrase);
		const decryptedStream = await decryptPipeStream(encryptedStream, passphrase);
		const resultBytes = await streamToBytes(decryptedStream);
		expect(resultBytes).toEqual(originalBytes);
	});

	it('encrypts and decrypts multi-chunk stream (> 64 KiB)', async () => {
		// 140 KiB spans across 3 age chunks (64 KiB each)
		const length = 140 * 1024;
		const largePayload = new Uint8Array(length);
		for (let i = 0; i < length; i++) {
			largePayload[i] = i % 251;
		}

		const inputStream = new ReadableStream<Uint8Array>({
			start(controller) {
				// Enqueue in 16 KiB chunks
				const chunkSize = 16 * 1024;
				for (let offset = 0; offset < length; offset += chunkSize) {
					controller.enqueue(largePayload.subarray(offset, Math.min(offset + chunkSize, length)));
				}
				controller.close();
			}
		});

		const encryptedStream = await encryptPipeStream(inputStream, passphrase);
		const decryptedStream = await decryptPipeStream(encryptedStream, passphrase);
		const resultBytes = await streamToBytes(decryptedStream);
		expect(resultBytes).toEqual(largePayload);
	});

	it('fails closed when decrypting with wrong passphrase', async () => {
		const plaintext = 'Classified live data';
		const encryptedStream = await encryptPipeStream(plaintext, passphrase);
		const wrongPassphrase = computePipePassphrase('999999', 'test_salt_hex_value');

		await expect(decryptPipeStream(encryptedStream, wrongPassphrase)).rejects.toThrow();
	});

	it('fails closed when ciphertext is corrupted', async () => {
		const plaintext = 'Untampered payload';
		const encryptedStream = await encryptPipeStream(plaintext, passphrase);
		const ciphertextBytes = await streamToBytes(encryptedStream);

		// Corrupt a byte in the encrypted payload body
		ciphertextBytes[ciphertextBytes.length - 1] ^= 0xff;

		const corruptedStream = new ReadableStream<Uint8Array>({
			start(controller) {
				controller.enqueue(ciphertextBytes);
				controller.close();
			}
		});

		const decryptedStream = await decryptPipeStream(corruptedStream, passphrase);
		await expect(streamToText(decryptedStream)).rejects.toThrow();
	});

	it('rejects empty passphrases', async () => {
		await expect(encryptPipeStream('hello', '')).rejects.toThrow('Passphrase cannot be empty');
		const emptyStream = new ReadableStream<Uint8Array>({
			start(controller) {
				controller.close();
			}
		});
		await expect(decryptPipeStream(emptyStream, '')).rejects.toThrow('Passphrase cannot be empty');
	});
});

describe('client API hooks', () => {
	const jsonResponse = (body: unknown, status = 200) =>
		new Response(JSON.stringify(body), {
			status,
			headers: { 'Content-Type': 'application/json' }
		});
	const errorResponse = (status: number, code: string) =>
		jsonResponse({ error: { code, message: 'server text' } }, status);

	it('initPipeRendezvous sends the confirmation choice and maps the sender token', async () => {
		let sentBody = '';
		const mockFetchSuccess: typeof fetch = (_url, init) => {
			sentBody = String(init?.body ?? '');
			return Promise.resolve(
				jsonResponse(
					{
						code: '123456',
						salt: 'salthex',
						expires_in: 180,
						sender_token: 'tok',
						require_confirmation: true
					},
					201
				)
			);
		};

		const res = await initPipeRendezvous('http://localhost:8080/', true, mockFetchSuccess);
		expect(JSON.parse(sentBody)).toEqual({ require_confirmation: true });
		expect(res).toEqual({
			code: '123456',
			salt: 'salthex',
			expiresIn: 180,
			senderToken: 'tok',
			requireConfirmation: true
		});

		const mockFetch429: typeof fetch = async () => errorResponse(429, 'capacity_exceeded');
		await expect(initPipeRendezvous('http://localhost:8080', false, mockFetch429)).rejects.toThrow(
			'maximum capacity'
		);

		const mockFetch503: typeof fetch = async () => new Response('Disabled', { status: 503 });
		await expect(initPipeRendezvous('http://localhost:8080', false, mockFetch503)).rejects.toThrow(
			'currently disabled'
		);
	});

	it('joinPipe returns this connection and maps rejections to reasons', async () => {
		let observedCode = '';
		const mockFetch: typeof fetch = (_url, init) => {
			observedCode = (init?.headers as Record<string, string>)['X-Flick-Pipe-Code'];
			return Promise.resolve(
				jsonResponse(
					{
						connection_id: 'conn-1',
						require_confirmation: true,
						verify_code: '042',
						device: 'Safari on iPhone',
						expires_in: 170
					},
					201
				)
			);
		};
		const joined = await joinPipe('http://localhost:8080', '123-456', mockFetch);
		expect(observedCode).toBe('123456');
		expect(joined).toEqual({
			connectionId: 'conn-1',
			requireConfirmation: true,
			verifyCode: '042',
			device: 'Safari on iPhone',
			expiresIn: 170
		});

		for (const [status, code, message] of [
			[409, 'room_full', 'Too many devices are trying to connect. Ask the sender for a new code.'],
			[403, 'declined', 'The sender declined this device.'],
			[409, 'already_connected', 'Another device is already receiving this transfer.']
		] as const) {
			const rejection = joinPipe('http://localhost:8080', '123456', () =>
				Promise.resolve(errorResponse(status, code))
			);
			await expect(rejection).rejects.toBeInstanceOf(PipeRelayError);
			await expect(rejection).rejects.toMatchObject({ reason: code, message });
		}

		// A proxy error page without a JSON body still maps by status.
		await expect(
			joinPipe('http://localhost:8080', '123456', () =>
				Promise.resolve(new Response('<html>', { status: 404 }))
			)
		).rejects.toMatchObject({ reason: 'not_found', message: 'Invalid or expired 6-digit code.' });
	});

	it('waitForPipeSelection re-polls /status until selected and rejects with the end reason', async () => {
		// Given: 서버가 두 번은 waiting, 세 번째에 selected를 준다
		const answers = ['waiting', 'waiting', 'selected'];
		const seen: { url: string; headers: Record<string, string> }[] = [];
		const mockFetch: typeof fetch = (url, init) => {
			seen.push({ url: String(url), headers: init?.headers as Record<string, string> });
			return Promise.resolve(jsonResponse({ status: answers[seen.length - 1] }));
		};

		await waitForPipeSelection('http://localhost:8080/', '123-456', 'conn-1', mockFetch);

		// Then: 고를 때까지 같은 연결로 /status를 세 번 불렀다
		expect(seen).toHaveLength(3);
		expect(seen[0]).toEqual({
			url: 'http://localhost:8080/api/pipe/status',
			headers: { 'X-Flick-Pipe-Code': '123456', 'X-Flick-Pipe-Connection': 'conn-1' }
		});

		// Then: 거절·미선택은 이유 코드와 함께 끝난다
		for (const [status, code] of [
			[403, 'declined'],
			[410, 'not_selected']
		] as const) {
			await expect(
				waitForPipeSelection('http://localhost:8080', '123456', 'conn-1', () =>
					Promise.resolve(errorResponse(status, code))
				)
			).rejects.toMatchObject({ reason: code });
		}
	});

	it('waitForPipeSelection retries one 5xx, then fails, and names a dropped connection', async () => {
		const sequence = (...responses: (() => Response)[]) => {
			let calls = 0;
			const fetcher: typeof fetch = () => Promise.resolve(responses[calls++]());
			return { fetcher, calls: () => calls };
		};
		const status = (value: string) => () => jsonResponse({ status: value });
		const proxyError = (code: number) => () => new Response('<html>', { status: code });

		// Given: 프록시 524 한 번 뒤 정상 응답 → 실패 없이 선택까지 간다
		const flaky = sequence(proxyError(524), status('waiting'), proxyError(502), status('selected'));
		await waitForPipeSelection('http://localhost:8080', '123456', 'conn-1', flaky.fetcher);
		expect(flaky.calls()).toBe(4);

		// Given: 5xx가 연달아 두 번 → 두 번째에서 실패한다
		const down = sequence(proxyError(502), proxyError(503));
		await expect(
			waitForPipeSelection('http://localhost:8080', '123456', 'conn-1', down.fetcher)
		).rejects.toMatchObject({ status: 503 });
		expect(down.calls()).toBe(2);

		// Given: 참가는 됐는데 /status가 404 → 거절이 아니라 연결이 끊긴 것으로 안내한다
		await expect(
			waitForPipeSelection('http://localhost:8080', '123456', 'conn-1', () =>
				Promise.resolve(errorResponse(404, 'not_found'))
			)
		).rejects.toMatchObject({
			reason: 'dropped',
			message: 'This device was disconnected while waiting. Enter the code again.'
		});
	});

	it('waitForPipeUpdate sends the sender token and since, and maps the waiting list', async () => {
		let observedUrl = '';
		let observedHeaders: Record<string, string> = {};
		const mockFetch: typeof fetch = (url, init) => {
			observedUrl = String(url);
			observedHeaders = init?.headers as Record<string, string>;
			return Promise.resolve(
				jsonResponse({
					status: 'waiting',
					version: 4,
					connections: [
						{
							connection_id: 'c1',
							index: 1,
							verify_code: '150',
							device: 'Chrome on Windows',
							connected_seconds: 7
						}
					]
				})
			);
		};

		const update = await waitForPipeUpdate('http://localhost:8080', '123-456', 'tok', 3, mockFetch);
		expect(observedUrl).toBe('http://localhost:8080/api/pipe/wait?since=3');
		expect(observedHeaders['X-Flick-Pipe-Code']).toBe('123456');
		expect(observedHeaders['X-Flick-Pipe-Sender']).toBe('tok');
		expect(update).toEqual({
			status: 'waiting',
			version: 4,
			connections: [
				{
					connectionId: 'c1',
					index: 1,
					verifyCode: '150',
					device: 'Chrome on Windows',
					connectedSeconds: 7
				}
			]
		});

		await expect(
			waitForPipeUpdate('http://localhost:8080', '123456', 'tok', 0, () =>
				Promise.resolve(errorResponse(410, 'expired'))
			)
		).rejects.toThrow('This code expired.');
	});

	it('select, decline, and cancel carry the sender token and connection', async () => {
		const calls: { url: string; headers: Record<string, string> }[] = [];
		const mockFetch: typeof fetch = (url, init) => {
			calls.push({ url: String(url), headers: init?.headers as Record<string, string> });
			return Promise.resolve(jsonResponse({ status: 'ok' }));
		};

		await selectPipeConnection('http://api', '123456', 'tok', 'c1', mockFetch);
		await declinePipeConnection('http://api', '123456', 'tok', 'c2', mockFetch);
		await cancelPipe('http://api', '123456', 'tok', mockFetch);

		expect(calls.map((call) => call.url)).toEqual([
			'http://api/api/pipe/select',
			'http://api/api/pipe/decline',
			'http://api/api/pipe/cancel'
		]);
		expect(calls.map((call) => call.headers['X-Flick-Pipe-Connection'])).toEqual([
			'c1',
			'c2',
			undefined
		]);
		expect(calls.every((call) => call.headers['X-Flick-Pipe-Sender'] === 'tok')).toBe(true);
	});

	it('sendPipeStream passes headers, body, duplex and returns transferred bytes', async () => {
		let capturedMethod = '';
		let capturedHeaders: Record<string, string> = {};
		let capturedDuplex = '';
		let capturedBody: unknown = null;

		const mockFetch: typeof fetch = (_url, init) => {
			capturedMethod = init?.method ?? '';
			capturedHeaders = (init?.headers as Record<string, string>) ?? {};
			// @ts-expect-error test inspection
			capturedDuplex = init?.duplex ?? '';
			capturedBody = init?.body ?? null;
			return Promise.resolve(
				new Response(JSON.stringify({ status: 'completed', bytes_transferred: 1024 }), {
					status: 200
				})
			);
		};

		// A request body stream can be consumed only once, so each call gets a
		// fresh one.
		const makeStream = () =>
			new ReadableStream<Uint8Array>({
				start(c) {
					c.close();
				}
			});

		const bytes = await sendPipeStream({
			baseUrl: 'http://localhost:8080',
			code: '987-654',
			senderToken: 'tok',
			stream: makeStream(),
			kind: 'file',
			encryptedFilename: 'enc_filename_token',
			fetcher: mockFetch
		});

		expect(bytes).toBe(1024);
		expect(capturedMethod).toBe('POST');
		expect(capturedHeaders['X-Flick-Pipe-Code']).toBe('987654');
		expect(capturedHeaders['X-Flick-Pipe-Sender']).toBe('tok');
		expect(capturedHeaders['Flick-Kind']).toBe('file');
		expect(capturedHeaders['Flick-Encrypted-Filename']).toBe('enc_filename_token');
		// Plaintext HTTP/1.1 targets get a buffered Blob body: Chromium refuses
		// streaming uploads without HTTP/2.
		expect(capturedDuplex).toBe('');
		expect(capturedBody).toBeInstanceOf(Blob);

		await sendPipeStream({
			baseUrl: 'https://flick.example',
			code: '987-654',
			senderToken: 'tok',
			stream: makeStream(),
			fetcher: mockFetch
		});
		expect(capturedDuplex).toBe('half');

		const mockFetch502: typeof fetch = () =>
			Promise.resolve(new Response('Disconnected', { status: 502 }));
		await expect(
			sendPipeStream({
				baseUrl: 'http://localhost:8080',
				code: '987654',
				senderToken: 'tok',
				stream: makeStream(),
				fetcher: mockFetch502
			})
		).rejects.toThrow('Receiver disconnected during live transfer.');
	});

	it('receivePipeStream retrieves headers and response stream', async () => {
		const testBytes = new TextEncoder().encode('streamed stream');
		let observedConnection = '';
		const mockFetch: typeof fetch = (_url, init) => {
			observedConnection = (init?.headers as Record<string, string>)['X-Flick-Pipe-Connection'];
			return Promise.resolve(
				new Response(
					new ReadableStream({
						start(controller) {
							controller.enqueue(testBytes);
							controller.close();
						}
					}),
					{
						status: 200,
						headers: {
							'X-Flick-Pipe-Salt': 'mock_salt_value',
							'Flick-Kind': 'text',
							'Flick-Encrypted-Filename': 'none'
						}
					}
				)
			);
		};

		const res = await receivePipeStream('http://localhost:8080', '123-456', 'conn-1', mockFetch);
		expect(observedConnection).toBe('conn-1');
		expect(res.salt).toBe('mock_salt_value');
		expect(res.kind).toBe('text');
		expect(res.encryptedFilename).toBe('none');

		const receivedText = await streamToText(res.stream);
		expect(receivedText).toBe('streamed stream');

		await expect(
			receivePipeStream('http://localhost:8080', '123456', 'conn-1', () =>
				Promise.resolve(errorResponse(410, 'not_selected'))
			)
		).rejects.toMatchObject({
			reason: 'not_selected',
			message: 'The sender chose another device.'
		});
	});
});
