# Public sharing guides

`/guides/password-sharing/` and `/guides/temporary-file-sharing/` are
prerendered, first-party pages in `web/src/routes/guides/`. The tool homepage
remains the create form. Every guide links directly to the tool and the other
guide; no advertising provider, interstitial, or new runtime dependency is used.

`GuidePage.svelte` reuses Flick's existing Inter body type, Instrument Serif
headings, teal primary color, and theme control. Articles use a narrow reading
column and numbered steps only for the sender sequence. Titles, descriptions,
and canonical paths are guide-specific. Open Graph remains the existing
brand-only preview from `app.html`, with no user content or link identifiers.

Canonical links use fixed guide paths with trailing slashes; SvelteKit's base
path resolves them against the serving self-hosted origin. They never read query strings or
fragments, and do not bake the demo or a staging origin into a shared image.
No XML sitemap is shipped for these two internally linked pages; this is a
deliberate small-site choice, not a dynamic enumeration of delivery URLs.
An operator adding an absolute-origin sitemap must list only the homepage and
these two guide paths. No `/s/`, `/m/`, `/r/`, or API path is public inventory.

`web/static/robots.txt` excludes private routes from crawling.
`web/nginx.conf` also sends `X-Robots-Tag: noindex, nofollow` for every document
outside the public allowlist, including the static SPA fallback before hydration.
These are crawler directives, not authorization or a guarantee that other sites
cannot publish a shared URL. Keys/tokens must still stay out of paths/queries.

Follow the M10 [advertising boundary](advertising-experiment.md): no third-party scripts on
the app origin. No guide analytics or sponsor is introduced by #209. Any later
network-ad site needs a separate origin and a separate implementation review.

Run `pnpm --dir web test:guides` with `FLICK_WEB_URL` pointing to the built nginx
web app and a real same-origin API. The suite checks two viewport widths,
canonical URLs, crawl headers, no remote guide requests, and a synthetic
create/open/reopen flow. An isolated local NATS/API fixture is sufficient;
the guide test uses inline text and does not require an object store.
