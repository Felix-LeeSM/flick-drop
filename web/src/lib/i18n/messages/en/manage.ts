// Sender's management page (ManageSecretPage.svelte), its cancel dialog
// (ConfirmDialog.svelte), and the remaining-time chip (RemainingTime.svelte).
export const manage = {
	headTitle: 'Manage delivery',
	statuses: {
		pendingUpload: {
			title: 'Upload not finished',
			detail: 'The encrypted upload has not been finalized. You can cancel it.'
		},
		active: {
			title: 'Ready to open',
			detail: 'The recipient can open this delivery once before it expires.'
		},
		locked: {
			title: 'Locked',
			detail: 'Five incorrect access proofs exhausted access. This is not a confirmed opening.'
		},
		cancelled: {
			title: 'Cancelled',
			detail:
				'The recipient link can no longer open this delivery. Cancellation cannot recall copies already delivered.'
		},
		unavailable: {
			title: 'Unavailable',
			detail: 'The upload ended without an available delivery.'
		}
	},
	leavePrompt:
		"You haven't copied or shared the recipient link yet. It can't be shown again after you leave. Leave anyway?",
	changedBeforeCancel:
		'The delivery changed before cancellation. Its current status is shown below.',
	noLongerCancellable: 'This delivery can no longer be cancelled.',
	privateSenderPage: 'private sender page',
	titleCreated: 'Link created',
	titleManage: 'Manage delivery',
	intro: 'Check one delivery and cancel it before it is opened.',
	expired: 'Management link expired.',
	statusRegion: 'Delivery status',
	lastKnown: 'Last known status: ',
	loadingStatus: 'Loading delivery status…',
	unknownStatus: 'Delivery status is unknown.',
	refresh: 'Refresh status',
	sendToRecipient: 'Send to your recipient',
	copyRecipientLink: 'Copy recipient link',
	showQr: 'Show QR',
	sendPassphraseSeparately:
		'Send the passphrase separately. The recipient needs both the link and the passphrase.',
	anyoneWithLink: 'Anyone with this full link can open it once. Share only with your recipient.',
	recipientLinkOnlyOriginal:
		'The recipient link is available only in the original creation session. Use the copy you saved or create a new delivery.',
	keepPrivate: 'Keep this management link private',
	keepPrivateDetail:
		'Anyone with this link can check or cancel this delivery until it is opened or expires. Save it now; a lost link cannot be recovered. It cannot decrypt the content or recover the recipient link.',
	privateLinkLabel: 'Private management link',
	copyManagementLink: 'Copy management link',
	cancelDelivery: 'Cancel delivery',
	createAnother: 'Create another',
	confirmTitle: 'Cancel this delivery?',
	confirmDescription:
		'The recipient link will stop working. This cannot recall content already delivered.',
	keepIt: 'Keep it',
	// formatRemaining in web/src/lib/lifetime.ts writes "6d 23h"; each unit letter maps here.
	timeUnits: { d: 'd', h: 'h', m: 'm', s: 's' },
	remaining: (time: string) => `${time} remaining`
};
