# UI dependencies

Protocol per package: exact pin; OSV query empty; registry maintainers identifiable; last 10 versions reviewed;
newest release at least 14 days old as of 2026-10-30; no install scripts; `pnpm install --ignore-scripts`; name matches upstream repo.

| Package | Version | Published | Maintainers (registry) | Repo |
|---|---|---|---|---|
| react | 19.3.1 | 2026-10-14 | fb, react-bot | react/react |
| react-dom | 19.3.1 | 2026-10-14 | fb, react-bot | react/react |
| react-router | 8.4.0 | 2026-09-15 | brophdawg11, mjackson | remix-run/react-router |
| @tanstack/react-query | 5.104.3 | 2026-10-15 | tannerlinsley, alemtuzlak, kevinvandy | TanStack/query |
| lucide-react | 1.57.0 | 2026-10-14 | ericfennis | lucide-icons/lucide |
| vite | 8.3.4 | 2026-10-08 | yyx990803, vitebot | vitejs/vite |
| typescript | 7.0.2 | 2026-07-08 | microsoft and typescript-bot accounts | microsoft/TypeScript |
| vitest | 5.0.5 | 2026-10-15 | ariperkkio, antfu, hiogawa, oreanno, yyx990803 | vitest-dev/vitest |
| @testing-library/react | 16.3.3 | 2026-08-27 | testing-library org maintainers | testing-library/react-testing-library |
| @testing-library/dom | 10.4.2 | 2026-09-13 | testing-library org maintainers | testing-library/dom-testing-library |
| @testing-library/user-event | 14.6.8 | 2026-10-13 | testing-library org maintainers | testing-library/user-event |
| jsdom | 30.1.3 | 2026-10-15 | domenic, timothygu, tmpvar, others | jsdom/jsdom |
| @vitejs/plugin-react | 6.1.2 | 2026-10-05 | yyx990803, vitebot | vitejs/vite-plugin-react |
| downshift | 9.4.0 | 2026-06-30 | kentcdodds, silviuaavram (unchanged across the last 3 releases) | downshift-js/downshift (tag v9.4.0, 1bb8b75) |
| @types/react | 19.3.0 | 2026-09-09 | types | DefinitelyTyped |
| @types/react-dom | 19.3.0 | 2026-09-09 | types | DefinitelyTyped |
| boring-avatars | 2.0.4 | 2025-09-28 | hihayk, josepmartins, epallerols | boringdesigners/boring-avatars |
| cronstrue | 3.32.0 | 2026-10-12 | bradymholt (sole maintainer; since 3.18.0 published by GitHub Actions trusted publishing with SLSA provenance) | bradymholt/cRonstrue (tag v3.32.0, gitHead f16e309) |
| @shikijs/core | 4.5.0 | 2026-10-01 | antfu, orta, octref; published by GitHub Actions with provenance | shikijs/shiki (release v4.5.0) |
| @shikijs/engine-javascript | 4.5.0 | 2026-10-01 | antfu, orta, octref; GitHub Actions with provenance | shikijs/shiki |
| @shikijs/langs | 4.5.0 | 2026-10-01 | antfu, orta, octref; GitHub Actions with provenance | shikijs/shiki |
| anser | 2.3.5 | 2025-12-15 | ionicabizau (sole maintainer) | IonicaBizau/anser (tag 2.3.5, d300b23) |
| cmdk | 1.1.1 | 2025-03-14 | paco, dipnpm | pacocoursey/cmdk |
| @radix-ui/react-dialog | 1.3.1 | 2026-10-13 | hadihallak, chancestrickland, mark-workos, npm-workos; GitHub Actions with provenance | radix-ui/primitives |
| @radix-ui/react-select | 2.4.1 | 2026-10-12 | hadihallak, chancestrickland, mark-workos, npm-workos; GitHub Actions with provenance | radix-ui/primitives |
| @codemirror/state | 6.7.6 | 2026-09-22 | marijn (sole maintainer and publisher) | code.haverbeke.berlin/codemirror/state |
| @codemirror/view | 6.43.15 | 2026-10-19 | marijn (sole maintainer and publisher) | code.haverbeke.berlin/codemirror/view |
| @codemirror/commands | 6.11.1 | 2026-09-15 | marijn (sole maintainer and publisher) | code.haverbeke.berlin/codemirror/commands |
| @codemirror/language | 6.13.1 | 2026-10-07 | marijn (sole maintainer and publisher) | code.haverbeke.berlin/codemirror/language |
| @codemirror/lang-python | 6.2.1 | 2025-05-14 | marijn (sole maintainer and publisher) | github.com/codemirror/lang-python |
| @codemirror/lang-markdown | 6.5.2 | 2026-08-04 | marijn (sole maintainer and publisher) | code.haverbeke.berlin/codemirror/lang-markdown |
| @codemirror/lang-json | 6.0.2 | 2025-06-19 | marijn (sole maintainer and publisher) | github.com/codemirror/lang-json |
| @codemirror/lang-yaml | 6.1.3 | 2026-03-24 | marijn (sole maintainer and publisher) | github.com/codemirror/lang-yaml |
| @codemirror/lang-javascript | 6.2.5 | 2026-03-02 | marijn (sole maintainer and publisher) | github.com/codemirror/lang-javascript |
| @codemirror/lang-html | 6.4.12 | 2026-08-04 | marijn (sole maintainer and publisher) | code.haverbeke.berlin/codemirror/lang-html |
| @codemirror/lang-css | 6.3.1 | 2024-11-26 | marijn (sole maintainer and publisher) | github.com/codemirror/lang-css |
| @codemirror/lang-sql | 6.10.0 | 2025-09-16 | marijn (sole maintainer and publisher) | github.com/codemirror/lang-sql |
| @codemirror/legacy-modes | 6.5.5 | 2026-10-07 | marijn (sole maintainer and publisher) | code.haverbeke.berlin/codemirror/legacy-modes |
| @lezer/highlight | 1.2.5 | 2026-09-28 | marijn (sole maintainer and publisher) | code.haverbeke.berlin/lezer/highlight |

