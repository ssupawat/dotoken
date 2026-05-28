# Token Watch ⬡

A lightweight, native macOS menu bar app to monitor your AI token limits and usage in real-time.

![Screenshot](https://github.com/ssupawat/tokenwatch/raw/main/build/screenshot.png)

## Features

- **Claude Pro (Sonnet 4.6)**: Automatically tracks your 5-hour and weekly subscription limits as percentages via your active terminal session.
- **Z.ai**: Monitor daily queries and token limits natively from your dashboard.
- **Auto-Refresh**: Silently updates in the background every 5 minutes (paused when settings are open).
- **Settings view**: Paste your Z.ai token and select your active Claude Tmux session directly from the UI.
- **Clean macOS UI**: Translucent, frameless, and attached directly to your status bar with proper rounded corners.

## How it works

1. **Z.ai**: Calls the Z.ai limit API with your custom token.
2. **Claude**: Hooks into an active local `tmux` session (e.g. `tw-claude`) running Claude Code, sends `/usage`, captures the plan bars, and closes the dialog seamlessly under 1 second. This completely bypasses background macOS Keychain authorization locks.

## Notes

- Claude's `/usage` fetches data from Anthropic's API, which can occasionally be slow or unresponsive. When this happens, the UI shows a shimmer loading bar and falls back to the last known cached data.
- If Claude's session authentication expires, `/usage` will hang on "Loading usage data…" indefinitely. Re-login to Claude to fix this.
- Claude Code must run in your own terminal (not spawned by the app) to avoid macOS Keychain permission issues. The app simply reads from your active tmux session.

## Build and Run

1. Install [Wails v3](https://v3.wails.io/):
   ```bash
   go install github.com/wailsapp/wails/v3/cmd/wails3@latest
   ```

2. Build the app:
   ```bash
   wails3 build
   ```

3. Run the binary (detached from terminal):
   ```bash
   nohup ./bin/tokenwatch &
   ```

   Closing your terminal won't kill the app. Use `pkill -f tokenwatch` to stop it.
