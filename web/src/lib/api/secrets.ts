import { encodeKeyFragment } from '$lib/crypto/fragment';
import type {
	AccessVerifierPayload,
	EncryptedFilePayload,
	EncryptedTextPayload,
	KdfParams
} from '$lib/crypto/text';

export const DEFAULT_API_BASE_URL =
	import.meta.env.PUBLIC_FLICK_API_BASE_URL || 'http://localhost:8080';

export type TtlSeconds = number;

export type CreateSecretResponse = {
	id: string;
	expires_at: string;
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
};

export function createSecretApiClient(options: ClientOptions = {}): SecretApiClient {
	const baseUrl = normalizeBaseUrl(options.baseUrl ?? DEFAULT_API_BASE_URL);
	const fetcher = options.fetcher ?? fetch;

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

export function createShareUrl(origin: string, id: string, key?: Uint8Array): string {
	const url = new URL(origin);
	url.pathname = `/s/${encodeURIComponent(id)}`;
	url.search = '';
	// Model B carries the decryption key in the fragment, which the browser
	// never sends to the server. See web/src/lib/crypto/fragment.ts.
	url.hash = key ? encodeKeyFragment(key).slice(1) : '';
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