Notes
- downshift 9.4.0: newest stable (the alpha tags are not used), 4 months old; last releases 9.3.0 to 9.3.6 match GitHub tags, no burst. OSV clean for it and its 8 new transitive packages
  (@babel/runtime 7.29.10, compute-scroll-into-view 3.1.1, loose-envify 1.4.0, object-assign 4.1.1, prop-types 15.8.1, react-is 16.13.1 and 18.3.1, tslib 2.9.1). No install scripts
  (no preinstall/install/postinstall in its manifest; `pnpm add --ignore-scripts`). `pnpm audit --audit-level=low`: no known vulnerabilities. Name matches the upstream README (downshift-js/downshift).
- Skipped newer releases under 14 days old: react-router 8.5.x, @tanstack/react-query 5.104.4+, lucide-react 1.58+, vite 8.3.5.
- react 19.3.1 and react-dom 19.3.1 were published the same day as 19.0.9, 19.1.10, 19.2.9 (patch releases across lines); 16 days old, kept.
- vite 8 bundles with rolldown, so esbuild is not installed and no package needs a postinstall. A scan of the lockfile finds no `requiresBuild` packages and pnpm reports no ignored build scripts.
- OSV: all 15 direct packages empty; batch query of all 145 resolved packages (direct and transitive) returned 0 with vulnerabilities.
- `pnpm audit --audit-level=low`: No known vulnerabilities found.
- boring-avatars 2.0.4 (added 2026-10-30, generated user avatars): newest release, 13 months old; OSV empty; MIT; no dependencies
  (peer react, react-dom only), no install scripts, lockfile adds no other package. 2.0.3 and 2.0.4 have no git tag; 2.0.4's
  `gitHead` 89270f1 is a "bump version" commit on upstream master, 4 commits after tag v2.0.2 (types and version only).
  Maintainers unchanged since epallerols began publishing 2.0.0 in 2025-07. About 680k weekly downloads; name matches the repo.
