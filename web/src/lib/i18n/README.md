# `web/src/lib/i18n/`

Display language for the web app: English (`en`), Korean (`ko`), Japanese (`ja`).

- `locale.svelte.ts`: the current language. `initializeLocale` picks a stored manual choice
  (`localStorage` key `flick-locale`), else the first browser language Flick has, else `en`.
  `chooseLocale` is the manual switch (`LanguageRow.svelte` in `web/src/lib/components/`).
  URLs never carry a language.
- `index.ts`: `t()` returns the catalog of the current language; read it inside markup or
  `$derived` so text follows a language change.
- `web/src/routes/+layout.svelte` calls `initializeLocale` while the layout is created, before
  any page's `onMount`, so text a page stores at mount is already in the chosen language.
  Text stored in `$state` (an error or notice) keeps the language it was written in until it
  is replaced; markup and `$derived` values follow a language change at once.
- `messages/en/*.ts` are the source catalogs, one file per screen area. `messages/ko/*.ts` and
  `messages/ja/*.ts` give the same keys typed as `typeof <en area>`, so a missing or extra key
  fails `pnpm check`.

## Wording rules

- **English skeleton (decided 2026-11-19).** Page names (`h1` such as "Create a secret",
  "Open a secret", "Request a secret"), the delivery tabs, primary action buttons and their
  in-progress labels, and the header's text actions stay in English in every language ("Send",
  "Link", "Live Transfer", "Create Link", "Enter Code", "Request", "Open", "Download").
  Everything else is translated: headings that report a status or outcome ("Link created",
  "Request expired", "Transfer complete", "Nothing here."), descriptions, form labels,
  placeholders, errors, and accessible names.
- **Users' words, not the system's (decided 2026-11-19).** Name things the way the sender,
  recipient, or requester would: "열다" not "복호화", "보내다·도착" not "제출·접수", "내용" not
  "비밀" for what was sent, "상대에게 줄 링크 / 내 받기 링크" not "제출 링크 / 비공개 받기
  링크". Leave out implementation words (session, reservation, memory, key, format names, API
  paths), and never use "request" for an HTTP call, because Request is a feature name. The
  passphrase is just "passphrase" / "비밀번호" / "パスワード", never "additional".
- Korean says **비밀번호** for passphrase and uses 해요체 for sentences. Japanese uses
  です・ます and **パスワード** for passphrase.
- The term list is `docs/work/active/2026-11-12-i18n-glossary.md`.
