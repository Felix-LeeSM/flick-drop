<script lang="ts">
import GuidePage from '$lib/components/GuidePage.svelte';
</script>

<GuidePage
	title="Send a file, then close the handoff."
	description="Share a temporary document through one encrypted, expiring link. The recipient downloads once, and Flick stops serving the delivery after it is consumed."
	path="/guides/temporary-file-sharing/"
>
	<p>A colleague needs a temporary configuration file, a sample export, or a document for one task. Flick gives that handoff a deadline without creating a shared folder or a lasting file library. Check the recipient and remove unnecessary information before sending.</p>

	<h2>Prepare and send the file</h2>
	<ol>
		<li>Open Flick and choose File. Select or drop a file. Selecting several files creates one ZIP archive in your browser, so the recipient receives the bundle in a single opening.</li>
		<li>Check the size limit shown by the tool. Each self-hosted instance can have different limits; large-file sharing is available only when that instance has object storage enabled.</li>
		<li>Choose a short expiry and decide whether to require a separate passphrase. Your browser encrypts the file and its filename before upload. Leave the page open until creation succeeds.</li>
		<li>Copy the recipient link or use its QR code. Tell the recipient to open it when they are ready to download. Send a passphrase through a separate channel if you enabled protection.</li>
	</ol>

	<h2>Two ways to unlock the download</h2>
	<p>With a link-key share, the full URL includes the decryption key after <code>#key=</code>. The browser does not send that fragment to Flick's API. Anyone holding the full link can open the delivery, so do not post it in a public channel or remove the fragment when copying it.</p>
	<p>A passphrase-protected share requires both the link and a passphrase. Keep the passphrase distinct from anything inside the file and deliver it separately. Flick's server checks a separate access proof without receiving the passphrase or file-decryption key.</p>

	<h2>Receive and save it while the page is open</h2>
	<p>The recipient opens the link, enters the passphrase if required, and chooses Open. Flick releases the encrypted payload once. The browser decrypts it and offers a download with the decrypted filename; a bundle downloads as a ZIP.</p>
	<p>Save the file before leaving or refreshing the page. A consumed or expired link cannot provide another download, and a connection failure after the server consumes the delivery can require a new link. Five invalid passphrase attempts also lock the delivery.</p>

	<aside>
		<p><strong>Expiry stops future access through Flick.</strong> Expiry cannot remove a file the recipient already downloaded or stop the recipient from copying it elsewhere.</p>
	</aside>

	<h2>Use a temporary link for a temporary task</h2>
	<p>Use synthetic files such as <code>sample-config.txt</code> when trying the tool. Flick does not inspect plaintext, scan a decrypted file for malware, or verify a sender's identity. Only open files from people you trust.</p>
	<p>Deletion is asynchronous and storage backups may retain ciphertext. Flick does not promise physical erasure, link recovery, or permanent storage. For repeated access or collaboration, use a file-sharing service designed for that purpose.</p>
</GuidePage>