- cronstrue 3.32.0 (added 2026-10-31, cron expression to words in the Jobs screens): 3.32.0 is the newest release at least 14 days old (3.33.0 to 3.38.0 were published 2026-10-19 to 2026-10-21 and skipped). OSV empty; MIT; no dependencies; no install scripts (installed with `--ignore-scripts`; lockfile has no `requiresBuild`); `pnpm audit --audit-level=low` clean; lockfile adds only this package. Registry history: v3.11 to v3.14 published by bradymholt, then a gap to 2026-06-18 when releases moved to GitHub Actions trusted publishing (provenance attached), then a run of releases in Oct 2026 each with a GitHub release; a single-maintainer project, so noted. Name matches the repo's package.json (cRonstrue, 1.6k stars, owner bradymholt).
- Log syntax highlighting (added 2026-10-31): Shiki 4.5.0 (`@shikijs/core`, `@shikijs/engine-javascript`, and only the `log`
  grammar from `@shikijs/langs`) tokenizes log lines with VS Code's log grammar plus a small Javo grammar; anser 2.3.5 turns
  ANSI colour codes into runs. 4.5.0 is the newest Shiki release, 30 days old, with a GitHub release and npm provenance; releases
  since 4.2.0 (2026-06-03) come roughly monthly with matching GitHub releases. anser 2.3.5 is the newest
  release, 10 months old, no dependencies, about 22M weekly downloads; 2.3.4 and 2.3.5 share gitHead d300b23 (a republish
  minutes apart). No install scripts in any of them (`pnpm add --ignore-scripts`; lockfile has no `requiresBuild`).
  The packages add 38 transitive packages (unified/hast helpers that `@shikijs/core` imports for its HTML output, and
  oniguruma-to-es with its regex helpers). Four of those resolved to releases under 14 days old, so they are pinned back with
  pnpm overrides: `@shikijs/vscode-textmate` 10.0.2 (10.1.0 is from 2026-10-20), `hast-util-to-html` 9.0.5 (9.0.6 is from
  2026-10-29), `unist-util-visit` 5.1.0 and `unist-util-visit-parents` 6.0.2 (5.2.0 and 6.1.0 are from 2026-10-24); each
  override satisfies its dependents' ranges. OSV batch over all 42 new packages, and the overridden versions: 0 with
  vulnerabilities. `pnpm audit --audit-level=low`: no known vulnerabilities. Names match the upstream repos.
  Shiki is loaded lazily (its own chunk, 169 kB, 56 kB gzip); anser is in the main bundle.
