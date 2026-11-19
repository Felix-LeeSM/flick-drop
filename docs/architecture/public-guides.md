# Public guide

`/guides/` is one prerendered, first-party page in
`web/src/routes/guides/+page.svelte`. It reads from everyday questions to
developer depth: sending and opening, what Flick cannot do, why the server
cannot read content, where the source lives, and how to run an instance. The
tool homepage remains the create form; the create form links to the guide in a
new tab so an in-progress form survives (`web/src/lib/components/CreateSecretPage.svelte`).
The page renders `web/src/lib/components/SponsorSlot.svelte` after the
article, and the slot renders nothing while `SPONSOR` in
`web/src/lib/components/sponsor.ts` is `null`, the shipped default.

The earlier topic guides `/guides/password-sharing/` and
`/guides/temporary-file-sharing/` were merged into `/guides/`. `web/nginx.conf`
answers both old paths with a relative `301` to `/guides/#sending` so links
already shared keep working.

The guide reuses Flick's existing Inter body type, Instrument Serif
headings, teal primary color, and theme control. The article uses a narrow
reading column, an in-page table of contents, and numbered steps only for the
sender sequence. Open Graph remains the existing
brand-only preview from `app.html`, with no user content or link identifiers.

The canonical link uses the fixed path `/guides/` with its trailing slash; SvelteKit's base
path resolves it against the serving self-hosted origin. It never reads query strings or
fragments, and does not bake the demo or a staging origin into a shared image.
No XML sitemap is shipped for this internally linked page; this is a
deliberate small-site choice, not a dynamic enumeration of delivery URLs.
An operator adding an absolute-origin sitemap must list only the homepage and
`/guides/`. No `/s/`, `/m/`, `/r/`, or API path is public inventory.

`web/static/robots.txt` excludes private routes from crawling.
`web/nginx.conf` also sends `X-Robots-Tag: noindex, nofollow` for every document
outside the public allowlist, including the static SPA fallback before hydration.
These are crawler directives, not authorization or a guarantee that other sites
cannot publish a shared URL. Keys/tokens must still stay out of paths/queries.

Follow the M10 [advertising boundary](advertising-experiment.md): no third-party scripts on
the app origin. #209 added no guide analytics. #210 added the optional
first-party static sponsor slot (`SponsorSlot.svelte`, disabled by default);
its contract is the "Static sponsor contract" section of
[advertising-experiment.md](advertising-experiment.md). Any later network-ad
site needs a separate origin and a separate implementation review.

Run `pnpm --dir web test:guides` with `FLICK_WEB_URL` pointing to the built nginx
web app and a real same-origin API. The suite checks two viewport widths,
the canonical URL, crawl headers, table-of-contents targets, no remote guide
requests, the `301` from both retired guide paths, and a synthetic
create/open/reopen flow. An isolated local NATS/API fixture is sufficient;
the guide test uses inline text and does not require an object store.
