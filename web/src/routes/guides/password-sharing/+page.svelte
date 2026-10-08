<script lang="ts">
import GuidePage from '$lib/components/GuidePage.svelte';
</script>

<GuidePage
	title="Share a password for one moment."
	description="Send a temporary credential without leaving its readable value in a chat history. Flick encrypts it in your browser and allows one opening before it expires."
	path="/guides/password-sharing/"
>
	<p>Someone needs a temporary login, a Wi-Fi password, or a test API credential. Instead of putting the value directly into a conversation, create a short-lived encrypted link and send that link. A recipient still needs to trust the person who sent it.</p>

	<h2>Choose how the recipient will unlock it</h2>
	<p>A link-key share carries the decryption key in the part of the URL after <code>#key=</code>. Send the complete link. Anyone who gets that link can open it, so treat it like the credential itself. The browser keeps the key out of requests to Flick's server.</p>
	<p>For a passphrase-protected share, the link and passphrase are separate. Use a strong, distinct passphrase and send it through a different channel, such as a phone call to someone you already know. Sending both in the same chat gives anyone with access to that chat both pieces.</p>

	<h2>Send the credential</h2>
	<ol>
		<li>Open Flick and choose Text, or choose a credential template such as Login. Enter only what the recipient needs. An example label might be <code>Temporary staging login</code>; never use a real credential in a public example.</li>
		<li>Choose an expiry that leaves enough time for the recipient to open the link. Enable passphrase protection if you will send a separate passphrase.</li>
		<li>Create the link. Your browser encrypts the content before upload. Copy the recipient link, or display its QR code for the intended recipient.</li>
		<li>Send the complete link to the recipient. If you chose passphrase protection, send the passphrase separately. Ask the recipient to open the link when ready to use the credential.</li>
	</ol>

	<h2>What the recipient should expect</h2>
	<p>Opening the web page shows the delivery screen; the recipient then chooses Open. A passphrase-protected delivery asks for the passphrase. A successful one-time release consumes the delivery, even if the connection drops before the recipient sees the result. Reloading does not recover it.</p>
	<p>Five invalid passphrase attempts lock the delivery. If a link has expired, has already been consumed, or can no longer be opened, ask the sender to create a new delivery. Flick does not recover a lost link or a forgotten passphrase.</p>

	<aside>
		<p><strong>A one-time link does not make a credential one-time.</strong> The recipient can copy or save the revealed value. Revoke the temporary account or rotate the credential when its job is done.</p>
	</aside>

	<h2>Keep the limits in mind</h2>
	<p>Flick does not prove who opened a link, prevent screenshots, or erase copies from devices, chat histories, storage backups, or browser extensions. Use a trusted browser and a trusted Flick instance: the web server supplies the code that handles the secret.</p>
	<p>For passwords you need to keep using, use a password manager with an appropriate sharing workflow. Flick is for a short-lived handoff, with no permanent vault or account history.</p>
</GuidePage>
