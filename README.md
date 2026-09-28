# DoToken 👀

A lightweight macOS menu bar app to monitor AI usage limits in real-time.

## Providers

- **Claude Pro** — 5-hour session & weekly limits via Anthropic's OAuth usage API. Reads the token Claude Code stores in your macOS Keychain — no tmux session, no interference with your CLI sessions.
- **OpenCode Go** — 5h rolling, weekly, monthly via the console status API
- **Z.ai** — queries & token limits via API

## Install

```bash
curl -sL https://raw.githubusercontent.com/ssupawat/dotoken/main/install.sh | bash
```

> On first run, macOS may show "cannot be opened because it is from an unidentified developer." Right-click DoToken.app → Open → click Open to bypass Gatekeeper.

> For auto-start on login, add DoToken to **System Settings → General → Login Items**.

## Requirements

- **Claude**: [Claude Code](https://code.claude.com/) installed and signed in (`claude /login`). The app reads the OAuth token from your Keychain and refreshes it when needed.
- **OpenCode**: a paid Go plan. Paste the full `Cookie` request header from opencode.ai (must include both `auth=` and `__Host-console_session=`).

## Settings

Config file: `~/.dotoken.json`

| Field | Description |
|-------|-------------|
| `zaiToken` | Z.ai API bearer token |
| `openCodeCookie` | full `Cookie` header from opencode.ai console |

Settings can also be edited from the app's settings panel (tray menu → settings).

When a provider's session expires, the app shows a warning banner with how to fix it instead of silently hiding the card.

## Build

```bash
cp assets/appicon.png build/appicon.png
wails3 build
```

Requires [Wails v3](https://v3.wails.io/) and Go 1.25+.

## Run

```bash
(./bin/dotoken > /tmp/dotoken.log 2>&1 & disown)
```

Stop with `pkill -f dotoken`.
