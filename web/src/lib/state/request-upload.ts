import {
	type AttemptReceipt,
	type createRequestApiClient,
	RequestApiError,
	type RequestInstructions,
	type ReservedUpload,
	type Submission
} from '$lib/api/requests';
import type { FileUploadProgress } from '$lib/api/secrets';

type RequestApi = ReturnType<typeof createRequestApiClient>;
export type AttemptOutcome =
	| AttemptReceipt
	| { state: 'renewed'; instructions: RequestInstructions };

export function uploadExpired(reservation: ReservedUpload, now = Date.now()): boolean {
	return (
		now >=
		Math.min(
			Date.parse(reservation.reservation_expires_at),
			Date.parse(reservation.upload.expires_at)
		)
	);
}

// One invocation is one explicit user action, including any exact-body reserve retry.
export async function finishRequestUpload(
	api: RequestApi,
	id: string,
	token: string,
	attempt: Submission,
	expiresAt: string,
	signal: AbortSignal,
	onProgress: (progress: FileUploadProgress) => void,
	onReservation: (reservation: ReservedUpload) => void
): Promise<void> {
	signal.throwIfAborted();
	onProgress({ stage: 'preparing' });
	const reservation = await api.reserve(id, token, attempt, expiresAt, signal);
	signal.throwIfAborted();
	if (reservation.state === 'submitted') {
		return;
	}
	onReservation(reservation);
	if (uploadExpired(reservation)) {
		throw new RequestApiError(
			'The upload reservation expired. Check the attempt or cancel it before choosing another file.',
			'upload_expired',
			0
		);
	}
	await api.put(reservation.upload, attempt.ciphertext as string, signal, onProgress);
	signal.throwIfAborted();
	// A PUT finishing does not establish acceptance, even at 100 percent.
	onProgress({ stage: 'finalizing' });
	await api.finalize(id, token, attempt, signal);
	signal.throwIfAborted();
}

export async function inspectRequestAttempt(
	api: RequestApi,
	id: string,
	token: string,
	attempt: Submission,
	signal: AbortSignal
): Promise<AttemptOutcome> {
	try {
		return await api.attempt(id, token, attempt, signal);
	} catch (cause) {
		if (!(cause instanceof RequestApiError) || cause.status !== 404) {
			throw cause;
		}
		// Expiry/abandon invalidates the old generation. Only server evidence permits a new attempt.
		const instructions = await api.instructions(id, token, signal);
		if (instructions.generation > attempt.generation && instructions.can_submit) {
			return { state: 'renewed', instructions };
		}
		throw cause;
	}
}

export async function cancelRequestUpload(
	api: RequestApi,
	id: string,
	token: string,
	attempt: Submission,
	expiresAt: string,
	signal: AbortSignal
): Promise<AttemptOutcome> {
	const outcome = await inspectRequestAttempt(api, id, token, attempt, signal);
	signal.throwIfAborted();
	if (
		outcome.state === 'renewed' ||
		outcome.state === 'accepted' ||
		outcome.state === 'unavailable'
	) {
		return outcome;
	}
	if (outcome.state === 'waiting') {
		// A lost reservation may still execute after XHR/fetch abort. Establish that SAME attempt
		// before abandoning it, so a newer generation fences any late server work.
		const reservation = await api.reserve(id, token, attempt, expiresAt, signal);
		signal.throwIfAborted();
		if (reservation.state === 'submitted') {
			return { generation: attempt.generation, state: 'accepted' };
		}
	}
	await api.abandon(id, token, attempt, signal);
	signal.throwIfAborted();
	const instructions = await api.instructions(id, token, signal);
	if (instructions.generation > attempt.generation && instructions.can_submit) {
		return { state: 'renewed', instructions };
	}
	return { generation: attempt.generation, state: 'unavailable' };
}
