export const common = {
	enterCode: 'Enter Code',
	siteNav: 'Site',
	languageRow: 'Language',
	languageButton: (current: string) => `Change language (current: ${current})`,
	deliveryMethod: 'Delivery method',
	tabLink: 'Create link',
	tabLive: 'Live transfer',
	theme: {
		useLight: 'Use light theme',
		useDark: 'Use dark theme'
	},
	sponsor: {
		label: 'Sponsored',
		opensNewTab: ' (opens in a new tab)'
	},
	urlField: {
		label: 'Share URL',
		copyToClipboard: 'Copy to clipboard',
		copy: 'Copy',
		copied: 'Copied',
		copyFailed: 'Copy unavailable. Select the link above and copy it.'
	},
	share: {
		shareLink: 'Share link',
		failed: 'Could not share. Copy the link or use its QR code.'
	},
	qr: {
		title: 'Scan or copy',
		description: 'Point a camera at the code, or copy the link below.',
		close: 'Close',
		codeLabel: 'QR code for the secret share link'
	},
	stage: {
		creatingLink: 'Creating your link',
		openingSecret: 'Opening your secret',
		cancel: 'Cancel',
		progressSteps: 'Progress steps'
	},
	lifetime: {
		label: 'Expires after',
		group: 'Lifetime',
		preset10Min: '10 min',
		preset1Hour: '1 hour',
		preset24Hours: '24 hours',
		preset7Days: '7 days',
		customValue: 'Custom lifetime value',
		customUnit: 'Custom lifetime unit',
		unitMinutes: 'min',
		unitHours: 'hours',
		unitDays: 'days',
		daysHours: (d: number, h: number) => `${d}d ${h}h`,
		hoursMinutes: (h: number, m: number) => `${h}h ${m}m`,
		minutesSeconds: (m: number, s: number) => `${m}m ${s}s`,
		days: (n: number) => `${n} day${n === 1 ? '' : 's'}`,
		hours: (n: number) => `${n} hour${n === 1 ? '' : 's'}`,
		minutes: (n: number) => `${n} minute${n === 1 ? '' : 's'}`,
		range: (min: string, max: string) => `Choose a lifetime between ${min} and ${max}.`
	}
};
