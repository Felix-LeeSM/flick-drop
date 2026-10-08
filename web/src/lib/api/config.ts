// Client-facing size limits, fetched from the server at boot via GET /api/config.
// The server re-enforces both limits, so these values are advisory — a fetch
// failure (offline, misconfigured origin) must not break the app, so it falls
// back to the plaintext equivalents of internal/config/defaults.go.

// Inline storage bounds ciphertext, so reserve the 16-byte AES-GCM tag.
export const DEFAULT_PAYLOAD_INLINE_MAX_BYTES = 1_048_576 - 16; // 1 MiB minus tag
// The large-file limit already counts plaintext bytes; do not subtract the tag.
export const DEFAULT_MAX_FILE_BYTES = 52_428_800; // 50 MiB

export type ClientLimits = {
	payloadInlineMaxBytes: number;
	maxFileBytes: number;
};

export function defaultLimits(): ClientLimits {
	return {
		payloadInlineMaxBytes: DEFAULT_PAYLOAD_INLINE_MAX_BYTES,
		maxFileBytes: DEFAULT_MAX_FILE_BYTES
	};
}

type RawConfig = {
	payload_inline_max_bytes?: unknown;
	max_file_bytes?: unknown;
};

function positiveInt(raw: unknown, fallback: number): number {
	const value = Number(raw);
	return Number.isFinite(value) && value > 0 ? value : fallback;
}

// getConfig resolves the client-facing limits. It never throws: any network or
// parsing failure yields the built-in defaults so the create flow stays usable.
export async function getConfig(
	baseUrl: string,
	fetcher: typeof fetch = fetch
): Promise<ClientLimits> {
	const normalized = baseUrl.replace(/\/+$/, '');
	try {
		const response = await fetcher(`${normalized}/api/config`);
		if (!response.ok) {
			return defaultLimits();
		}
		const raw = (await response.json()) as RawConfig;
		return {
			payloadInlineMaxBytes: positiveInt(
				raw.payload_inline_max_bytes,
				DEFAULT_PAYLOAD_INLINE_MAX_BYTES
			),
			maxFileBytes: positiveInt(raw.max_file_bytes, DEFAULT_MAX_FILE_BYTES)
		};
	} catch {
		return defaultLimits();
	}
}

// A consuming request must know the server's actual bounds before decrypting.
// Advisory defaults are useful for creation, but could discard a larger valid
// one-time response if a custom deployment's config request temporarily fails.
export async function getVerifiedConfig(
	baseUrl: string,
	fetcher: typeof fetch = fetch,
	signal?: AbortSignal
): Promise<ClientLimits> {
	try {
		const response = await fetcher(`${baseUrl.replace(/\/+$/, '')}/api/config`, {
			signal,
			credentials: 'omit',
			cache: 'no-store',
			redirect: 'error',
			referrerPolicy: 'no-referrer'
		});
		if (!response.ok) {
			throw new Error();
		}
		const raw = (await response.json()) as RawConfig | null;
		for (const value of [raw?.payload_inline_max_bytes, raw?.max_file_bytes]) {
			if (
				typeof value !== 'number' ||
				!Number.isSafeInteger(value) ||
				value <= 0 ||
				value > Number.MAX_SAFE_INTEGER - 16
			) {
				throw new Error();
			}
		}
		return {
			payloadInlineMaxBytes: raw?.payload_inline_max_bytes as number,
			maxFileBytes: raw?.max_file_bytes as number
		};
	} catch {
		throw new Error(
			'Could not verify size limits. Refresh status before opening. Nothing has been opened.'
		);
	}
}
