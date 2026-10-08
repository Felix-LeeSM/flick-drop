# Advertising experiment

Status: accepted implementation plan for M10 (#208–#210); no advertiser,
ad-network approval, traffic baseline, or revenue result is established here.

Flick remains a free, ephemeral delivery tool. Advertising must not change
the encryption boundary or require a click before creating or opening a link.

## Surfaces and trust boundary

| Surface | Static sponsor | Third-party ad or analytics code |
| --- | --- | --- |
| Public password/file-sharing guides | Optional, reviewed, disabled by default | Prohibited on the app origin |
| Create, open, management, request submission/retrieval | None in M10 | Prohibited |
| Success, expired, unavailable, error screens | None | Prohibited |
| A future separate public-content origin | Separately reviewed | Requires a separate implementation and provider review |

The current `web/src/routes/+layout.svelte` is a shared SvelteKit SPA layout.
A script loaded on one route can survive client-side navigation into another
route and read browser-held secrets. Conditional rendering or removing the
script element does not revoke code that already ran. Never add ad-network
code to that layout or any app-origin route. The existing
`web/nginx-templates/csp.conf.template` must not gain ad hosts or weaker rules.
OWASP describes the privileges and disclosure risks of third-party scripts in
its [JavaScript management guide](https://cheatsheetseries.owasp.org/cheatsheets/Third_Party_Javascript_Management_Cheat_Sheet.html).

Any later ad-network guide site must use a separate origin and document, with
no shared service worker, sensitive storage, credentialed CORS, or secret
handoff. Its tool CTA opens the clean app origin using a normal navigation,
with no user-specific path, query, fragment, or referrer. An ad-network trial
is not enabled by shipping the first-party guides in #209.

## Static sponsor contract (#210)

Start with one optional text sponsor on public guides only. A repository-owned
configuration contains reviewed literal name, description, and HTTPS target;
the shipped default is disabled. No runtime remote configuration, injected
HTML, JavaScript, tracking pixel, iframe, impression beacon, redirect service,
or dynamically constructed link is permitted. A future image must be a reviewed
local static asset, never a remote creative fetch.

Label the slot `Sponsored`, keep it visually distinct from tool navigation,
and reserve its place in the layout so content does not shift under a click.
Use an ordinary accessible link with `rel="sponsored noopener noreferrer"`
and `referrerpolicy="no-referrer"`. Do not append the current URL or any
user-derived value. No click handler reads application state. The advertiser
receives a request only when the user chooses the link; the external site then
has its own privacy practices. Disable the configuration to remove the slot.

This direct sponsorship contract is separate from Google ad placement rules;
shipping the component does not accept an advertiser agreement.

The implemented configuration is `SPONSOR` in
`web/src/lib/components/sponsor.ts`. Its shipped value is `null`. To enable a
reviewed agreement, replace `null` with literal `name`, `description`, and `url`
strings, then rebuild and deploy the web image. The URL must be an absolute,
canonical HTTPS URL (including the trailing slash for a bare origin), without
credentials, query, or fragment. Invalid or missing fields render no placement.
Name and description render as escaped text, never HTML. Do not place user data
in any configuration field. No environment variable or remote service supplies
the creative; the operator owns review of the public copy and destination.
Set `SPONSOR` back to `null`, rebuild, and deploy to disable it.

`SponsorSlot.svelte` appears only after the article in `GuidePage.svelte`,
separated from its tool CTA and navigation. The placement is present at the
initial render when enabled, so no asynchronous creative changes the layout.
It opens the literal destination in a new tab without a referrer or opener.
No impression/click counter is implemented; these measures remain unmeasured.

## Publisher policy check

Official sources checked on 2026-10-08; re-check before a provider submission:

- Google excludes low-value, navigation, alert, and empty screens, including
  thank-you and error pages. Completion and expiry screens are therefore not
  planned inventory. See [screens without publisher content](https://support.google.com/publisherpolicies/answer/11112688?hl=en).
- Google restricts private-communication placements and layouts that encourage
  accidental ad clicks. Keep any future network ad on substantive public
  content, clearly separated from copy, open, and download controls. See
  [ad placement policies](https://support.google.com/adsense/answer/1346295?hl=en).

These are constraints, not an approval prediction. Never give a provider
secret links, decrypted examples, or access credentials to inspect the tool.
Use synthetic examples on the public guides.

## Measurement plan

The operator records daily aggregate totals, with no per-person attribution.
Store daily rows for at most 90 days; retain a final aggregate decision record.
Revenue and costs use the same currency and billing period.

| Measure | Source and boundary |
| --- | --- |
| Eligible guide page views | First-party daily counts keyed only by a fixed allowlist of guide names; absent today, report unmeasured until deployed |
| App creations | Existing `flick_secret_created_total`, summed by day; S3 counts include staged uploads, so creation is not upload completion, a unique user, or a guide conversion |
| Request and upload errors | Daily counts by fixed route template and status class, unmeasured until a privacy-safe counter is verified; no individual traces or URLs copied into the experiment |
| Actual revenue | Sponsor invoice/payment or provider report; distinguish estimated from settled revenue |
| Transfer, object storage, hosting spend | Provider aggregate billing; include free-tier credits separately |
| Support time and friction | Operator minutes and counts of relevant reports; record categories, not user messages or shared links |

Do not collect IDs, full URLs, query strings, fragments, tokens, passphrases,
plaintext, filenames, IP addresses, referrers, fingerprints, or browser storage
identifiers for this experiment. The current nginx access log is not a
privacy-safe page-view dataset: it records request details. Do not export it
to an advertiser or derive a user journey from it. No browser analytics SDK
is needed. A future aggregate counter must discard request details before
incrementing an allowlisted bucket; measurement is unmeasured until verified.

Run 14 calendar days of baseline, followed by at most 28 calendar days with
the reviewed static sponsor enabled. Start the sponsor window only when the
creative, destination, agreement, deployment, and guide/request aggregate counts
are ready.
Without those inputs, stop at preparation; do not invent a trial result.

Review weekly and at day 28:

- Continue only with a positive contribution after hosting/storage/transfer
  spend and support time valued at an operator-declared hourly rate, no security
  regression, and no unresolved accidental-click or obstructed-flow reports.
- Change one placement or copy choice if the trial has fewer than 1,000 eligible
  guide views or the economics are negative; low traffic is inconclusive, not
  proof of profitability. Any new trial is another explicitly bounded window.
- Disable immediately for key/token exposure, unexpected remote requests,
  provider-policy failure, misleading clicks, or blocked create/open controls.
  Pause and investigate if the daily aggregate app 5xx rate exceeds both twice
  baseline and baseline plus 1 percentage point on at least 100 requests.
- At day 28, disable an unprofitable or unmeasurable placement unless a concrete
  revision and new end date are recorded. Never silently extend the experiment.

Page RPM equals revenue divided by the **ad-bearing page views** in the same
period, multiplied by 1,000 ([Google definition](https://support.google.com/adsense/answer/112030?hl=en)).
App creations, private-link opens, and ad-free guide views are not that
denominator. A flat sponsor payment can be reported directly without RPM.
For scenario arithmetic only, 10,000 eligible views at an assumed $2 RPM gives
$20 gross revenue; $15 service spend leaves $5 before support, fees, and tax.
The assumed $2 is not a benchmark or a revenue forecast.

## Release evidence

For #209/#210, verify wide and narrow guide layouts, keyboard access, ordinary
tool navigation, and disabled sponsor configuration. With a synthetic enabled
configuration, inspect network traffic and the outgoing link: no remote
creative requests, no tracking requests, no app identifiers, and no referrer.
Check that sensitive routes never render the sponsor and the CSP is unchanged.
Provider approval, actual traffic, and actual financial results remain separate
external inputs, not software test outcomes.

Run `pnpm --dir web test:sponsor` against the built site selected by
`FLICK_WEB_URL` with the default disabled configuration. For an enabled local
fixture, set `SPONSOR` to `{ name: 'Synthetic sponsor', description: 'Test
placement only.', url: 'https://sponsor.example.test/' }`, rebuild the isolated
site, and run with `FLICK_SPONSOR_TEST_ENABLED=1`. The browser intercepts that
synthetic destination locally; it checks the outgoing request has no referrer,
the new tab has no opener, and only public guides contain the slot. Restore
`SPONSOR = null` before committing; never ship a fixture as a real agreement.
