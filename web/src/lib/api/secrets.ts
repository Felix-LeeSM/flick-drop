import { encodeKeyFragment } from '$lib/crypto/fragment';
import type {
	AccessVerifierPayload,
	EncryptedFilePayload,
	EncryptedTextPayload,
	KdfParams
} from '$lib/crypto/text';
import { type ClientLimits, defaultLimits } from './config';

export const DEFAULT_API_BASE_URL =
	import.meta.env.PUBLIC_FLICK_API_BASE_URL || 'http://localhost:8080';

export type TtlSeconds = number;

// PresignedPost mirrors the server's presignedPOSTResponse: a form the browser
// POSTs to upload ciphertext straight to the object store. The server never
// sees the bytes. The bucket rejects uploads outside the signed
// content-length-range with 413.
export type PresignedPost = {
	url: string;
	method: string;
	expires_at: string;
	fields: Record<string, string>;
	file_field: string;
};

export type CreateSecretResponse = {
	id: string;
	expires_at: string;
	// Present only for large secrets (request omitted ciphertext). The client
	// uploads the ciphertext multipart to `url` with `fields`, then calls
	// /finalize. Defined here so the large path can read it, but callers see a
	// plain { id, expires_at } — the S3 upload + finalize are completed inside.
	upload?: PresignedPost;
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
		access?: AccessVerifierPayload
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

		createFileSecret(payload, ttlSeconds, access) {
			if (payload.size_bytes > limits.maxFileBytes) {
				// Reject before any network call — the server would refuse it too.
				return Promise.reject(
					new SecretApiError('This file is too large.', 'payload_too_large', 0)
				);
			}
			if (payload.size_bytes <= limits.payloadInlineMaxBytes) {
				return createInlineFileSecret(fetcher, baseUrl, payload, ttlSeconds, access);
			}
			return createLargeFileSecret(fetcher, baseUrl, payload, ttlSeconds, access);
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
//   1. POST /api/secrets WITHOUT ciphertext → server returns a presigned POST.
//   2. POST the ciphertext multipart to the object store using the signed form.
//   3. POST /api/secrets/{id}/finalize so the server HEAD-checks the object and
//      activates the secret.
// Resolves to a plain { id, expires_at } so callers are unaware of the routing.
async function createLargeFileSecret(
	fetcher: typeof fetch,
	baseUrl: string,
	payload: EncryptedFilePayload,
	ttlSeconds: TtlSeconds,
	access?: AccessVerifierPayload
): Promise<CreateSecretResponse> {
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
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify(body)
	});
	if (!staged.upload) {
		// The server only returns upload when ciphertext was omitted; reaching
		// here without it means a contract mismatch (or S3 not enabled).
		throw new SecretApiError('Could not start the large upload. Try again.', 'upload_failed', 0);
	}

	await uploadToObjectStore(fetcher, staged.upload, payload.ciphertext);

	await requestJson<{ id: string; finalized: boolean }>(
		fetcher,
		`${baseUrl}/api/secrets/${encodeURIComponent(staged.id)}/finalize`,
		{
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: '{}'
		}
	);

	return { id: staged.id, expires_at: staged.expires_at };
}

// uploadToObjectStore POSTs the signed form fields and the ciphertext as the
// file_field. The file field must be appended last for the object store to
// validate the signature. The ciphertext is base64; it must be decoded to raw
// bytes so the upload length matches the signed content-length-range.
async function uploadToObjectStore(
	fetcher: typeof fetch,
	upload: PresignedPost,
	ciphertextBase64: string
): Promise<void> {
	const bytes = decodeBase64(ciphertextBase64);
	const formData = new FormData();
	for (const [key, value] of Object.entries(upload.fields)) {
		formData.append(key, value);
	}
	// bytes is a fresh Uint8Array over an ArrayBuffer (offset 0), so its backing
	// buffer carries the exact ciphertext length the signed range expects.
	formData.append(upload.file_field, new Blob([bytes.buffer as ArrayBuffer]));

	let response: Response;
	try {
		response = await fetcher(upload.url, { method: upload.method, body: formData });
	} catch {
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
	let response: Response;
	try {
		response = await fetcher(input, init);
	} catch {
		throw new SecretApiError(
			'Could not reach Flick. Check your connection and try again.',
			'network_error',
			0
		);
	}

	if (!response.ok) {
		const serverError = await readServerError(response);
		throw new SecretApiError(
			clientErrorMessage(serverError.code, response.status),
			serverError.code,
			response.status
		);
	}
	return (await response.json()) as T;
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

// decodeBase64 converts a base64 ciphertext string to raw bytes for the object
// store upload. The standard atob path is wrapped because crypto payloads use
// base64 (no URL-safe variant) and the length must match the signed range.
function decodeBase64(value: string): Uint8Array {
	const binary = atob(value);
	const bytes = new Uint8Array(binary.length);
	for (let i = 0; i < binary.length; i += 1) {
		bytes[i] = binary.charCodeAt(i);
	}
	return bytes;
}

function normalizeBaseUrl(value: string): string {
	return value.replace(/\/+$/, '');
}
