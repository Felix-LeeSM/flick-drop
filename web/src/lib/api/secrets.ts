import type { AccessVerifierPayload, KdfParams } from '#lib/crypto/access.js';
import { base64ToBytes, bytesToBase64 } from '#lib/crypto/encoding.js';
import { encodeKeyFragment } from '#lib/crypto/fragment.js';
import type { SealedFile } from '#lib/crypto/secret.js';
import { type ClientLimits, defaultLimits } from './config';

export const DEFAULT_API_BASE_URL =
	import.meta.env.PUBLIC_FLICK_API_BASE_URL || 'http://localhost:8080';

export type TtlSeconds = number;

export type FileUploadProgress = {
	stage: 'preparing' | 'uploading' | 'finalizing';
	loaded?: number;
	total?: number;
};

// PresignedUpload mirrors the server's presignedUploadResponse: a signed request
// the browser sends the raw ciphertext to, so the server never sees the bytes.
// Content-Length is inside the signature, so a body of any other length is
// rejected by the bucket before the object lands.
export type PresignedUpload = {
	url: string;
	method: string;
	expires_at: string;
	headers: Record<string, string>;
};

export type CreateSecretResponse = {
	id: string;
	expires_at: string;
	management_token?: string;
	management_expires_at?: string;
	// Present only for large secrets (request omitted ciphertext). The client
	// sends the raw ciphertext to `url`, then calls /finalize. Defined here so
	// the large path can read it. The result preserves management authority
	// only after upload + finalize are completed inside.
	upload?: PresignedUpload;
};

export type SecretKind = 'text' | 'file';

// Model A: the PBKDF2 access proof plus the identity wrapped by age scrypt.
// Model B sends neither; its identity is in the share URL fragment.
export type PassphraseEnvelope = {
	access: AccessVerifierPayload;
	wrappedIdentity: Uint8Array;
};

// The open response: the raw age body as a stream, and the metadata the API
// sends in Flick-* headers (contracts/openapi.yaml openSecret).
export type OpenedSecret = {
	kind: SecretKind;
	expiresAt: string;
	contentType: string;
	encryptedFilename?: Uint8Array;
	wrappedIdentity?: Uint8Array;
	body: ReadableStream<Uint8Array>;
};

export type GetSecretMetadataResponse = {
	id: string;
	kind: SecretKind;
	// access is present for Model A (browser derives the proof from it) and
	// absent for Model B (browser opens with the URL fragment key instead).
	access?: {
		kdf: KdfParams;
	};
	size_bytes: number;
	expires_at: string;
};

export type SecretApiClient = {
	createTextSecret(
		body: Uint8Array,
		ttlSeconds: TtlSeconds,
		modelA?: PassphraseEnvelope
	): Promise<CreateSecretResponse>;
	createFileSecret(
		sealed: SealedFile,
		ttlSeconds: TtlSeconds,
		modelA?: PassphraseEnvelope,
		// Cancels a large-file attempt; no later upload/finalize or result may follow.
		signal?: AbortSignal,
		onProgress?: (progress: FileUploadProgress) => void
	): Promise<CreateSecretResponse>;
	getSecretMetadata(id: string): Promise<GetSecretMetadataResponse>;
	openSecret(id: string, accessProof?: string): Promise<OpenedSecret>;
};

export class SecretApiError extends Error {
	readonly code: string;
	readonly status: number;

	constructor(message: string, code: string, status: number) {
		super(message);
		this.name = 'SecretApiError';
		this.code = code;
		this.status = status;
	}
}

type ClientOptions = {
	baseUrl?: string;
	fetcher?: typeof fetch;
	// Client-facing size limits (fetched from /api/config). Drive file routing:
	// inline path at or below payloadInlineMaxBytes, S3 path above it, rejected
	// above maxFileBytes. Defaults to the built-in limits when unset.
	limits?: ClientLimits;
};

