import { encodeKeyFragment } from '$lib/crypto/fragment';
import type {
	AccessVerifierPayload,
	EncryptedFilePayload,
	EncryptedTextPayload,
	KdfParams
} from '$lib/crypto/text';
import { base64ToBytes } from '$lib/crypto/text';
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

export type GetTextSecretResponse = EncryptedTextPayload & {
	id: string;
	kind: 'text';
	expires_at: string;
};

export type GetFileSecretResponse = EncryptedFilePayload & {
	id: string;
	kind: 'file';
	expires_at: string;
};

export type GetSecretResponse = GetTextSecretResponse | GetFileSecretResponse;

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
		payload: EncryptedTextPayload,
		ttlSeconds: TtlSeconds,
		access?: AccessVerifierPayload
	): Promise<CreateSecretResponse>;
	createFileSecret(
		payload: EncryptedFilePayload,
		ttlSeconds: TtlSeconds,
		access?: AccessVerifierPayload,
		// Cancels a large-file attempt; no later upload/finalize or result may follow.
		signal?: AbortSignal,
		onProgress?: (progress: FileUploadProgress) => void
	): Promise<CreateSecretResponse>;
	getSecretMetadata(id: string): Promise<GetSecretMetadataResponse>;
	openSecret(id: string, accessProof?: string): Promise<GetSecretResponse>;
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
		createTextSecret(payload, ttlSeconds, access) {
			// Model A sends kdf + access (passphrase-derived); Model B omits both.
			const body: Record<string, unknown> = {
				kind: 'text',
				ciphertext: payload.ciphertext,
				nonce: payload.nonce,
				size_bytes: payload.size_bytes,
				ttl_seconds: ttlSeconds,
				max_views: 1
			};
			if (access) {
				body.kdf = payload.kdf;
				body.access = access;
			}

			return requestJson<CreateSecretResponse>(fetcher, `${baseUrl}/api/secrets`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify(body)
			});
		},

		createFileSecret(payload, ttlSeconds, access, signal, onProgress) {
			if (payload.size_bytes > limits.maxFileBytes) {
				// Reject before any network call — the server would refuse it too.
				return Promise.reject(
					new SecretApiError('This file is too large.', 'payload_too_large', 0)
				);
			}
			if (payload.size_bytes <= limits.payloadInlineMaxBytes) {
				return createInlineFileSecret(fetcher, baseUrl, payload, ttlSeconds, access);
			}
			return createLargeFileSecret(
				fetcher,
				baseUrl,
				payload,
				ttlSeconds,
				access,
				signal,
				onProgress
			);
		},

		getSecretMetadata(id) {
			return requestJson<GetSecretMetadataResponse>(
				fetcher,
				`${baseUrl}/api/secrets/${encodeURIComponent(id)}`
			);
		},

		openSecret(id, accessProof) {
			// Model A sends an access proof; Model B omits it (link is the
			// capability). An empty body still satisfies the required request body.
			const body = accessProof ? { access_proof: accessProof } : {};
			return requestJson<GetSecretResponse>(
				fetcher,
				`${baseUrl}/api/secrets/${encodeURIComponent(id)}/open`,
				{
					method: 'POST',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify(body)
				}
			);
		}
	};
}

// createInlineFileSecret sends the ciphertext in the request body — the SQLite
// BLOB path. Files at or below payloadInlineMaxBytes take this route.
function createInlineFileSecret(
	fetcher: typeof fetch,
	baseUrl: string,
	payload: EncryptedFilePayload,
	ttlSeconds: TtlSeconds,
	access?: AccessVerifierPayload
): Promise<CreateSecretResponse> {
	const body: Record<string, unknown> = {
		kind: 'file',
		ciphertext: payload.ciphertext,
		nonce: payload.nonce,
		encrypted_filename: payload.encrypted_filename,
		content_type: payload.content_type,
		size_bytes: payload.size_bytes,
		ttl_seconds: ttlSeconds,
		max_views: 1
	};
	if (access) {
		body.kdf = payload.kdf;
		body.access = access;
	}

	return requestJson<CreateSecretResponse>(fetcher, `${baseUrl}/api/secrets`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify(body)
	});
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
	payload: EncryptedFilePayload,
	ttlSeconds: TtlSeconds,
	access?: AccessVerifierPayload,
	signal?: AbortSignal,
	onProgress?: (progress: FileUploadProgress) => void
): Promise<CreateSecretResponse> {
	throwIfUploadAborted(signal);
	onProgress?.({ stage: 'preparing' });
	const body: Record<string, unknown> = {
		kind: 'file',
		// ciphertext intentionally omitted — that's what selects the large path.
		nonce: payload.nonce,
		encrypted_filename: payload.encrypted_filename,
		content_type: payload.content_type,
		size_bytes: payload.size_bytes,
		ttl_seconds: ttlSeconds,
		max_views: 1
	};
	if (access) {
		body.kdf = payload.kdf;
		body.access = access;
	}

	const staged = await requestJson<CreateSecretResponse>(fetcher, `${baseUrl}/api/secrets`, {
		signal,
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify(body)
	});
	if (!staged.upload) {
		// The server only returns upload when ciphertext was omitted; reaching
		// here without it means a contract mismatch (or S3 not enabled).
		throw new SecretApiError('Could not start the large upload. Try again.', 'upload_failed', 0);
	}

	await uploadToObjectStore(fetcher, staged.upload, payload.ciphertext, signal, onProgress);
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

// uploadToObjectStore sends the raw ciphertext as the request body. The
// ciphertext arrives base64 and must be decoded first: Content-Length is signed,
// and base64 is a third longer than the bytes it encodes, so uploading the text
// form would fail authentication. Content-Length is not set by hand — the
// browser forbids it as a header and derives it from the body, which is exactly
// the signed value.
export async function uploadToObjectStore(
	fetcher: typeof fetch,
	upload: PresignedUpload,
	ciphertextBase64: string,
	signal?: AbortSignal,
	onProgress?: (progress: FileUploadProgress) => void
): Promise<void> {
	throwIfUploadAborted(signal);
	const bytes = base64ToBytes(ciphertextBase64);
	// Content-Length is a forbidden header name, so it cannot be set here — the
	// browser derives it from the body. Compare against the signed value anyway:
	// a mismatch means the encryption overhead assumption drifted from the
	// server's, and failing here names the cause instead of leaving a bare 403
	// from the bucket.
	const signedLength = Number(
		Object.entries(upload.headers).find(([name]) => name.toLowerCase() === 'content-length')?.[1]
	);
	if (Number.isFinite(signedLength) && signedLength !== bytes.byteLength) {
		throw new SecretApiError('Upload size mismatch. Try again.', 'upload_failed', 0);
	}
	// bytes is a fresh Uint8Array over an ArrayBuffer (offset 0), so its backing
	// buffer carries exactly the signed ciphertext length.
	const body = bytes.buffer as ArrayBuffer;
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

export function createShareUrl(origin: string, id: string, key?: Uint8Array): string {
	const url = new URL(origin);
	url.pathname = `/s/${encodeURIComponent(id)}`;
	url.search = '';
	// Model B carries the decryption key in the fragment, which the browser
	// never sends to the server. See web/src/lib/crypto/fragment.ts.
	url.hash = key ? encodeKeyFragment(key) : '';
	return url.toString();
}

async function requestJson<T>(
	fetcher: typeof fetch,
	input: RequestInfo | URL,
	init?: RequestInit
): Promise<T> {
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
	const result = (await response.json()) as T;
	throwIfUploadAborted(init?.signal);
	return result;
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
