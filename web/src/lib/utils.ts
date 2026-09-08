import { type ClassValue, clsx } from 'clsx';
import { twMerge } from 'tailwind-merge';

export function cn(...inputs: ClassValue[]): string {
	return twMerge(clsx(inputs));
}

// Human-readable byte size (B / KiB / MiB). Shared by the create + open pages.
// Rounds down, never up. Rounding up overstates both things this formats: a
// limit fetched from /api/config (1,048,560 bytes rendered as "1024.0 KiB" — a
// size the server refuses, and one unit short of reading as 1 MiB) and a real
// file size (anything in [1048524.8, 1048576) shows the same nonsense).
export function formatBytes(bytes: number): string {
	if (bytes < 1024) {
		return `${bytes} B`;
	}
	if (bytes < 1024 * 1024) {
		return `${floorTo(bytes / 1024, 1)} KiB`;
	}
	return `${floorTo(bytes / 1024 / 1024, 2)} MiB`;
}

function floorTo(value: number, digits: number): string {
	const factor = 10 ** digits;
	return (Math.floor(value * factor) / factor).toFixed(digits);
}

export type WithoutChild<T> = T extends { child?: unknown } ? Omit<T, 'child'> : T;
export type WithoutChildren<T> = T extends { children?: unknown } ? Omit<T, 'children'> : T;
export type WithoutChildrenOrChild<T> = WithoutChildren<WithoutChild<T>>;
export type WithElementRef<T, U extends HTMLElement = HTMLElement> = T & { ref?: U | null };
