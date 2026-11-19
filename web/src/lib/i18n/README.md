# `web/src/lib/i18n/`

Display language for the web app: English (`en`), Korean (`ko`), Japanese (`ja`).

- `locale.svelte.ts`: the current language. `initializeLocale` picks a stored manual choice
  (`localStorage` key `flick-locale`), else the first browser language Flick has, else `en`.
  `chooseLocale` is the manual switch (`LanguageRow.svelte` in `web/src/lib/components/`).
  URLs never carry a language.
- `index.ts`: `t()` returns the catalog of the current language; read it inside markup or
  `$derived` so text follows a language change.
- `messages/en/*.ts` are the source catalogs, one file per screen area. `messages/ko/*.ts` and
  `messages/ja/*.ts` give the same keys typed as `typeof <en area>`, so a missing or extra key
  fails `pnpm check`.

## Wording rules

- **English skeleton (decided 2026-11-19).** Page titles (`h1`), the delivery tabs, primary
  action buttons, and the header's text actions stay in English in every language ("Send",
  "Link", "Live Transfer", "Create Link", "Enter Code", "Request"). Everything else is
  translated: descriptions, form labels, placeholders, status, errors, and accessible names.
- Korean says **비밀번호** for passphrase and uses 해요체 for sentences. Japanese uses
  です・ます and **パスワード** for passphrase.
- The term list is `docs/work/active/2026-11-12-i18n-glossary.md`.