export function createSecretApiClient(options: ClientOptions = {}): SecretApiClient {
	const baseUrl = normalizeBaseUrl(options.baseUrl ?? DEFAULT_API_BASE_URL);
	const fetcher = options.fetcher ?? fetch;
	const limits = options.limits ?? defaultLimits();

	return {
		createTextSecret(body, ttlSeconds, modelA) {
			const request = withModelA(
				{
					kind: 'text',
					ciphertext: bytesToBase64(body),
					size_bytes: body.byteLength,
					ttl_seconds: ttlSeconds,
					max_views: 1
				},
				modelA
			);
			return requestJson<CreateSecretResponse>(fetcher, `${baseUrl}/api/secrets`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify(request)
			});
		},

		async createFileSecret(sealed, ttlSeconds, modelA, signal, onProgress) {
			// Limits from /api/config count bytes before encryption; the server's
			// ciphertext caps already include the age overhead.
			if (sealed.plaintextBytes > limits.maxFileBytes) {
				// Reject before any network call — the server would refuse it too.
				throw new SecretApiError('This file is too large.', 'payload_too_large', 0);
			}
			const request = withModelA(
				{
					kind: 'file',
					encrypted_filename: bytesToBase64(sealed.encryptedFilename),
					content_type: sealed.contentType,
					size_bytes: sealed.body.size,
					ttl_seconds: ttlSeconds,
					max_views: 1
				},
				modelA
			);
			if (sealed.plaintextBytes <= limits.payloadInlineMaxBytes) {
				request.ciphertext = bytesToBase64(new Uint8Array(await sealed.body.arrayBuffer()));
				return requestJson<CreateSecretResponse>(fetcher, `${baseUrl}/api/secrets`, {
					method: 'POST',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify(request)
				});
			}
			return createLargeFileSecret(fetcher, baseUrl, request, sealed.body, signal, onProgress);
		},

		getSecretMetadata(id) {
			return requestJson<GetSecretMetadataResponse>(
				fetcher,
				`${baseUrl}/api/secrets/${encodeURIComponent(id)}`
			);
		},

		async openSecret(id, accessProof) {
			// Model A sends an access proof; Model B omits it (link is the
			// capability). An empty body still satisfies the required request body.
			const response = await send(
				fetcher,
				`${baseUrl}/api/secrets/${encodeURIComponent(id)}/open`,
				{
					method: 'POST',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify(accessProof ? { access_proof: accessProof } : {})
				}
			);
			return openedSecret(response);
		}
	};
}

// createLargeFileSecret uploads the ciphertext straight to the object store:
//   1. POST /api/secrets WITHOUT ciphertext → server returns a presigned upload.
//   2. Send the raw ciphertext as the body of that signed request.
//   3. POST /api/secrets/{id}/finalize so the server HEAD-checks the object and
//      activates the secret.
// Preserves the creation capability after successful finalize, without upload credentials.
async function createLargeFileSecret(
	fetcher: typeof fetch,
	baseUrl: string,
	// ciphertext intentionally absent — that's what selects the large path.
	request: Record<string, unknown>,
	body: Blob,
	signal?: AbortSignal,
	onProgress?: (progress: FileUploadProgress) => void
): Promise<CreateSecretResponse> {
	throwIfUploadAborted(signal);
	onProgress?.({ stage: 'preparing' });

	const staged = await requestJson<CreateSecretResponse>(fetcher, `${baseUrl}/api/secrets`, {
		signal,
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify(request)
	});
	if (!staged.upload) {
		// The server only returns upload when ciphertext was omitted; reaching
		// here without it means a contract mismatch (or S3 not enabled).
		throw new SecretApiError('Could not start the large upload. Try again.', 'upload_failed', 0);
	}

	await uploadToObjectStore(fetcher, staged.upload, body, signal, onProgress);
	throwIfUploadAborted(signal);
	onProgress?.({ stage: 'finalizing' });

	await requestJson<{ id: string; finalized: boolean }>(
		fetcher,
		`${baseUrl}/api/secrets/${encodeURIComponent(staged.id)}/finalize`,
		{
			signal,
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: '{}'
		}
	);

	return {
		id: staged.id,
		expires_at: staged.expires_at,
		...(staged.management_token
			? {
					management_token: staged.management_token,
					management_expires_at: staged.management_expires_at
				}
			: {})
	};
}

