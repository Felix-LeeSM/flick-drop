# BurnLink Web

SvelteKit frontend for BurnLink.

## Commands

Install dependencies:

```sh
pnpm install
```

Run local checks:

```sh
pnpm check
pnpm lint
pnpm test
pnpm build
```

Start the development server:

```sh
pnpm dev
```

## Boundary

Browser code owns plaintext, passphrases, and derived keys. API requests must
contain ciphertext and safe metadata only.

The project was scaffolded with:

```sh
pnpm dlx sv@0.16.1 create --template minimal --types ts --add vitest="usages:unit" sveltekit-adapter="adapter:static" --no-download-check --install pnpm web
```
