# Oku Agent npm packages

`@oku-trade/cli` is a thin Node launcher. The native binary ships in one of four
exact-version optionalDependencies (`@oku-trade/cli-{linux,darwin}-{x64,arm64}`), selected by
npm's `os`/`cpu` metadata. No postinstall scripts or downloads. If the platform package is
missing (e.g. `--omit=optional`), the launcher exits 1 with an explanatory error.

Package names are candidates pending scope ownership checks. License is Apache-2.0 and
publish access is public; publishing remains a separate maintainer decision. See `docs/releasing.md`.
