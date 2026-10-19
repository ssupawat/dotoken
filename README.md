# DoToken 👀

A simple macOS menu bar app to monitor AI usage limits in real-time.

## Providers

- **Claude Pro** — 5-hour session & weekly limits via Anthropic's OAuth usage API. Reads the token Claude Code stores in your macOS Keychain.
- **OpenCode Go** — 5h rolling, weekly, monthly via the console status API
- **Z.ai** — queries & token limits via API
- **GitHub Copilot** — monthly premium requests (plus chat & completions on Copilot Free). Uses the token your Copilot editor plugin or the `gh` CLI already stores, so there is nothing to paste.

## Install

```bash
curl -sL https://raw.githubusercontent.com/ssupawat/dotoken/main/install.sh | bash
```

> On first run, macOS may show "cannot be opened because it is from an unidentified developer." Right-click DoToken.app → Open → click Open to bypass Gatekeeper.

> For auto-start on login, add DoToken to **System Settings → General → Login Items**.

## Requirements

- **Claude**: [Claude Code](https://code.claude.com/) installed and signed in (`claude /login`). The app reads the OAuth token from your Keychain and refreshes it when needed.
- **Copilot**: signed in to Copilot in VS Code / JetBrains / Neovim, or to the GitHub CLI (`gh auth login`). The card stays hidden if the account has no Copilot seat.
- **OpenCode**: a paid Go plan. Paste the full `Cookie` request header from opencode.ai (must include both `auth=` and `__Host-console_session=`).

## Settings

Config file: `~/.dotoken.json`

| Field | Description |
|-------|-------------|
| `zaiToken` | Z.ai API bearer token |
| `openCodeCookie` | full `Cookie` header from opencode.ai console |
| `copilotToken` | optional GitHub OAuth token for Copilot, if the auto-detected one is not the right account |
| `hideTrayLabel` | `true` to hide the usage % next to the menu bar icon |

Usage refreshes in the background every 5 minutes. The menu bar shows the highest usage % across all providers (e.g. `73%`), so you can see how close you are to a limit without opening the popup; a `!` means a provider needs you to sign in again.

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
