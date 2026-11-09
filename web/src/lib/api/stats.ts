import { DEFAULT_API_BASE_URL, SecretApiError } from './secrets';

// Metric names mirror internal/usage (the closed set the stores write).
export const USAGE_METRICS = {
	createdText: 'secret_created_text',
	createdFile: 'secret_created_file',
	opened: 'secret_opened',
	openFailed: 'secret_open_failed',
	bytesUploaded: 'bytes_uploaded',
	requestCreated: 'request_created',
	requestSubmitted: 'request_submitted',
	requestConsumed: 'request_consumed'
} as const;

export type StatsDaily = { day: string; metrics: Record<string, number> };
export type StatsLive = { active_secrets: number; live_requests: number };
export type StatsReport = {
	days: number;
	series: StatsDaily[];
	totals: Record<string, number>;
	live: StatsLive;
};

// Distinct error type so the page can branch on stats failures without
// confusing them with secret-delivery errors.
export class StatsApiError extends SecretApiError {}

function reportFrom(value: unknown): StatsReport {
	const data = value as Partial<StatsReport> | null;
	const dailyShape = (entry: StatsDaily | undefined): boolean =>
		!!entry &&
		typeof entry.day === 'string' &&
		/^\d{4}-\d{2}-\d{2}$/.test(entry.day) &&
		!!entry.metrics &&
		typeof entry.metrics === 'object';
	if (
		!data ||
		typeof data.days !== 'number' ||
		!Array.isArray(data.series) ||
		data.series.length === 0 ||
		!data.series.every(dailyShape) ||
		!data.totals ||
		typeof data.totals !== 'object' ||
		!data.live ||
		typeof data.live.active_secrets !== 'number' ||
		typeof data.live.live_requests !== 'number'
	) {
		throw new StatsApiError('Could not read the usage report. Try again.', 'invalid_response', 0);
	}
	return { days: data.days, series: data.series, totals: data.totals, live: data.live };
}

export function createStatsApiClient(options: { baseUrl?: string; fetcher?: typeof fetch } = {}) {
	const base = (options.baseUrl ?? DEFAULT_API_BASE_URL).replace(/\/+$/, '');
	const fetcher = options.fetcher ?? fetch;
	// The admin token travels only in the Authorization header: never in the
	// URL, cookies, or browser storage beyond the page's sessionStorage copy.
	async function get(token: string, days = 30, signal?: AbortSignal): Promise<StatsReport> {
		let response: Response;
		try {
			response = await fetcher(`${base}/api/stats?days=${days}`, {
				method: 'GET',
				signal,
				credentials: 'omit',
				cache: 'no-store',
				redirect: 'error',
				headers: { authorization: `Bearer ${token}` }
			});
		} catch {
			throw new StatsApiError(
				'Could not load the usage report. Check your connection and try again.',
				'network_error',
				0
			);
		}
		if (response.status === 404) {
			throw new StatsApiError(
				'Stats are unavailable. The token was rejected or the endpoint is disabled on this deployment.',
				'stats_unavailable',
				404
			);
		}
		let data: unknown;
		try {
			data = await response.json();
		} catch {
			data = null;
		}
		if (!response.ok) {
			throw new StatsApiError(
				response.status === 429
					? 'Too many report requests. Wait a moment and try again.'
					: 'Could not load the usage report. Try again.',
				'stats_failed',
				response.status
			);
		}
		return reportFrom(data);
	}
	return { get };
}
