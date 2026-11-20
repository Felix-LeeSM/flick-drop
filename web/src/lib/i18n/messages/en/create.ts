export const create = {
	headTitle: 'Create secret - Flick',
	tagline: 'end-to-end encrypted · burns after read',
	// Page titles (h1) keep English in every language.
	titleLink: 'Create a secret',
	titleCode: 'Send a secret',
	receivedCode: 'Received a 6-digit code?',
	enterItHere: 'Enter it here',
	needOne: 'Need one from someone else?',
	requestSecret: 'Request a secret',
	typeTablist: 'Secret type',
	types: {
		text: 'Note',
		file: 'File',
		login: 'Login',
		card: 'Card',
		identity: 'Identity',
		custom: 'Custom'
	},
	text: {
		label: 'Message',
		placeholder: 'Paste text'
	},
	files: {
		label: 'Files',
		dropLead: 'Drop files',
		dropRest: ' or click to browse',
		limitLead: 'Multiple files and folders are zipped into one · up to ',
		addFiles: 'Add files to upload',
		chooseFolder: 'Choose a folder',
		removeFolder: (name: string) => `Remove folder ${name}`,
		removeFile: (path: string) => `Remove ${path}`,
		count: (n: number) => (n === 1 ? '1 file' : `${n} files`),
		zipping: (n: number) => `Zipping ${n === 1 ? '1 file' : `${n} files`}…`,
		tooLarge: (size: string) => `Choose files up to ${size} total.`
	},
	passphrase: {
		toggle: 'Protect with a passphrase',
		infoLabel: "Why the server can't see this secret",
		whyTitle: "Why we can't read your secret",
		whyLocked: 'It is locked on your device before sending.',
		whyNeverSent: 'We never receive the passphrase.',
		placeholder: 'Add a passphrase',
		fieldLabel: 'Passphrase',
		generate: 'Generate passphrase',
		hide: 'Hide passphrase',
		show: 'Show passphrase'
	},
	expiresAfter: 'Expires after',
	lifetimeGroup: 'Secret lifetime',
	// Primary action buttons keep English in every language.
	cta: {
		creating: 'Creating',
		createLink: 'Create link',
		startLive: 'Start live transfer'
	},
	linkCaption: 'burns after a single open',
	liveCaption: 'never stored on the server',
	blocked: {
		passphrase: 'Set a passphrase to finish protecting this secret',
		empty: 'Add a message, file or credential first'
	},
	stage: {
		caption: 'preparing your link',
		ready: 'Link ready'
	},
	status: {
		encrypting: 'Encrypting in your browser…',
		creatingLink: 'Creating link…',
		preparingUpload: 'Preparing encrypted upload…',
		finalizing: 'Finalizing link…',
		uploading: 'Uploading encrypted file…',
		uploadingProgress: (loaded: string, total: string, percent: number) =>
			`Uploading encrypted file · ${loaded} of ${total} (${percent}%)`,
		uploadCancelled: 'Upload cancelled.',
		createFailed: 'Could not create link. Try again.',
		zipFailed: 'Could not zip those files. Try again.',
		folderEmpty: 'That folder is empty.',
		folderReadFailed: 'Could not read that folder.',
		skipped: (n: number) => `Skipped ${n === 1 ? '1 item' : `${n} items`} that could not be read.`
	},
	result: {
		oneTimeLink: 'one-time link',
		title: 'Link created',
		opensOnce: 'Opens once, then burns. Forward it fast.',
		expiresIn: 'EXPIRES IN',
		openManagement: 'Open private management page',
		showQr: 'Show QR',
		passphraseTitle: 'Send the passphrase separately.',
		passphraseBody: 'The recipient needs both the link and the passphrase to open this.',
		linkOnlyTitle: 'Share only with your recipient.',
		linkOnlyBody: 'Anyone with this full link can open it once.',
		createAnother: 'Create another'
	},
	guideNav: 'Guide',
	howItWorks: 'How Flick works'
};
