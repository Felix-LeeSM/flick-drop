// Request flow: create a request, submit to it, retrieve the submission.
export const request = {
	shared: {
		badge: 'end-to-end encrypted · one-time request',
		timeRemaining: 'Time remaining',
		statusUnknown: 'Request status is unknown.'
	},
	create: {
		pageTitle: 'Request a secret - Flick',
		headerLink: 'Send a secret',
		heading: 'Request a secret',
		intro: 'Get a link for someone to send you a note, a file, or credentials, once.',
		noteLabel: 'Note',
		optional: 'Optional',
		notePlaceholder: 'Tell your sender what to share (e.g. Wi-Fi password, SSH key)',
		noteTooLong: (max: string, used: string) => `Note must be at most ${max} bytes (${used} used).`,
		noteHint: 'End-to-end encrypted; shown only to the person opening your request link.',
		lifetimeGroup: 'Request lifetime',
		lifetimeHint: 'Starts when you create the link. Submitting does not extend it.',
		unconfirmed:
			'Creation was not confirmed. An unused request may exist and will expire. Its links cannot be recovered. Nothing is retried automatically.',
		unsupported:
			'Request encryption is unavailable. Use a current browser over HTTPS or localhost.',
		creating: 'Creating request…',
		createNew: 'Create a new request',
		create: 'Create request'
	},
	submit: {
		pageTitle: 'Submit a secret - Flick',
		headingAccepted: 'Submission accepted',
		headingExpired: 'Request expired',
		headingUnavailable: 'Request unavailable',
		headingReady: 'Send a secret',
		intro: 'Only the requester can open what you send.',
		resumed:
			'The previous in-memory attempt was discarded when you left or reloaded. It may already have been accepted.',
		acceptedBody: 'Only the requester can open it.',
		expiredBody: 'Ask the requester for a new link.',
		unavailableBody:
			'This link no longer accepts submissions. If you still need to send something, ask the requester for a new link.',
		outcomeRegion: 'Submission outcome',
		outcomeUnknown: 'Submission outcome unknown.',
		outcomeHint:
			'Keep this tab open and check the submission before retrying. A retry resends the same encrypted content; leaving or reloading discards it.',
		reservationRemaining: (minutes: number, seconds: number) =>
			`Upload reservation: ${minutes}m ${seconds}s remaining.`,
		checkSubmission: 'Check submission',
		cancelUpload: 'Cancel upload',
		retryCancellation: 'Retry cancellation',
		cancelHint:
			'Cancellation must be confirmed by the server before another attempt. Stopping the transfer does not delete uploaded bytes; server cleanup handles them.',
		retryReady: 'The server has not accepted this attempt. You can retry the same submission.',
		retrySame: 'Retry same submission',
		requesterNote: 'Note from requester',
		messageLabel: 'Message',
		fileLabel: 'File',
		fileLimit: (size: string) => `One file, up to ${size}.`,
		encrypting: 'Encrypting…',
		submit: 'Encrypt and submit',
		loading: 'Loading request…',
		retryLoading: 'Retry loading request',
		progress: {
			cancelling: 'Cancelling upload…',
			reserving: 'Reserving encrypted upload…',
			verifying: 'Verifying upload… Acceptance is not confirmed yet.',
			uploadingBytes: (loaded: string, total: string, percent: number) =>
				`Uploading encrypted file: ${loaded} of ${total} bytes (${percent}%). Acceptance requires verification.`,
			uploading: 'Uploading encrypted file…',
			submitting: 'Submitting encrypted content…'
		},
		errors: {
			reservationExpired:
				'The upload reservation expired. Check the attempt before choosing another file.',
			keyMismatch:
				'The request key does not match this link. Ask the requester for a new submission link.',
			loadFailed: 'Could not load the request. Try again.',
			noteMismatch:
				'The request note does not match this link. Ask the requester for the complete submission link.',
			noteDecryptFailed:
				'Could not decrypt the requester note. Ask the requester for the complete submission link.',
			submitUnconfirmed: 'Submission was not confirmed.',
			fillField: 'Fill in at least one field.',
			sizeRange: (max: string) => `Choose content between 1 and ${max} bytes.`,
			encryptFailed:
				'Could not encrypt the content. Check the file name and size, and use a current browser over HTTPS or localhost.',
			verifyFailed: 'Could not verify the submission.',
			noWebCrypto: 'Request encryption requires a browser with Web Crypto over HTTPS or localhost.',
			invalidLink: 'Invalid submission link. Ask the requester for the complete link.'
		},
		notices: {
			acceptedBeforeCancel:
				'The server accepted the submission before cancellation. Only the requester can cancel accepted content.',
			attemptRenewed: 'The previous upload attempt ended. Choose a file to start a new attempt.',
			cancelNotConfirmed:
				'Cancellation is not confirmed. Retry cancellation with the same attempt.',
			instructionExpired:
				'The upload instruction expired. Cancel this attempt before choosing another file.',
			cancelUnconfirmed:
				'Cancellation was not confirmed. Keep this tab open and check the attempt. Stopping a transfer does not delete uploaded bytes.'
		}
	},
	receive: {
		pageTitle: 'Private retrieval - Flick',
		badge: 'private requester page',
		headingExpired: 'Request expired',
		headingUnavailable: 'Request unavailable',
		headingOpened: 'Opened once',
		headingOpenLost: 'Open outcome unknown',
		headingDecryptFailed: 'Could not decrypt',
		headingReady: 'Ready to retrieve',
		headingConsumed: 'Consumed',
		headingCancelled: 'Cancelled',
		headingWaiting: 'Waiting for a submission',
		headingStatus: 'Request status',
		statusRegion: 'Request status',
		expiredBody: 'The request and anything shown here are no longer available.',
		unavailableBody: 'The link is invalid or expired. Use the complete private retrieval link.',
		lastKnown: 'Last known status: ',
		uploading: 'A file is uploading. It cannot be opened yet.',
		waiting: 'Waiting for the first submission.',
		accepted: 'A submission was accepted.',
		openOnce: 'You can open it once.',
		cancelled: 'Nothing more can be submitted or opened.',
		saveNow: 'Save it now. Leaving, refreshing, or reaching the deadline clears it for good.',
		consumed: 'It was released once and cannot be opened again.',
		checking: 'Checking automatically',
		loadingStatus: 'Loading request status…',
		refresh: 'Refresh status',
		openLost:
			'The open response was lost or invalid. The server may already have consumed the content, and this page will not retry.',
		decryptFailed:
			'The content was released, but decryption failed. It cannot be opened again. Ask the sender to use a new request.',
		decryptedSecret: 'Decrypted secret',
		download: (filename: string) => `Download ${filename}`,
		working: 'Working…',
		open: 'Open once',
		shareHeading: 'Share the submission link',
		shareHint: 'Anyone with this link can submit once. It cannot open or cancel the request.',
		submissionLink: 'Submission link',
		copySubmissionLink: 'Copy submission link',
		showSubmissionQr: 'Show submission QR',
		submissionGone: 'Use the submission link you saved; it cannot be shown here again.',
		privateHeading: 'Keep your retrieval link private',
		privateBody:
			'This link holds your decryption key. Save it to come back here or on another device; without it, the submission cannot be opened. Anyone with it can open or cancel the request. Browser history, sync, clipboard managers, and screenshots may keep a copy.',
		privateLink: 'Private retrieval link',
		copyPrivateLink: 'Copy private retrieval link',
		cancelRequest: 'Cancel request',
		createAnother: 'Create another request',
		confirmTitle: 'Cancel this request?',
		confirmBody:
			'Nothing more can be submitted or opened. Already released copies cannot be recalled.',
		errors: {
			missingKey:
				'Missing private key. Reopen the complete private retrieval link. Flick cannot recover a lost key or decrypt without it.',
			invalidKey:
				'Invalid private key. Reopen the original complete private retrieval link. Nothing was opened; Flick cannot recover a missing or damaged key.',
			statusFailed: 'Could not verify request status. Try again.',
			limitsFailed:
				'Could not verify size limits. Refresh status before opening. Nothing has been opened.'
		},
		notices: {
			changedBeforeCancel:
				'The request changed before cancellation. The current server status is shown.',
			notReleased:
				'The server did not release content for this opening. The request may have changed in another tab.'
		}
	},
	// Messages the request API client and upload state throw; the pages show them as-is.
	api: {
		invalidResponse: 'Could not verify the server response.',
		linkUnavailable: 'Request link unavailable.',
		offline: 'Offline or connection interrupted. Check your connection.',
		unavailable: 'Request unavailable. The link may be invalid or expired.',
		conflict: 'The request changed. Refresh its status.',
		tooLarge: 'The content exceeds the supported size limit.',
		rateLimited: 'Too many requests. Wait a moment before trying again.',
		storageFull: 'Object storage is at capacity. Try again later.',
		serverUnavailable: 'The server is temporarily unavailable. Try again later.',
		reservationExpired:
			'The upload reservation expired. Check the attempt or cancel it before choosing another file.'
	}
};
