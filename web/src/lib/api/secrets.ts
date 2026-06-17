import type {
	AccessVerifierPayload,
	EncryptedFilePayload,
	EncryptedTextPayload,
	KdfParams
} from '$lib/crypto/text';

export const DEFAULT_API_BASE_URL =
	import.meta.env.PUBLIC_BURNLINK_API_BASE_URL || 'http://localhost:8080';

export type TTLSeconds = 600 | 3600 | 86400;

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
	access: {
		kdf: KdfParams;
	};
	size_bytes: number;
	expires_at: string;
};

export type SecretAPIClient = {
	createTextSecret(
		payload: EncryptedTextPayload,
		ttlSeconds: TTLSeconds,
		access: AccessVerifierPayload
	): Promise<CreateSecretResponse>;
	createFileSecret(
		payload: EncryptedFilePayload,
		ttlSeconds: TTLSeconds,
		access: AccessVerifierPayload
	): Promise<CreateSecretResponse>;
	getSecretMetadata(id: string): Promise<GetSecretMetadataResponse>;
	openSecret(id: string, accessProof: string): Promise<GetSecretResponse>;
};

export class SecretAPIError extends Error {
	readonly code: string;
	readonly status: number;

	constructor(message: string, code: string, status: number) {
		super(message);
		this.name = 'SecretAPIError';
		this.code = code;
		this.status = status;
	}
}

type ClientOptions = {
	baseURL?: string;
	fetcher?: typeof fetch;
};

export function createSecretAPIClient(options: ClientOptions = {}): SecretAPIClient {
	const baseURL = normalizeBaseURL(options.baseURL ?? DEFAULT_API_BASE_URL);
	const fetcher = options.fetcher ?? fetch;

	return {
		async createTextSecret(payload, ttlSeconds, access) {
			const body = {
				kind: 'text',
				ciphertext: payload.ciphertext,
				nonce: payload.nonce,
				kdf: payload.kdf,
				size_bytes: payload.size_bytes,
				ttl_seconds: ttlSeconds,
				max_views: 1,
				access
			};

			return requestJSON<CreateSecretResponse>(fetcher, `${baseURL}/api/secrets`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify(body)
			});
		},

		async createFileSecret(payload, ttlSeconds, access) {
			const body = {
				kind: 'file',
				ciphertext: payload.ciphertext,
				nonce: payload.nonce,
				kdf: payload.kdf,
				encrypted_filename: payload.encrypted_filename,
				content_type: payload.content_type,
				size_bytes: payload.size_bytes,
				ttl_seconds: ttlSeconds,
				max_views: 1,
				access
			};

			return requestJSON<CreateSecretResponse>(fetcher, `${baseURL}/api/secrets`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify(body)
			});
		},

		getSecretMetadata(id) {
			return requestJSON<GetSecretMetadataResponse>(
				fetcher,
				`${baseURL}/api/secrets/${encodeURIComponent(id)}`
			);
		},

		openSecret(id, accessProof) {
			return requestJSON<GetSecretResponse>(
				fetcher,
				`${baseURL}/api/secrets/${encodeURIComponent(id)}/open`,
				{
					method: 'POST',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify({ access_proof: accessProof })
				}
			);
		}
	};
}

export function createShareURL(origin: string, id: string): string {
	const url = new URL(origin);
	url.pathname = `/s/${encodeURIComponent(id)}`;
	url.search = '';
	url.hash = '';
	return url.toString();
}

async function requestJSON<T>(
	fetcher: typeof fetch,
	input: RequestInfo | URL,
	init?: RequestInit
): Promise<T> {
	let response: Response;
	try {
		response = await fetcher(input, init);
	} catch {
		throw new SecretAPIError(
			'Could not reach BurnLink. Check your connection and try again.',
			'network_error',
			0
		);
	}

	if (!response.ok) {
		const serverError = await readServerError(response);
		throw new SecretAPIError(
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
			return 'BurnLink is not ready. Try again shortly.';
		default:
			if (status >= 500) {
				return 'BurnLink could not complete the request. Try again.';
			}
			return 'Could not complete the request. Check the input and try again.';
	}
}

function normalizeBaseURL(value: string): string {
	return value.replace(/\/+$/, '');
}
