import { isManagementToken } from '#lib/api/management.js';

export function submissionFromFragment(
	fragment: string
): { token: string; fingerprint: string; memoKey?: string } | null {
	if (fragment.length > 256) {
		return null;
	}
	const match =
		/^#submit=([A-Za-z0-9_-]{43})&fp=([A-Za-z0-9_-]{43})(?:&mk=([A-Za-z0-9-]{1,128}))?$/.exec(
			fragment
		);
	if (!match || !isManagementToken(match[1]) || !isManagementToken(match[2])) {
		return null;
	}
	return match[3]
		? { token: match[1], fingerprint: match[2], memoKey: match[3] }
		: { token: match[1], fingerprint: match[2] };
}

export function retrievalFromFragment(
	fragment: string
): { token: string; identity: string } | null {
	// Bound input before parsing; a damaged key is returned so the page can tell it from a missing one.
	if (fragment.length > 256) {
		return null;
	}
	const match = /^#receive=([A-Za-z0-9_-]{43})(?:&key=([A-Za-z0-9-]{1,128}))?$/.exec(fragment);
	return match && isManagementToken(match[1])
		? { token: match[1], identity: match[2] ?? '' }
		: null;
}

export function submissionUrl(
	origin: string,
	id: string,
	token: string,
	fingerprint: string,
	memoKey?: string
): string {
	const url = new URL(`/r/${encodeURIComponent(id)}`, origin);
	url.hash = memoKey
		? `submit=${token}&fp=${fingerprint}&mk=${memoKey}`
		: `submit=${token}&fp=${fingerprint}`;
	if (!submissionFromFragment(url.hash)) {
		throw new Error('Invalid submission link.');
	}
	return url.toString();
}

export function retrievalUrl(origin: string, id: string, token: string, identity: string): string {
	const url = new URL(`/r/${encodeURIComponent(id)}/receive`, origin);
	url.hash = `receive=${token}&key=${identity}`;
	if (!identity || !retrievalFromFragment(url.hash)) {
		throw new Error('Invalid private retrieval link.');
	}
	return url.toString();
}

type Handoff = { id: string; token: string; submissionUrl: string };
// One browser-memory handoff. Refresh/new-device visits cannot reconstruct a submission token.
let handoff: Handoff | null = null;
export function handOffRequest(value: Handoff): void {
	handoff = value;
}
export function takeRequest(id: string, token: string): Handoff | null {
	const value = handoff;
	handoff = null;
	return value?.id === id && value.token === token ? value : null;
}
export function clearRequestHandoff(): void {
	handoff = null;
}
