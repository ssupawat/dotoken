# DoToken 👀

A lightweight macOS menu bar app to monitor AI usage limits in real-time.

## Providers

- **Claude Pro** — session & weekly limits via tmux + `/usage`
- **OpenCode Go** — 5h rolling, weekly, monthly via web API
- **Z.ai** — queries & token limits via API

## Install

```bash
curl -sL https://raw.githubusercontent.com/ssupawat/dotoken/main/install.sh | bash
```

> On first run, macOS may show "cannot be opened because it is from an unidentified developer." Right-click the binary → Open → click Open to bypass Gatekeeper.

> For auto-start on login, add `dotoken` to **System Settings → General → Login Items**.

## Settings

Config file: `~/.dotoken.json`

| Field | Description |
|-------|-------------|
| `zaiToken` | Z.ai API bearer token |
| `claudeSession` | tmux session name running Claude Code (e.g. `tw-claude`) |
| `openCodeCookie` | `auth` cookie value from opencode.ai |

Settings can also be edited from the app's settings panel.

## Build

```bash
wails3 build
```

## Run

```bash
nohup ./bin/dotoken > /dev/null 2>&1 &
```

Stop with `pkill -f dotoken`.
