export type Sponsor = Readonly<{
	name: string;
	description: string;
	url: string;
}>;

// Repository-reviewed public copy only. null disables the placement at build time.
export const SPONSOR: Sponsor | null = null;

export function validateSponsor(value: unknown): Sponsor | null {
	if (!value || typeof value !== 'object') {
		return null;
	}
	const { name, description, url } = value as Partial<Sponsor>;
	if (
		typeof name !== 'string' ||
		!name.trim() ||
		typeof description !== 'string' ||
		!description.trim() ||
		typeof url !== 'string'
	) {
		return null;
	}
	try {
		const target = new URL(url);
		if (
			target.protocol !== 'https:' ||
			target.username ||
			target.password ||
			/[?#]/.test(url) ||
			target.href !== url
		) {
			return null;
		}
	} catch {
		return null;
	}
	return { name: name.trim(), description: description.trim(), url };
}
