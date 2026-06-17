<script lang="ts">
	import { decryptText, encryptText, type EncryptedTextPayload } from '$lib/crypto/text';

	let plaintext = $state('');
	let passphrase = $state('');
	let encryptedJSON = $state('');
	let decryptPassphrase = $state('');
	let decryptedText = $state('');
	let status = $state('');

	async function encryptLocal() {
		status = '';
		decryptedText = '';

		try {
			const encrypted = await encryptText(plaintext, passphrase);
			encryptedJSON = JSON.stringify(encrypted, null, 2);
			status = 'Encrypted';
		} catch (error) {
			status = messageFrom(error);
		}
	}

	async function decryptLocal() {
		status = '';
		decryptedText = '';

		try {
			const payload = JSON.parse(encryptedJSON) as EncryptedTextPayload;
			decryptedText = await decryptText(payload, decryptPassphrase);
			status = 'Decrypted';
		} catch (error) {
			status = messageFrom(error);
		}
	}

	function messageFrom(error: unknown) {
		return error instanceof Error ? error.message : 'Request failed';
	}
</script>

<svelte:head>
	<title>BurnLink</title>
	<meta
		name="description"
		content="BurnLink creates one-time encrypted secret links for self-hosted deployments."
	/>
</svelte:head>

<main class="shell">
	<header class="topbar">
		<div>
			<p class="eyebrow">BurnLink</p>
			<h1>One-time secret drop</h1>
		</div>
		<p class="status" aria-live="polite">{status}</p>
	</header>

	<section class="workspace" aria-label="Secret workspace">
		<form class="panel" onsubmit={(event) => event.preventDefault()}>
			<label for="plaintext">Secret text</label>
			<textarea id="plaintext" bind:value={plaintext} spellcheck="false"></textarea>

			<label for="passphrase">Passphrase</label>
			<input id="passphrase" type="password" autocomplete="new-password" bind:value={passphrase} />

			<button type="button" onclick={encryptLocal}>Encrypt</button>
		</form>

		<form class="panel" onsubmit={(event) => event.preventDefault()}>
			<label for="encrypted">Encrypted payload</label>
			<textarea id="encrypted" bind:value={encryptedJSON} spellcheck="false"></textarea>

			<label for="decrypt-passphrase">Passphrase</label>
			<input
				id="decrypt-passphrase"
				type="password"
				autocomplete="new-password"
				bind:value={decryptPassphrase}
			/>

			<button type="button" onclick={decryptLocal}>Decrypt</button>
		</form>
	</section>

	<section class="output" aria-label="Decrypted output">
		<label for="decrypted">Decrypted text</label>
		<textarea id="decrypted" value={decryptedText} readonly spellcheck="false"></textarea>
	</section>
</main>

<style>
	:global(body) {
		margin: 0;
		color: #18211f;
		background: #f7f8f5;
		font-family:
			Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
	}

	.shell {
		box-sizing: border-box;
		min-height: 100vh;
		padding: 32px;
	}

	.topbar {
		display: flex;
		align-items: end;
		justify-content: space-between;
		gap: 24px;
		margin: 0 auto 28px;
		max-width: 1120px;
	}

	.eyebrow {
		margin: 0 0 6px;
		color: #365d54;
		font-size: 0.78rem;
		font-weight: 700;
		letter-spacing: 0;
		text-transform: uppercase;
	}

	h1 {
		margin: 0;
		font-size: clamp(2rem, 4vw, 4rem);
		line-height: 1;
		letter-spacing: 0;
	}

	.status {
		min-width: 9rem;
		margin: 0;
		color: #5a4630;
		font-weight: 700;
		text-align: right;
	}

	.workspace {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 18px;
		max-width: 1120px;
		margin: 0 auto;
	}

	.panel,
	.output {
		box-sizing: border-box;
		border: 1px solid #d8ddd5;
		border-radius: 8px;
		background: #ffffff;
	}

	.panel {
		display: grid;
		grid-template-rows: auto minmax(220px, 1fr) auto auto auto;
		gap: 10px;
		min-height: 420px;
		padding: 18px;
	}

	.output {
		display: grid;
		gap: 10px;
		max-width: 1120px;
		margin: 18px auto 0;
		padding: 18px;
	}

	label {
		color: #24332f;
		font-size: 0.9rem;
		font-weight: 700;
	}

	textarea,
	input {
		box-sizing: border-box;
		width: 100%;
		border: 1px solid #bac4bd;
		border-radius: 6px;
		background: #fbfcfa;
		color: #18211f;
		font: inherit;
	}

	textarea {
		min-height: 180px;
		padding: 12px;
		resize: vertical;
	}

	input {
		min-height: 42px;
		padding: 0 12px;
	}

	button {
		justify-self: start;
		min-height: 42px;
		border: 0;
		border-radius: 6px;
		background: #1f6b57;
		color: #ffffff;
		font: inherit;
		font-weight: 800;
		padding: 0 18px;
	}

	button:focus-visible,
	textarea:focus-visible,
	input:focus-visible {
		outline: 3px solid #c7902e;
		outline-offset: 2px;
	}

	@media (max-width: 760px) {
		.shell {
			padding: 20px;
		}

		.topbar {
			align-items: start;
			flex-direction: column;
		}

		.status {
			text-align: left;
		}

		.workspace {
			grid-template-columns: 1fr;
		}
	}
</style>
