import { DEFAULT_API_BASE_URL, SecretApiError } from './secrets';

export type ManagementStatus =
	| 'pending_upload'
	| 'active'
	| 'opened'
	| 'locked'
	| 'cancelled'
	| 'unavailable';
export type ManagementSnapshot = {
	id: string;
	status: ManagementStatus;
	expires_at: string;
	management_expires_at: string;
	can_cancel: boolean;
};

export function isManagementToken(token: string): boolean {
	// Canonical unpadded base64url for 32 bytes (the final sextet has two zero bits).
	return /^[A-Za-z0-9_-]{42}[AEIMQUYcgkosw048]$/.test(token);
}

export function managementTokenFromFragment(fragment: string): string | null {
	const fields = new URLSearchParams(fragment.replace(/^#/, ''));
	const token = fields.get('manage');
	return fields.size === 1 && token && isManagementToken(token) ? token : null;
}

export function createManagementUrl(origin: string, id: string, token: string): string {
	if (!isManagementToken(token)) {
		throw new Error('Invalid management capability');
	}
	const url = new URL(`/m/${encodeURIComponent(id)}`, origin);
	url.hash = new URLSearchParams({ manage: token }).toString();
	return url.toString();
}

export class ManagementApiError extends SecretApiError {
	constructor(
		message: string,
		code: string,
		status: number,
		readonly snapshot?: ManagementSnapshot
	) {
		super(message, code, status);
	}
}

function snapshotFrom(value: unknown, id: string): ManagementSnapshot {
	const data = value as Partial<ManagementSnapshot> | null;
	const statuses: ManagementStatus[] = [
		'pending_upload',
		'active',
		'opened',
		'locked',
		'cancelled',
		'unavailable'
	];
	if (
		!data ||
		data.id !== id ||
		!statuses.includes(data.status as ManagementStatus) ||
		typeof data.expires_at !== 'string' ||
		!Number.isFinite(Date.parse(data.expires_at)) ||
		data.management_expires_at !== data.expires_at ||
		data.can_cancel !== (data.status === 'active' || data.status === 'pending_upload')
	) {
		throw new ManagementApiError(
			'Could not verify delivery status. Try again.',
			'invalid_response',
			0
		);
	}
	return {
		id,
		status: data.status as ManagementStatus,
		expires_at: data.expires_at,
		management_expires_at: data.expires_at,
		can_cancel: data.can_cancel
	};
}

export function createManagementApiClient(
	options: { baseUrl?: string; fetcher?: typeof fetch } = {}
) {
	const base = (options.baseUrl ?? DEFAULT_API_BASE_URL).replace(/\/+$/, '');
	const fetcher = options.fetcher ?? fetch;
	async function request(id: string, token: string, cancel: boolean, signal?: AbortSignal) {
		if (!isManagementToken(token)) {
			throw new ManagementApiError('Management link unavailable.', 'management_unavailable', 404);
		}
		let response: Response;
		try {
			response = await fetcher(
				`${base}/api/secrets/${encodeURIComponent(id)}/${cancel ? 'revoke' : 'management'}`,
				{
					method: cancel ? 'POST' : 'GET',
					signal,
					credentials: 'omit',
					cache: 'no-store',
					redirect: 'error',
					headers: {
						authorization: `Bearer ${token}`,
						...(cancel ? { 'Content-Type': 'application/json' } : {})
					},
					...(cancel ? { body: '{}' } : {})
				}
			);
		} catch {
			throw new ManagementApiError(
				'Could not refresh delivery status. Check your connection and try again.',
				'network_error',
				0
			);
		}
		return readManagementResponse(response, id, cancel);
	}
	return {
		get: (id: string, token: string, signal?: AbortSignal) => request(id, token, false, signal),
		revoke: (id: string, token: string, signal?: AbortSignal) => request(id, token, true, signal)
	};
}

async function readManagementResponse(
	response: Response,
	id: string,
	cancel: boolean
): Promise<ManagementSnapshot> {
	if (response.status === 404) {
		throw new ManagementApiError('Management link unavailable.', 'management_unavailable', 404);
	}
	let data: unknown;
	try {
		data = await response.json();
	} catch {
		data = null;
	}
	if (response.ok) {
		return snapshotFrom(data, id);
	}
	if (response.status === 409 && cancel) {
		const conflict = data as { error?: { code?: string }; status?: unknown } | null;
		if (conflict?.error?.code === 'not_cancellable') {
			throw new ManagementApiError(
				'Delivery can no longer be cancelled.',
				'not_cancellable',
				409,
				snapshotFrom(conflict.status, id)
			);
		}
	}
	throw new ManagementApiError(
		response.status === 429
			? 'Too many status checks. Wait a moment and try again.'
			: 'Could not verify delivery status. Try again.',
		'management_failed',
		response.status
	);
}
