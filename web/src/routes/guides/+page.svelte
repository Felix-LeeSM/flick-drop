<script lang="ts">
import SiteHeader from '#lib/components/SiteHeader.svelte';
import SponsorSlot from '#lib/components/SponsorSlot.svelte';
import { resolve } from '$app/paths';

const title = 'How Flick works';
const description =
	'Everyday answers first, then how the server stays unable to read what you send, where the code lives, and how to run your own copy.';
const repo = 'https://github.com/Felix-LeeSM/flick-drop';

// The table of contents follows the page: everyday questions first, then the
// developer depth. Old guide URLs redirect to #sending (web/nginx.conf).
const sections = [
	{ id: 'sending', label: 'Sending and opening' },
	{ id: 'limits', label: 'What Flick cannot do' },
	{ id: 'privacy', label: 'Why the server cannot read it' },
	{ id: 'source', label: 'Source code' },
	{ id: 'self-host', label: 'Running your own Flick' }
];
</script>

<svelte:head>
	<title>{title} - Flick</title>
	<meta name="description" content={description} />
	<link rel="canonical" href={resolve('guides/')} />
</svelte:head>

<!-- The guide is English only; lang keeps screen readers on English whatever the display language. -->
<div lang="en" class="mx-auto grid max-w-3xl grid-cols-[minmax(0,1fr)] px-5 py-6 text-foreground sm:px-8 sm:py-10">
	<SiteHeader />
	<main class="py-12 sm:py-20">
		<h1 class="max-w-2xl font-serif text-5xl leading-[1.06] sm:text-6xl">{title}</h1>
		<p class="mt-6 max-w-xl text-lg leading-relaxed text-muted-foreground">{description}</p>
		<a href={resolve('/')} class="mt-8 inline-flex min-h-11 items-center rounded-lg bg-primary px-5 py-3 text-sm font-medium text-primary-foreground focus-visible:outline-2 focus-visible:outline-offset-4">
			Create a one-time link
		</a>

		<nav aria-label="On this page" class="mt-12 border-l-2 pl-5 text-sm">
			<p class="text-xs font-medium uppercase tracking-widest text-muted-foreground">On this page</p>
			<ol class="mt-2">
				{#each sections as section (section.id)}
					<li><a href={`#${section.id}`} class="inline-block py-1.5 underline-offset-4 hover:underline">{section.label}</a></li>
				{/each}
			</ol>
		</nav>

		<article class="guide-copy mt-6 max-w-prose leading-7">
			<h2 id="sending">Sending and opening</h2>

			<h3>What is Flick for?</h3>
			<p>Handing over something sensitive once: a temporary password, a Wi-Fi key, an API token, a contract, a log file. You get a link that opens one time and then stops working. Flick is not a password manager or a file library; nothing stays around after the handoff.</p>

			<h3>Can the people running Flick read what I send?</h3>
			<p>Not with the code Flick publishes. Your browser encrypts the content before anything leaves your device, and the key to decrypt it never reaches the server. The server stores locked bytes it cannot open. <a href="#privacy">The developer section</a> explains how, and where that promise ends.</p>

			<h3>How do I send something?</h3>
			<ol>
				<li>Choose what to send: a <strong>Note</strong>, one or more <strong>Files</strong> (several files or a folder are zipped into one bundle), or a template for a <strong>Login</strong>, <strong>Card</strong>, <strong>Identity</strong>, or <strong>Custom</strong> set of fields.</li>
				<li>Pick how long it may wait: 10 minutes, 1 hour, 24 hours, 7 days, or a custom time between 5 minutes and 7 days.</li>
				<li>Optionally set a passphrase.</li>
				<li>Choose <strong>Create link</strong> and send the whole link to your recipient. The part after <code>#</code> is the key; a link cut short cannot be opened.</li>
			</ol>

			<h3>What does the recipient see?</h3>
			<p>Opening the link shows a page with an <strong>Open</strong> button; loading that page uses nothing up. Pressing Open releases the content once and deletes it from the server. Reloading afterwards shows that it is no longer available, so the recipient should copy or save what they need right away.</p>

			<h3>What if nobody opens it?</h3>
			<p>It is deleted when its time runs out. Either way, a link works once at most.</p>

			<h3>When should I add a passphrase?</h3>
			<p>When the link might be seen by someone else, for example in a shared chat. With a passphrase, the link alone is not enough. Send the passphrase another way, such as a phone call or a different messenger. Five wrong passphrases lock the delivery for good.</p>

			<h3>What is live transfer?</h3>
			<p><strong>Live transfer</strong> moves content while both devices are open. The sender gets a 6-digit code, and the recipient types it into <strong>Enter Code</strong> within 3 minutes. Each waiting device shows a 3-digit number; the recipient reads it out so the sender picks the right device. Nothing is written to the database or file storage: the content passes through the server only while it streams.</p>

			<h3>Can I ask someone to send something to me?</h3>
			<p>Yes. <a href={resolve('/request')}>Request a secret</a> creates two links: one you give to the other person to submit a note, a file, or credentials, and a private one you keep to open their answer once. The first submission wins.</p>

			<h3>What if the link leaks?</h3>
			<p>Anyone holding the complete link can open it once. If your recipient finds it already opened, treat the content as exposed and change it. The sender can also cancel an unopened delivery from its private management link.</p>

			<h2 id="limits">What Flick cannot do</h2>
			<p>Flick cannot take back what the recipient already saw. They can copy it, screenshot it, or keep the file. A one-time link does not make a password one-time: change temporary credentials when their job is done.</p>
			<p>Flick also does not prove who opened a link, and it cannot protect content on a device that is already compromised. The page that encrypts your content comes from the Flick server you use, so use an instance whose operator you trust, or <a href="#self-host">run your own</a>. Copies can also remain in chat histories, the clipboard, or browser history.</p>

			<p class="guide-divider">For developers and operators</p>

			<h2 id="privacy">Why the server cannot read it</h2>
			<p>Content is encrypted in the browser with <a href="https://age-encryption.org/">age</a>, an open file-encryption format, using a fresh post-quantum hybrid key (ML-KEM-768 with X25519) for every delivery. Large files are encrypted in 64 KiB chunks, and nothing is shown until the last chunk verifies.</p>
			<p>Without a passphrase, the key travels in the link fragment, <code>#key=AGE-SECRET-KEY-PQ-1…</code>. Browsers never send the fragment to a server, so it stays out of requests and access logs.</p>
			<p>With a passphrase, the key is wrapped with age's scrypt mode (work factor 18) and only the wrapped copy is uploaded. The passphrase also derives a separate access proof (PBKDF2-SHA-256, 600,000 iterations); the server keeps only a hash of that proof and checks it before releasing anything. Someone holding a database copy could still guess passphrases offline against the wrapped key, so use a long one; the generator makes 80-bit passphrases.</p>
			<p>The server stores an ID, the encrypted bytes, their size, the file type, the encrypted file name, timestamps, and the wrong-passphrase count. It stores no IP address. The web access log masks delivery IDs as <code>&lt;id&gt;</code> and records no client IP, referrer, or query string.</p>
			<p>Live transfer works differently: the recipient's browser makes a one-time key pair and sends only the public half. The sender encrypts to it, and the server relays ciphertext from memory. Waiting-room details such as the hashed code and device labels live briefly in a non-persistent cache.</p>
			<aside>
				<p><strong>The honest limit of any web encryption:</strong> you trust the JavaScript the server sends. A server that is actively malicious could serve code that steals a key or passphrase; for live transfer it could also swap in its own key. The design protects against leaks of the database, logs, and backups, not against the operator turning hostile. The command-line client, verified against <code>SHA256SUMS</code>, removes that trust in served code.</p>
			</aside>

			<h2 id="source">Source code</h2>
			<p>Flick is open source under the Apache 2.0 license: <a href={repo}>github.com/Felix-LeeSM/flick-drop</a>. Good places to start:</p>
			<ul>
				<li><code>web/src/lib/crypto/</code>: browser encryption, the link key, and the passphrase proof.</li>
				<li><code>contracts/age-format-v1.md</code>: the encrypted format and what each API call carries.</li>
				<li><code>docs/architecture/security-model.md</code>: the full threat model, including what is out of scope.</li>
				<li><code>cmd/flick</code>: the command-line client, which encrypts the same way as the browser.</li>
			</ul>
			<p>The command-line client sends and opens deliveries from a terminal:</p>
			<pre><code>go install github.com/Felix-LeeSM/flick-drop/cmd/flick@latest
flick send -ttl 1h -file report.pdf
flick open 'https://flick.example/s/…#key=…'</code></pre>
			<p>Release builds for macOS, Linux, and Windows are attached to each <code>cli/v*</code> release with a <code>SHA256SUMS</code> file. Set <code>FLICK_URL</code> to the instance you use.</p>

			<h2 id="self-host">Running your own Flick</h2>
			<p>Flick is built to run on a small machine you control. A deployment has a web front end (nginx serving the built SvelteKit app), the Go API, the Go cleanup worker, NATS JetStream, and PostgreSQL 17. Valkey is optional, but live transfer needs it. An S3-compatible bucket is optional too; without one, files are limited to the 1 MiB that fits in the database.</p>
			<p>Images are published as <code>docker.io/haradwaith/flick-api</code>, <code>flick-worker</code>, and <code>flick-web</code>, tagged by version or commit (there is no <code>latest</code>). You can also build them from source.</p>
			<p>The repository ships Kubernetes manifests in <code>deploy/base</code>, with step-by-step runbooks for a k3s cluster (<code>docs/runbook/k3s-base.md</code>) and OCI Always Free resources (<code>docs/runbook/oci-free-tier.md</code>). Every setting is listed in <code>.env.example</code> and <code>docs/architecture/env-contract.md</code>.</p>
		</article>
		<SponsorSlot />
	</main>

	<nav aria-label="Flick" class="flex flex-wrap gap-x-6 gap-y-2 border-t pt-5 text-sm text-muted-foreground">
		<a href={resolve('/')} class="py-2 underline underline-offset-4">Open Flick</a>
		<a href={repo} class="py-2 underline underline-offset-4">Source code</a>
	</nav>
</div>

<style>
.guide-copy :global(h2) { margin: 3rem 0 0.8rem; font-size: 1.5rem; font-weight: 600; line-height: 1.35; scroll-margin-top: 1.5rem; }
.guide-copy :global(h3) { margin: 1.75rem 0 0.4rem; font-size: 1.05rem; font-weight: 600; line-height: 1.45; }
.guide-copy :global(p) { margin: 0.6rem 0; }
.guide-copy :global(ol) { list-style: decimal; padding-left: 1.4rem; }
.guide-copy :global(ul) { list-style: disc; padding-left: 1.4rem; }
.guide-copy :global(li) { padding-left: 0.4rem; margin: 0.5rem 0; }
.guide-copy :global(a) { text-decoration: underline; text-underline-offset: 4px; overflow-wrap: anywhere; }
.guide-copy :global(aside) { margin: 1.75rem 0; border-left: 3px solid var(--primary); padding: 0.2rem 0 0.2rem 1.25rem; }
.guide-copy :global(code) { overflow-wrap: anywhere; font-size: 0.9em; }
.guide-copy :global(pre) { margin: 1rem 0; overflow-x: auto; border-radius: 0.5rem; background: var(--muted); padding: 1rem; font-size: 0.875rem; line-height: 1.6; }
.guide-copy :global(pre code) { overflow-wrap: normal; white-space: pre; }
.guide-copy :global(.guide-divider) { margin: 4rem 0 0; border-top: 1px solid var(--border); padding-top: 1rem; font-size: 0.75rem; font-weight: 500; letter-spacing: 0.1em; text-transform: uppercase; color: var(--muted-foreground); }
</style>