- Command palette (added 2026-10-31): cmdk 1.1.1 (newest release, 19 months old; maintainers paco and dipnpm, every
  release since 0.1 published by paco) gives the filtered, keyboard-driven list; @radix-ui/react-dialog 1.3.1 (already
  cmdk's own dependency, made direct so the palette can set `aria-modal` and a title) gives the modal dialog, focus trap,
  Escape and focus return. They add 25 packages: 16 `@radix-ui/*` (13 of them released 2026-10-13, 18 days old, matching the
  dismissable-layer and dialog fixes merged upstream on 2026-10-13 and 2026-10-14, published by GitHub Actions with SLSA
  provenance; the oldest are 98 days) and 8 small helpers by kashey (aria-hidden 1.2.6, react-remove-scroll 2.7.2,
  react-remove-scroll-bar 2.3.8, react-style-singleton 2.2.3, use-callback-ref 1.3.3, use-sidecar 1.1.3, detect-node-es
  1.1.0, get-nonce 1.0.1; 11 to 78 months old, each its newest release). Every package is its newest release and at least
  14 days old, so no override is needed. No install scripts (aria-hidden only has a `prepare` hook, which npm and pnpm do
  not run for registry installs; installed with `--ignore-scripts`; lockfile has no `requiresBuild`). OSV batch over all
  25: 0 with vulnerabilities. `pnpm audit --audit-level=low`: no known vulnerabilities. Names match the upstream repos.
  The palette loads as its own chunk on first open (59 kB, 20 kB gzip); the main bundle grows 1.5 kB.
- Dropdowns (added 2026-11-03): @radix-ui/react-select 2.4.1 (newest release, 22 days old, published the same day as
  @radix-ui/react-dialog 1.3.1 and built on the same primitive, context, portal, dismissable-layer and focus-scope versions
  already installed; the base of shadcn's Select) gives the listbox, keyboard navigation, typeahead and the hidden native
  select that forms read. It adds 14 packages: 9 `@radix-ui/*` (number 1.1.3, rect 1.1.3, react-arrow 1.1.17,
  react-collection 1.2.0, react-direction 1.2.0, react-popper 1.4.0, react-use-rect 1.1.4, react-use-size 1.1.5,
  react-visually-hidden 1.2.13; 22 to 103 days old, same four maintainers) and 4 from floating-ui (core 1.8.0, dom 1.8.0,
  react-dom 2.1.9, utils 0.2.12; 115 days old, maintainers fezvrasta and atomiks). Every package is its newest release,
  published by GitHub Actions with provenance; last 10 releases of each follow the upstream release cadence, no burst and
  no new maintainer. No install scripts (none in any manifest; `pnpm add --save-exact --ignore-scripts`; lockfile has no
  `requiresBuild`). OSV batch over all 14: 0 with vulnerabilities. `pnpm audit --audit-level=low`: no known
  vulnerabilities. Names match the upstream repos (radix-ui/primitives, floating-ui/floating-ui).
- File editor (added 2026-11-03): CodeMirror 6 used directly (no React wrapper): `@codemirror/state` 6.7.6, `view` 6.43.15,
  `commands` 6.11.1, `language` 6.13.1, `legacy-modes` 6.5.5 (only the shell mode is imported), `lang-python` 6.2.1,
  `lang-markdown` 6.5.2, `lang-json` 6.0.2, `lang-yaml` 6.1.3, `lang-javascript` 6.2.5, `lang-html` 6.4.12, `lang-css` 6.3.1,
  `lang-sql` 6.10.0, and `@lezer/highlight` 1.2.5 (the highlight tags). Each is the newest release and at least 15 days old.
  They add 16 transitive packages, all by the same author: `@codemirror/autocomplete` 6.20.4, `lint` 6.9.7, `streamparser`
  6.0.0, `@lezer/common`, `lr`, `css` 1.3.9, `html` 1.3.13, `javascript` 1.5.6, `json` 1.0.3, `markdown` 1.8.1, `python`
  1.1.19, `yaml` 1.0.4, `@marijn/find-cluster-break` 1.0.4, `crelt` 1.0.7, `style-mod` 4.1.4, `w3c-keyname` 2.2.8.
  `@lezer/common` 1.5.5 and 1.5.4 and `@lezer/lr` 1.4.12 are under 14 days old, so they are pinned back with pnpm
  overrides to `@lezer/common` 1.5.3 (2026-09-24) and `@lezer/lr` 1.4.11 (2026-10-07); every dependent's range
  (`^1.0.0` to `^1.5.0`) accepts them. All 30 packages list one maintainer, marijn (Marijn Haverbeke, CodeMirror's
  author), who published every release of the last 12 months; no new maintainer. Releases since 2026-06 name
  code.haverbeke.berlin as their repository instead of github.com/codemirror (`@codemirror/state` from 6.7.0); the GitHub repositories
  are archived with a README that points to the new forge, codemirror.net links there, and its tags match the npm
  versions and dates (view 6.43.15 tag da63d53 on 2026-10-19). No package has an install script (`hasInstallScript`
  false for all 30; their `prepare` scripts only run in a git checkout; `pnpm add --save-exact --ignore-scripts`; lockfile
  has no `requiresBuild`). OSV batch over all 30: 0 with vulnerabilities. `pnpm audit --audit-level=low`: no known
  vulnerabilities. Names match the official `@codemirror` and `@lezer` scopes. The file viewer reuses Shiki 4.5.0 with
  more of its grammars from `@shikijs/langs` (python, markdown, json, yaml, javascript, jsx, typescript, tsx, html, css,
  sql, shellscript); no new package. The editor and each grammar load lazily, in chunks of their own.

## API client generator (ui/tools/gen-api, own lockfile)

`pnpm gen:api` installs this small package (`--frozen-lockfile --ignore-scripts`) and writes `src/api/` from `openapi.json`
(`make openapi` writes that file from `javo openapi`). It is kept apart from the app because @hey-api/openapi-ts uses the
TypeScript compiler API, which TypeScript 7 (the app's type checker) does not ship; with 7.0.2 it fails on `ts.SyntaxKind`.
The generated client code is committed and has no runtime dependency.

| Package | Version | Published | Maintainers (registry) | Repo |
|---|---|---|---|---|
| @hey-api/openapi-ts | 0.99.0 | 2026-06-22 | mrlubos; published by GitHub Actions with SLSA provenance | hey-api/hey-api (tag `@hey-api/openapi-ts@0.99.0`, 26420d1) |
| typescript | 6.0.3 | 2026-04-16 | typescript-bot | microsoft/TypeScript |
| js-yaml (override) | 4.3.2 | 2026-08-26 | vitaly | nodeca/js-yaml (tag 4.3.2) |

Notes
- 0.99.0 is the newest stable release (the only newer builds are `0.0.0-next-*` snapshots); maintainer list unchanged; no install scripts (`requiresBuild` count 0).
- Transitive audit first failed: @hey-api/json-schema-ref-parser 1.4.4 pins js-yaml 4.2.0, which has three high DoS advisories
  (GHSA-52cp-r559-cp3m, GHSA-5p4m-2wfm-xmqj, GHSA-2883-xcg3-v3hh). Fixed with a pnpm override to js-yaml 4.3.2 (patched, 2 months old,
  same maintainer, no install scripts, OSV clean). The generator only parses our own JSON document, so the advisories were not reachable in practice.
- After the override: `pnpm audit --audit-level=low` clean; OSV batch over all 52 resolved packages: 0 with vulnerabilities.
- App lockfile unchanged by the generator (no new app dependencies). Tests mock fetch directly; MSW was not added.

## swagger-ui-dist (API docs page, 2026-10-31)

| Package | Version | Published | Maintainers (registry) | Repo |
|---|---|---|---|---|
| swagger-ui-dist | 5.33.3 | 2026-10-15 | swagger-admin, ponelat, swagger-api, kyleshockey; every release of the last year published by swagger-api with npm provenance | swagger-api/swagger-ui (tag v5.33.3 = gitHead 38ca10e, GitHub release 2026-10-15) |

- Newest release at least 14 days old (5.34.0 and 5.34.1 are younger). Last 12 versions: steady patch releases every one to three weeks by the same publisher, no burst.
- OSV query for 5.33.3: empty. Tarball sha512 checked against the registry integrity (also in pnpm-lock.yaml).
- No install scripts. Its one dependency, `@scarf/scarf` 1.4.0 (sole maintainer aviaviavi), exists only to run an analytics `postinstall` (`node ./report.js`) that calls home; the bundle never imports it. It is removed from the tree with the pnpm override `"@scarf/scarf": "-"`, so it is never downloaded. OSV for it was empty too.
- Only `swagger-ui-bundle.js` and `swagger-ui.css` are used: `scripts/copy-apidocs.mjs` copies them into `dist/apidocs/` at build, and the gateway serves them at `/api/docs/` with `script-src 'self'` (its own initializer replaces the package's inline one; the validator badge, an outside call, is off).
- After install: `pnpm audit --audit-level=low` clean.
