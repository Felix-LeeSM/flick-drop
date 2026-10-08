import type { RequestPayload } from '$lib/crypto/requests';
import { bytesToBase64 } from '$lib/crypto/text';
import type { ClientLimits } from './config';
import { isManagementToken } from './management';
import { DEFAULT_API_BASE_URL, SecretApiError } from './secrets';

export type RequestState = 'waiting' | 'submitted' | 'consumed' | 'cancelled';
export type RequestCreated = {
	id: string;
	expires_at: string;
	submission_token: string;
	retrieval_token: string;
};
export type RequestOwner = {
	public_key: string;
	fingerprint: string;
	expires_at: string;
	state: RequestState;
};
export type RequestInstructions = Omit<RequestOwner, 'state'> & {
	generation: number;
	can_submit: boolean;
};
export type Submission = Readonly<{ generation: number; attemptToken: string; body: string }>;
export type AttemptReceipt = { generation: number; state: 'waiting' | 'accepted' | 'unavailable' };
export class RequestApiError extends SecretApiError {}

export function requestLimits(config: ClientLimits) {
	return {
		maxTextBytes: config.payloadInlineMaxBytes,
		maxFileBytes: Math.min(config.payloadInlineMaxBytes, config.maxFileBytes)
	};
}

// Snapshot the exact encrypted body once. An unknown outcome may retry only these bytes.
export function prepareSubmission(payload: RequestPayload, generation: number): Submission {
	const attemptToken = bytesToBase64(crypto.getRandomValues(new Uint8Array(32)))
		.replace(/\+/g, '-')
		.replace(/\//g, '_')
		.replace(/=+$/, '');
	return Object.freeze({
		generation,
		attemptToken,
		body: JSON.stringify({
			generation,
			attempt_token: attemptToken,
			kind: payload.kind,
			size_bytes: payload.size_bytes,
			envelope: payload.envelope,
			ciphertext: payload.ciphertext
		})
	});
}

function invalidResponse(): never {
	throw new RequestApiError('Could not verify the server response.', 'invalid_response', 0);
}
function record(value: unknown): Record<string, unknown> {
	if (!value || typeof value !== 'object' || Array.isArray(value)) {
		invalidResponse();
	}
	return value as Record<string, unknown>;
}
function metadata(value: unknown) {
	const data = record(value);
	if (
		typeof data.public_key !== 'string' ||
		typeof data.fingerprint !== 'string' ||
		!isManagementToken(data.fingerprint) ||
		typeof data.expires_at !== 'string' ||
		!Number.isFinite(Date.parse(data.expires_at))
	) {
		invalidResponse();
	}
	return {
		public_key: data.public_key,
		fingerprint: data.fingerprint,
		expires_at: data.expires_at
	};
}
function generation(value: unknown): number {
	if (typeof value !== 'number' || !Number.isInteger(value) || value < 1 || value > 16) {
		invalidResponse();
	}
	return value;
}

export function createRequestApiClient(options: { baseUrl?: string; fetcher?: typeof fetch } = {}) {
	const base = (options.baseUrl ?? DEFAULT_API_BASE_URL).replace(/\/+$/, '');
	const fetcher = options.fetcher ?? fetch;
	async function request(
		path: string,
		method: 'GET' | 'POST',
		token?: string,
		body?: string,
		signal?: AbortSignal
	): Promise<unknown> {
		if (token !== undefined && !isManagementToken(token)) {
			throw new RequestApiError('Request link unavailable.', 'request_unavailable', 404);
		}
		let response: Response;
		try {
			response = await fetcher(`${base}/api/requests${path}`, {
				method,
				signal,
				credentials: 'omit',
				cache: 'no-store',
				redirect: 'error',
				referrerPolicy: 'no-referrer',
				headers: {
					...(token ? { authorization: `Bearer ${token}` } : {}),
					...(body === undefined ? {} : { 'Content-Type': 'application/json' })
				},
				body
			});
		} catch {
			throw new RequestApiError(
				'Offline or connection interrupted. Check your connection.',
				'network_error',
				0
			);
		}
		if (!response.ok) {
			const messages: Record<number, string> = {
				404: 'Request unavailable. The link may be invalid or expired.',
				409: 'The request changed. Refresh its status.',
				413: 'The content exceeds the supported size limit.',
				429: 'Too many requests. Wait a moment before trying again.'
			};
			throw new RequestApiError(
				messages[response.status] ?? 'The server is temporarily unavailable. Try again later.',
				'request_failed',
				response.status
			);
		}
		try {
			return await response.json();
		} catch {
			invalidResponse();
		}
	}
	const path = (id: string, action = '') => `/${encodeURIComponent(id)}${action}`;
	return {
		async create(
			publicKey: string,
			ttlSeconds: number,
			signal?: AbortSignal
		): Promise<RequestCreated> {
			const data = record(
				await request(
					'',
					'POST',
					undefined,
					JSON.stringify({ public_key: publicKey, ttl_seconds: ttlSeconds }),
					signal
				)
			);
			if (
				typeof data.id !== 'string' ||
				!data.id.length ||
				data.id.length > 256 ||
				/[\r\n]/.test(data.id) ||
				typeof data.expires_at !== 'string' ||
				!Number.isFinite(Date.parse(data.expires_at)) ||
				typeof data.submission_token !== 'string' ||
				!isManagementToken(data.submission_token) ||
				typeof data.retrieval_token !== 'string' ||
				!isManagementToken(data.retrieval_token) ||
				data.submission_token === data.retrieval_token
			) {
				invalidResponse();
			}
			return {
				id: data.id,
				expires_at: data.expires_at,
				submission_token: data.submission_token,
				retrieval_token: data.retrieval_token
			};
		},
		async instructions(
			id: string,
			token: string,
			signal?: AbortSignal
		): Promise<RequestInstructions> {
			const data = record(await request(path(id), 'GET', token, undefined, signal));
			if (typeof data.can_submit !== 'boolean') {
				invalidResponse();
			}
			return {
				...metadata(data),
				generation: generation(data.generation),
				can_submit: data.can_submit
			};
		},
		async owner(id: string, token: string, signal?: AbortSignal): Promise<RequestOwner> {
			const data = record(await request(path(id, '/owner'), 'GET', token, undefined, signal));
			if (!['waiting', 'submitted', 'consumed', 'cancelled'].includes(String(data.state))) {
				invalidResponse();
			}
			return { ...metadata(data), state: data.state as RequestState };
		},
		async submit(
			id: string,
			token: string,
			attempt: Submission,
			signal?: AbortSignal
		): Promise<void> {
			const data = record(await request(path(id, '/submit'), 'POST', token, attempt.body, signal));
			if (data.state !== 'submitted' || data.generation !== attempt.generation) {
				invalidResponse();
			}
		},
		async attempt(
			id: string,
			token: string,
			attempt: Submission,
			signal?: AbortSignal
		): Promise<AttemptReceipt> {
			const data = record(
				await request(
					path(id, '/attempt'),
					'POST',
					token,
					JSON.stringify({ generation: attempt.generation, attempt_token: attempt.attemptToken }),
					signal
				)
			);
			if (
				data.generation !== attempt.generation ||
				!['waiting', 'accepted', 'unavailable'].includes(String(data.state))
			) {
				invalidResponse();
			}
			return { generation: attempt.generation, state: data.state as AttemptReceipt['state'] };
		},
		// The crypto module validates the entire payload before decryption. These operations have NO body.
		open: (id: string, token: string, signal?: AbortSignal) =>
			request(path(id, '/open'), 'POST', token, undefined, signal),
		async revoke(id: string, token: string, signal?: AbortSignal): Promise<void> {
			if (
				record(await request(path(id, '/revoke'), 'POST', token, undefined, signal)).state !==
				'cancelled'
			) {
				invalidResponse();
			}
		}
	};
}
