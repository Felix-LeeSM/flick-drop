import type { Messages } from './index';

// Rewords internal/httpapi/pipedevice.go:pipeDeviceLabel; other shapes pass through.
export function deviceLabelText(label: string, text: Messages): string {
	const pending = text.live.pending;
	const browserName = (browser: string) =>
		browser === 'Unknown browser' ? pending.unknownBrowser : browser;
	const match = /^(.+) on (.+)$/.exec(label);
	if (match) {
		return pending.device(browserName(match[1]), match[2]);
	}
	return browserName(label);
}
