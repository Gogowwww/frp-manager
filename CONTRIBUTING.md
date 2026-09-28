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

The panel is a Flask application with no build step: Python on the server
(`app.py`), native ES modules in the browser (`templates/assets/js/`).

```bash
git clone https://github.com/Gogowwww/frp-manager.git && cd frp-manager
python3 -m venv .venv && . .venv/bin/activate
pip install -r requirements.txt pytest ruff
```

To run the panel locally without touching `/etc/frp-manager`:

```bash
echo '{"bind_port": 18765, "ssl_enabled": false}' > /tmp/frpm.json
FRP_MANAGER_CONFIG=/tmp/frpm.json python3 app.py
```

The [demo](demo/build.py) (`python3 demo/build.py`) produces a static version
of the interface with sample data, handy to work on the front end without frp
installed.

The project website ([frp-manager.gogow.fr](https://frp-manager.gogow.fr)) is
built with `python3 website/build.py`: home page, documentation rendered from
`docs/`, `SECURITY.md`, `CHANGELOG.md`… and the demo under `/demo/`. Serve the
result with `DEMO_SITE=dist-site python3 demo/serve.py`. After a visible change
to the interface, `python3 website/shots.py` retakes the website screenshots
from the demo (requires Pillow and Chrome or Edge).

## Before opening a pull request

```bash
ruff check .
python3 -m pytest
```

- One pull request = one topic. Describe the problem it solves and how you
  tested it (install script, Docker, or both).
- Keep the style of the existing code: comments in French with proper
  accents, explicit names, no new dependency without a reason.
- Add a test when you touch authentication, input validation or system
  commands.
- System commands are always argument lists (never `shell=True` nor `sh -c`
  with interpolated data); every service or container name goes through
  `valid_service_name` / `valid_container_name`.
- The interface only builds the DOM with `h()` (`templates/assets/js/ui.js`),
  never with `innerHTML` on data. No emoji in the interface: icons are the
  embedded SVGs of `templates/partials/icons.html`.
- Update the "Unreleased" section of the changelog, in both languages:
  [CHANGELOG.md](CHANGELOG.md) and [CHANGELOG.fr.md](CHANGELOG.fr.md).

## Translating the panel

All interface texts live in `templates/assets/locales/`: `fr.js` (reference
language) and `en.js`.

1. Copy `fr.js` to `<code>.js` (`de.js`, `es.js`…) and translate the values,
   without touching the keys or the `{parameters}`.
2. Declare the language in `LOCALES` in `templates/assets/js/i18n.js`.
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
project's license, [Apache 2.0](LICENSE).
