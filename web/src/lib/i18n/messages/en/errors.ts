// API and client errors shown on several screens (web/src/lib/api/*.ts) and the error page
// (web/src/routes/+error.svelte).
export const errors = {
	invalidPassphrase: 'Passphrase is invalid.',
	noLongerAvailable: 'This secret is no longer available.',
	fileTooLarge: 'This file is too large.',
	storageFull: 'Object storage is at capacity. Try again later.',
	uploadFailed: 'Upload failed. Try again.',
	notReady: 'Flick is not ready. Try again shortly.',
	serverFailed: 'Flick could not complete the request. Try again.',
	requestFailed: 'Could not complete the request. Check the input and try again.',
	rateLimited: 'Too many tries in a short time. Wait a moment and try again.',
	largeUploadStartFailed: 'Could not start the large upload. Try again.',
	uploadSizeMismatch: 'Upload size mismatch. Try again.',
	uploadCancelled: 'Upload cancelled.',
	uploadUnreachable: 'Could not reach the upload endpoint. Check your connection and try again.',
	uploadStartFailed: 'Could not start the upload. Try again.',
	flickUnreachable: 'Could not reach Flick. Check your connection and try again.',
	unreadableSecret: 'Flick sent an unreadable secret.',
	managementUnavailable: 'Management link unavailable.',
	deliveryStatusUnverified: 'Could not verify delivery status. Try again.',
	deliveryStatusUnreachable:
		'Could not refresh delivery status. Check your connection and try again.',
	notCancellable: 'Delivery can no longer be cancelled.',
	tooManyStatusChecks: 'Too many status checks. Wait a moment and try again.',
	page: {
		notFoundTitle: 'Page not found',
		errorTitle: (status: number) => `Error ${status}`,
		label: (status: number) => `error ${status}`,
		notFoundHeading: 'Nothing here.',
		failedHeading: 'Something went wrong.',
		notFoundBody:
			'No page lives at this address. Check the link for a typo, or head back and start a fresh one.',
		failedBody:
			'An unexpected error interrupted this page. Starting over from the front page is usually enough.',
		cta: 'Create a one-time link'
	}
};