// uploadToObjectStore sends the raw ciphertext as the request body.
// Content-Length is signed and is not set by hand — the browser forbids it as a
// header and derives it from the body, which is exactly the signed value.
export async function uploadToObjectStore(
	fetcher: typeof fetch,
	upload: PresignedUpload,
	body: Blob,
	signal?: AbortSignal,
	onProgress?: (progress: FileUploadProgress) => void
): Promise<void> {
	throwIfUploadAborted(signal);
	// Content-Length is a forbidden header name, so it cannot be set here — the
	// browser derives it from the body. Compare against the signed value anyway:
	// a mismatch means the encryption overhead assumption drifted from the
	// server's, and failing here names the cause instead of leaving a bare 403
	// from the bucket.
	const signedLength = Number(
		Object.entries(upload.headers).find(([name]) => name.toLowerCase() === 'content-length')?.[1]
	);
	if (Number.isFinite(signedLength) && signedLength !== body.size) {
		throw new SecretApiError('Upload size mismatch. Try again.', 'upload_failed', 0);
	}
	// Echo every signed header except Content-Length, which is a forbidden
	// header name — the browser refuses to let us set it and derives it from the
	// body instead. Any other header the server signs (a checksum, a content
	// type) has to travel, or the signature stops matching.
	const headers = Object.fromEntries(
		Object.entries(upload.headers).filter(([name]) => name.toLowerCase() !== 'content-length')
	);

	onProgress?.({ stage: 'uploading' });
	throwIfUploadAborted(signal);
	if (typeof XMLHttpRequest !== 'undefined') {
		return new Promise<void>((resolve, reject) => {
			const xhr = new XMLHttpRequest();
			let settled = false;
			const finish = (error?: SecretApiError) => {
				if (settled) {
					return;
				}
				settled = true;
				signal?.removeEventListener('abort', abort);
				xhr.upload.onprogress = null;
				xhr.onload = xhr.onerror = xhr.onabort = xhr.ontimeout = null;
				if (error) {
					reject(error);
				} else {
					resolve();
				}
			};
			const abort = () => {
				finish(new SecretApiError('Upload cancelled.', 'upload_cancelled', 0));
				xhr.abort();
			};
			xhr.upload.onprogress = (event) => {
				if (!settled && !signal?.aborted) {
					onProgress?.({
						stage: 'uploading',
						loaded: event.loaded,
						total: event.lengthComputable && event.total > 0 ? event.total : undefined
					});
				}
			};
			xhr.onload = () => {
				if (signal?.aborted) {
					abort();
				} else if (xhr.status >= 200 && xhr.status < 300) {
					finish();
				} else {
					finish(new SecretApiError('Upload failed. Try again.', 'upload_failed', xhr.status));
				}
			};
			xhr.onerror = xhr.ontimeout = () =>
				finish(
					new SecretApiError(
						'Could not reach the upload endpoint. Check your connection and try again.',
						'network_error',
						0
					)
				);
			xhr.onabort = abort;
			try {
				xhr.open(upload.method, upload.url);
				for (const [name, value] of Object.entries(headers)) {
					xhr.setRequestHeader(name, value);
				}
				signal?.addEventListener('abort', abort, { once: true });
				xhr.send(body);
			} catch {
				finish(new SecretApiError('Could not start the upload. Try again.', 'upload_failed', 0));
			}
		});
	}

	// Non-browser clients retain fetch with indeterminate progress.
	let response: Response;
	try {
		response = await fetcher(upload.url, { method: upload.method, headers, body, signal });
	} catch (error) {
		// A user-triggered abort is not a failure — surface it distinctly so the
		// caller routes it to the idle path instead of an "Upload failed" error.
		if (error instanceof DOMException && error.name === 'AbortError') {
			throw new SecretApiError('Upload cancelled.', 'upload_cancelled', 0);
		}
		throw new SecretApiError(
			'Could not reach the upload endpoint. Check your connection and try again.',
			'network_error',
			0
		);
	}
	if (!response.ok) {
		throw new SecretApiError('Upload failed. Try again.', 'upload_failed', response.status);
	}
}

