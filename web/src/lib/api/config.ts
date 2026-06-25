// Client-facing size limits, fetched from the server at boot via GET /api/config.
// The server re-enforces both limits, so these values are advisory — a fetch
// failure (offline, misconfigured origin) must not break the app, so it falls
// back to the same built-in defaults as internal/config/defaults.go.

export const DEFAULT_PAYLOAD_INLINE_MAX_BYTES = 1_048_576; // 1 MiB
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
