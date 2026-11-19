// biome-ignore-all lint/style/useNamingConvention: request fixtures use canonical API field names.
// Generated per run; age output is randomized, so tests check round trips, not pinned bytes.
import { createHash } from 'node:crypto';
import { Encrypter, generateHybridIdentity, identityToRecipient } from 'age-encryption';

async function seal(recipient, plaintext) {
	const encrypter = new Encrypter();
	encrypter.addRecipient(recipient);
	return Buffer.from(await encrypter.encrypt(plaintext)).toString('base64');
}

export async function requestVectors() {
	const identity = await generateHybridIdentity();
	const publicKey = await identityToRecipient(identity);
	const text = 'Synthetic request text';
	const file = { plaintext: 'Synthetic request file', filename: 'synthetic-é.txt' };
	const textBody = await seal(publicKey, text);
	const fileBody = await seal(publicKey, file.plaintext);
	return {
		request_id: 'synthetic-request',
		public_key: publicKey,
		fingerprint: createHash('sha256').update(publicKey).digest('base64url'),
		identity,
		text: {
			plaintext: text,
			payload: {
				kind: 'text',
				size_bytes: Buffer.from(textBody, 'base64').length,
				envelope: { version: 2 },
				ciphertext: textBody
			}
		},
		file: {
			...file,
			payload: {
				kind: 'file',
				size_bytes: Buffer.from(fileBody, 'base64').length,
				envelope: { version: 2, encrypted_filename: await seal(publicKey, file.filename) },
				ciphertext: fileBody
			}
		}
	};
}

export function openResponse(payload) {
	const headers = {
		'Content-Type': 'application/octet-stream',
		// Mirrors the headers internal/httpapi/router.go exposes cross-origin.
		'Access-Control-Expose-Headers': 'Flick-Kind, Flick-Encrypted-Filename',
		'Flick-Kind': payload.kind
	};
	if (payload.envelope.encrypted_filename) {
		headers['Flick-Encrypted-Filename'] = payload.envelope.encrypted_filename;
	}
	return { headers, body: Buffer.from(payload.ciphertext, 'base64') };
}