function throwIfUploadAborted(signal?: AbortSignal | null): void {
	if (signal?.aborted) {
		throw new SecretApiError('Upload cancelled.', 'upload_cancelled', 0);
	}
}

export function createShareUrl(origin: string, id: string, identity?: string): string {
	const url = new URL(origin);
	url.pathname = `/s/${encodeURIComponent(id)}`;
	url.search = '';
	// Model B carries the decryption key in the fragment, which the browser
	// never sends to the server. See web/src/lib/crypto/fragment.ts.
	url.hash = identity ? encodeKeyFragment(identity) : '';
	return url.toString();
}

async function requestJson<T>(
	fetcher: typeof fetch,
	input: RequestInfo | URL,
	init?: RequestInit
): Promise<T> {
	const response = await send(fetcher, input, init);
	const result = (await response.json()) as T;
	throwIfUploadAborted(init?.signal);
	return result;
}

// send performs the request and turns a non-2xx answer into a SecretApiError.
async function send(
	fetcher: typeof fetch,
	input: RequestInfo | URL,
	init?: RequestInit
): Promise<Response> {
	throwIfUploadAborted(init?.signal);
	let response: Response;
	try {
		response = await fetcher(input, init);
	} catch {
		throwIfUploadAborted(init?.signal);
		throw new SecretApiError(
			'Could not reach Flick. Check your connection and try again.',
			'network_error',
			0
		);
	}

	throwIfUploadAborted(init?.signal);
	if (!response.ok) {
		const serverError = await readServerError(response);
		throw new SecretApiError(
			clientErrorMessage(serverError.code, response.status),
			serverError.code,
			response.status
		);
	}
	return response;
}

function withModelA(
	request: Record<string, unknown>,
	modelA?: PassphraseEnvelope
): Record<string, unknown> {
	if (modelA) {
		request.access = modelA.access;
		request.wrapped_identity = bytesToBase64(modelA.wrappedIdentity);
	}
	return request;
}

function openedSecret(response: Response): OpenedSecret {
	const header = (name: string) => response.headers.get(name) ?? undefined;
	const kind = header('Flick-Kind');
	if ((kind !== 'text' && kind !== 'file') || !response.body) {
		throw new SecretApiError('Flick sent an unreadable secret.', 'invalid_response', 0);
	}
	const encryptedFilename = header('Flick-Encrypted-Filename');
	const wrappedIdentity = header('Flick-Wrapped-Identity');
	return {
		kind,
		expiresAt: header('Flick-Expires-At') ?? '',
		contentType: header('Flick-Content-Type') || 'application/octet-stream',
		encryptedFilename: encryptedFilename ? base64ToBytes(encryptedFilename) : undefined,
		wrappedIdentity: wrappedIdentity ? base64ToBytes(wrappedIdentity) : undefined,
		body: response.body
	};
}

type ServerError = {
	code: string;
};

async function readServerError(response: Response): Promise<ServerError> {
	try {
		const body = (await response.json()) as { error?: { code?: string } };
		return { code: body.error?.code ?? 'request_failed' };
	} catch {
		return { code: 'request_failed' };
	}
}

function clientErrorMessage(code: string, status: number): string {
	switch (code) {
		case 'invalid_access':
			return 'Passphrase is invalid.';
		case 'not_found':
		case 'consumed':
		case 'expired':
			return 'This secret is no longer available.';
		case 'payload_too_large':
			return 'This file is too large.';
		case 'upload_failed':
			return 'Upload failed. Try again.';
		case 'unauthorized':
		case 'not_ready':
			return 'Flick is not ready. Try again shortly.';
		default:
			if (status >= 500) {
				return 'Flick could not complete the request. Try again.';
			}
			return 'Could not complete the request. Check the input and try again.';
	}
}

function normalizeBaseUrl(value: string): string {
	return value.replace(/\/+$/, '');
}
