import { describe, expect, it } from 'vitest';
import {
	computePipePassphrase,
	decryptPipeStream,
	encryptPipeStream,
	formatPipeCode,
	initPipeRendezvous,
	normalizePipeCode,
	receivePipeStream,
	sendPipeStream,
	streamToBytes,
	streamToText,
	waitForReceiver
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
	it('initPipeRendezvous handles success and errors', async () => {
		const mockFetchSuccess: typeof fetch = async () =>
			new Response(JSON.stringify({ code: '123456', salt: 'salthex', expires_in: 180 }), {
				status: 200,
				headers: { 'Content-Type': 'application/json' }
			});

		const res = await initPipeRendezvous('http://localhost:8080/', mockFetchSuccess);
		expect(res).toEqual({
			code: '123456',
			salt: 'salthex',
			expiresIn: 180
		});

		const mockFetch429: typeof fetch = async () =>
			new Response('Too many sessions', { status: 429 });
		await expect(initPipeRendezvous('http://localhost:8080', mockFetch429)).rejects.toThrow(
			'maximum capacity'
		);

		const mockFetch503: typeof fetch = async () => new Response('Disabled', { status: 503 });
		await expect(initPipeRendezvous('http://localhost:8080', mockFetch503)).rejects.toThrow(
			'currently disabled'
		);
	});

	it('waitForReceiver handles 200, 404, 410, and generic errors', async () => {
		let observedHeader = '';
		const mockFetchSuccess: typeof fetch = (_url, init) => {
			observedHeader = (init?.headers as Record<string, string>)?.['X-Flick-Pipe-Code'] ?? '';
			return Promise.resolve(
				new Response(JSON.stringify({ status: 'connected' }), { status: 200 })
			);
		};

		await waitForReceiver('http://localhost:8080', '123-456', mockFetchSuccess);
		expect(observedHeader).toBe('123456');

		const mockFetch404: typeof fetch = () =>
			Promise.resolve(new Response('Not found', { status: 404 }));
		await expect(waitForReceiver('http://localhost:8080', '123456', mockFetch404)).rejects.toThrow(
			'Transfer code not found or expired.'
		);

		const mockFetch410: typeof fetch = () =>
			Promise.resolve(new Response('Burned', { status: 410 }));
		await expect(waitForReceiver('http://localhost:8080', '123456', mockFetch410)).rejects.toThrow(
			'Transfer session expired or burned.'
		);
	});

	it('sendPipeStream passes headers, body, duplex and returns transferred bytes', async () => {
		let capturedMethod = '';
		let capturedHeaders: Record<string, string> = {};
		let capturedDuplex = '';

		const mockFetch: typeof fetch = (_url, init) => {
			capturedMethod = init?.method ?? '';
			capturedHeaders = (init?.headers as Record<string, string>) ?? {};
			// @ts-expect-error test inspection
			capturedDuplex = init?.duplex ?? '';
			return Promise.resolve(
				new Response(JSON.stringify({ status: 'completed', bytes_transferred: 1024 }), {
					status: 200
				})
			);
		};

		const dummyStream = new ReadableStream<Uint8Array>({
			start(c) {
				c.close();
			}
		});

		const bytes = await sendPipeStream({
			baseUrl: 'http://localhost:8080',
			code: '987-654',
			stream: dummyStream,
			kind: 'file',
			encryptedFilename: 'enc_filename_token',
			fetcher: mockFetch
		});

		expect(bytes).toBe(1024);
		expect(capturedMethod).toBe('POST');
		expect(capturedHeaders['X-Flick-Pipe-Code']).toBe('987654');
		expect(capturedHeaders['Flick-Kind']).toBe('file');
		expect(capturedHeaders['Flick-Encrypted-Filename']).toBe('enc_filename_token');
		expect(capturedDuplex).toBe('half');

		const mockFetch502: typeof fetch = () =>
			Promise.resolve(new Response('Disconnected', { status: 502 }));
		await expect(
			sendPipeStream({
				baseUrl: 'http://localhost:8080',
				code: '987654',
				stream: dummyStream,
				fetcher: mockFetch502
			})
		).rejects.toThrow('Receiver disconnected during live transfer.');
	});

	it('receivePipeStream retrieves headers and response stream', async () => {
		const testBytes = new TextEncoder().encode('streamed stream');
		const mockFetch: typeof fetch = () =>
			Promise.resolve(
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

		const res = await receivePipeStream('http://localhost:8080', '123-456', mockFetch);
		expect(res.salt).toBe('mock_salt_value');
		expect(res.kind).toBe('text');
		expect(res.encryptedFilename).toBe('none');

		const receivedText = await streamToText(res.stream);
		expect(receivedText).toBe('streamed stream');

		const mockFetch409: typeof fetch = () =>
			Promise.resolve(new Response('Conflict', { status: 409 }));
		await expect(
			receivePipeStream('http://localhost:8080', '123456', mockFetch409)
		).rejects.toThrow('Another device is already receiving this transfer.');

		const mockFetch410: typeof fetch = () =>
			Promise.resolve(new Response('Burned', { status: 410 }));
		await expect(
			receivePipeStream('http://localhost:8080', '123456', mockFetch410)
		).rejects.toThrow('Transfer code burned due to too many failed attempts.');
	});
});
