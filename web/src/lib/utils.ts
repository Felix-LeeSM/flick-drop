import { type ClassValue, clsx } from 'clsx';
import { twMerge } from 'tailwind-merge';

export function cn(...inputs: ClassValue[]): string {
	return twMerge(clsx(inputs));
}

// Rounds down: rounding up shows limits the server refuses (1,048,560 B as "1024.0 KiB").
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
