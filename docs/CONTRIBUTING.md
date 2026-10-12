# Contributing to FRP Manager

**English** · [Français](CONTRIBUTING.fr.md)

Thank you for your interest! Bug reports, ideas, translations and pull
requests are welcome, in English or French. By taking part, you agree to the
[code of conduct](CODE_OF_CONDUCT.md).

## Reporting a bug or suggesting an idea

- Search the existing [issues](https://github.com/Gogowwww/frp-manager/issues)
  first.
- Use the "Bug" or "Feature request" template: panel version, install method
  (script or Docker), frp version, steps to reproduce.
- Remove tokens, public IP addresses and domain names from what you paste
  (TOML configurations, logs).
- **Security issues are not reported in a public issue**: see
  [SECURITY.md](SECURITY.md).

## Setting up the environment

The panel is a Go program (`panel/*.go`, Go 1.26 or later) that embeds its
interface at build time: native ES modules in the browser
(`panel/templates/assets/js/`), with no build step.

```bash
git clone https://github.com/Gogowwww/frp-manager.git && cd frp-manager
go build -o frp-manager ./panel
```

To run the panel locally without touching `/etc/frp-manager`:

```bash
echo '{"bind_port": 18765, "ssl_enabled": false}' > /tmp/frpm.json
FRP_MANAGER_CONFIG=/tmp/frpm.json go run ./panel
```

The [demo](../website/demo.go) (`go run ./website demo`) produces a static version
of the interface with sample data, handy to work on the front end without frp
installed.

The project website ([frp-manager.gogow.fr](https://frp-manager.gogow.fr)) is
built with `go run ./website build`: home page, documentation rendered from
`docs/`, `CHANGELOG.md`… and the demo under `/demo/`. Serve the
result with `go run ./website serve dist-site`; `go run ./website seo dist-site` checks
titles, links and structured data. After a visible change to the interface,
`go run ./website shots` retakes the website screenshots from the demo
(requires Chrome, Edge or Chromium).

## Before opening a pull request

```bash
go vet ./... && go test ./... && gofmt -l .
ruff check .        # Python bridge (packaging/python-bridge)
```

- One pull request = one topic. Describe the problem it solves and how you
  tested it (install script, Docker, or both).
- Keep the style of the existing code: comments in French with proper
  accents, explicit names, no new dependency without a reason.
- Add a test when you touch authentication, input validation or system
  commands.
- System commands are always argument lists (`exec.Command`, never `sh -c`
  with interpolated data); every service or container name goes through
  `validServiceName` / `validContainerName`.
- The interface only builds the DOM with `h()` (`panel/templates/assets/js/ui.js`),
  never with `innerHTML` on data. No emoji in the interface: icons are the
  embedded SVGs of `panel/templates/partials/icons.html`.
- Update the "Unreleased" section of the changelog, in both languages:
  [CHANGELOG.md](../CHANGELOG.md) and [CHANGELOG.fr.md](../CHANGELOG.fr.md).

## Versions and releases

Development happens on the project's Forgejo, where all continuous
integration runs; GitHub is only used to publish (no GitHub Actions
workflow).

- **Numbers**: up to 0.0.60, versions follow each other (`0.0.59`,
  `0.0.60`). Then semantic versioning from `0.1.0`: the middle number for a
  new feature (`0.2.0`), the last one for fixes only (`0.1.1`), the first one
  for a change that breaks the configuration, the API or the installation
  (`1.0.0`).
- **Pre-release**: an annotated `vX.Y.Z` tag on the Forgejo main (after the
  dated section of both CHANGELOGs) runs `release.yml`: program, `:X.Y.Z`
  image, packages in the `testing` apt distribution, GitHub pre-release. Its
  notes (French then English) come from both CHANGELOGs through
  `tools/releasenotes`: the dated section starts with a one-sentence summary,
  which becomes the title and introduction (`go run ./tools/releasenotes vX.Y.Z`
  to proofread them before tagging).
- **Final release**: `promote.yml` (Actions → "Promouvoir une release", tag
  `vX.Y.Z`) makes the same pre-release final, without rebuilding it: what was
  tried is exactly what gets published (`:latest` image, `stable` apt
  distribution, public main moved forward).
- **Between releases**: `github.yml` (Actions → "Publier sur GitHub")
  publishes the Forgejo main to the public main (documentation, translations,
  CHANGELOG…), fast-forward only and only once the commit's CI has passed.

## Translating the panel

All interface texts live in `panel/templates/assets/locales/`: `fr.js` (reference
language) and `en.js`.

1. Copy `fr.js` to `<code>.js` (`de.js`, `es.js`…) and translate the values,
   without touching the keys or the `{parameters}`.
2. Declare the language in `LOCALES` in `panel/templates/assets/js/i18n.js`.
3. While it is incomplete, also add it to `PREVIEW_LOCALES`: it is offered in
   Settings marked *preview*, without being picked from the browser language.

Every key added to `fr.js` must also be added to `en.js` (otherwise the
interface silently falls back to French). The documentation exists in two
languages: `X.md` in English, `X.fr.md` in French (README, SECURITY,
CONTRIBUTING, CODE_OF_CONDUCT, CHANGELOG and the pages of `docs/`). Update
both.

## Community blocklists

A list of addresses to share? Use the **Publish** button on a *Block* firewall
rule, or open a pull request on the
[`blocklists`](https://github.com/Gogowwww/frp-manager/tree/blocklists) branch.

## License

By contributing, you agree that your contribution is published under the
project's license, [Apache 2.0](../LICENSE).
