// biome-ignore-all lint/style/useNamingConvention: relay error keys are the API's snake_case error codes.
// Live transfer (send with code): the receiver's dialog, the sender's waiting
// room, the received view, and relay status and error messages.
export const live = {
	receive: {
		dialogTitle: 'Receive secret with code',
		dialogDescription:
			'Enter the 6-digit code to connect to the sender. Nothing is stored on the server; the content arrives encrypted.',
		close: 'Close',
		step: (step: number) => `STEP ${step} OF 2`,
		zeroDisk: '0B disk relay',
		headingConnect: 'Connect to sender',
		headingVerify: 'Read this to the sender',
		headingReceiving: 'Receiving',
		headingConnecting: 'Connecting',
		codeInputLabel: '6-digit transfer code',
		autoConnect: 'Auto-connects immediately when 6 digits are typed.',
		connect: 'Connect',
		nextHint: "Next, you'll read a 3-digit number to the sender to confirm.",
		invalidCode: 'Please enter a valid 6-digit number',
		connectingStatus: 'Connecting to the sender…',
		decryptingStatus: 'Opening on this device…',
		unlocked: 'Unlocked',
		pipe: (code: string) => `Pipe: ${code}`,
		cancel: 'Cancel',
		verifyHint:
			'Read this number to the sender out loud or in chat. They pick your device by this number.',
		yourNumber: (spaced: string) => `Your number ${spaced}`,
		waitingForPick: 'Waiting for the sender to pick…',
		notMine: 'Not my transfer — disconnect',
		declinedTitle: 'Connection declined',
		declinedDescription:
			'Nothing was sent to this device, and this code cannot be used again. Ask the sender for a new one.',
		declinedLabel: (code: string) => `Code ${code} · Declined`,
		declinedHeading: 'The sender declined this connection',
		declinedBody:
			"Nothing was sent to this device, and this code can't be used again. Ask the sender for a new one.",
		notChosenHeading: 'The sender chose another device',
		notChosenBody:
			'This device was disconnected and received nothing. If you were expecting a secret, ask the sender for a new code.',
		roomFullHeading: 'Too many devices are trying to connect',
		roomFullBody: 'Too many devices are trying to connect. Ask the sender for a new code.',
		enterNewCode: 'Enter a new code',
		fileDownloaded: 'File Downloaded!',
		fileSaved: 'Opened and saved directly to your device.',
		downloadAgain: 'Download again',
		done: 'Done',
		decryptedInMemory: 'Opened without being stored anywhere',
		burned: 'burned',
		decryptedContent: 'Received content',
		copy: 'Copy',
		copied: 'Copied!',
		failedHeading: 'Transfer Failed',
		failedFallback: 'Transfer failed',
		tryAgain: 'Try again'
	},
	waiting: {
		defaultStatus: 'Waiting for receiver...',
		stageLabel: 'Sending your secret',
		stageCaption: 'sending to receiver',
		stageCleared: 'Cleared after sending',
		stageStreaming: 'Streaming to receiver…',
		stageEncrypting: 'Encrypting in your browser…',
		completeLabel: 'transfer complete',
		completeTitle: 'Transfer complete',
		completeBody: 'Delivered directly from this tab with zero server storage.',
		sendAnother: 'Send another secret',
		endedLabel: 'transfer ended',
		failedTitle: 'Transfer failed',
		failedBody: 'The code no longer works. Nothing more will be sent.',
		startNew: 'Start new secret',
		pageTitle: 'Live transfer',
		codeLabel: 'Live transfer code',
		// The remaining time sits between these two in bold.
		expiresBefore: 'Expires in ',
		expiresAfter: '',
		// The header's "Enter Code" action sits between these two in bold.
		recipientHintBefore: 'Recipient clicks ',
		recipientHintAfter: ' in the header and enters:',
		codeAria: (code: string) => `Transfer code ${code}`,
		copied: 'Copied!',
		copyCode: 'Copy code',
		showQr: 'Show QR',
		cancel: 'Cancel live transfer',
		keepOpen: 'Keep this tab open.',
		keepOpenBody:
			'Nothing is saved on the server — your browser sends the secret live when the recipient connects.'
	},
	pending: {
		devicesWaiting: (count: number, capacity: number) => `${count} of ${capacity} devices waiting`,
		heading: 'Which number does your receiver see?',
		empty: 'Waiting for receiver to enter the code…',
		listLabel: 'Waiting devices',
		number: (spaced: string) => `Number ${spaced}`,
		device: (browser: string, platform: string) => `${browser} on ${platform}`,
		unknownBrowser: 'Unknown browser',
		connection: (index: number) => `Connection ${index}`,
		justNow: 'just now',
		secondsAgo: (seconds: number) => `${seconds}s ago`,
		minutesAgo: (minutes: number) => `${minutes}m ago`,
		deny: (index: number) => `Deny connection ${index}`,
		sendTo: (index: number, code: string) => `Send to connection ${index}, number ${code}`,
		send: 'Send',
		showFewer: 'Show fewer',
		showMore: (count: number) => `Show ${count} more`
	},
	received: {
		label: (code: string) => `Live transfer · Code ${code}`,
		title: 'Secret',
		body: 'Save this content before closing the tab. Nothing remains on the server.',
		copied: 'Copied',
		copyAll: 'Copy all',
		copySecret: 'Copy secret',
		done: 'Done',
		decryptedContent: 'Received content',
		decryptedInBrowser: (size: string) => `${size} · Opened on this device`,
		save: (name: string) => `Save ${name}`,
		saved: (name: string) => `Saved ${name}`
	},
	// Sender-side relay status (web/src/lib/state/pipe-relay.svelte.ts).
	relay: {
		initializing: 'Preparing the transfer...',
		waitingForCode: 'Waiting for receiver to enter code...',
		sendingChosen: 'Sending to the chosen device...',
		encrypting: 'Encrypting as it sends...',
		streaming: 'Streaming encrypted bytes directly to receiver...',
		complete: 'Transfer complete!',
		streamingFailed: 'Streaming failed',
		liveFailed: 'Live transfer failed',
		noLongerWaiting: 'That device is no longer waiting.',
		denyFailed: 'Could not deny that device. Try again.'
	},
	// Relay API errors (web/src/lib/crypto/pipe.ts). `reasons` is keyed by the
	// API's error code; the rest are fallbacks when the code is unknown.
	errors: {
		reasons: {
			not_found: 'Invalid or expired 6-digit code.',
			declined: 'The sender declined this device.',
			room_full: 'Too many devices are trying to connect. Ask the sender for a new code.',
			dropped: 'This device was disconnected while waiting. Enter the code again.',
			poll_in_progress:
				'This device is already waiting in another request. Wait a moment and try again.',
			network_full:
				'Too many devices on your network are already waiting for this code. Close one of them and try again.',
			already_connected: 'Another device is already receiving this transfer.',
			not_selected: 'The sender chose another device.',
			cancelled: 'The sender cancelled this code.',
			expired: 'This code expired.',
			closed: 'This transfer has ended.',
			streaming: 'The transfer is already streaming.',
			rate_limited: 'Too many attempts. Wait a minute and try again.',
			capacity_exceeded:
				'Server is at maximum capacity for live transfers. Please try again later.',
			service_unavailable: 'Live transfer is not available on this server right now.',
			receiver_disconnected: 'Receiver disconnected during live transfer.'
		},
		initFailed: 'Could not prepare the transfer',
		joinFailed: 'Join failed',
		statusFailed: 'Status check failed',
		waitFailed: 'Wait failed',
		actionFailed: 'The server could not complete this step',
		sendFailed: 'Send failed',
		receiveFailed: 'Receive failed',
		emptyBody: 'Server response body is empty'
	}
};
