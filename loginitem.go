package main

import (
	"errors"
	"log"
)

// loginItem is the part of Wails' autostart manager DoToken uses, so tests can
// swap in a fake instead of touching the real login items.
type loginItem interface {
	Enable() error
	Disable() error
	IsEnabled() (bool, error)
}

// launchAtLogin registers DoToken as a login item: SMAppService for the bundled
// app on macOS 13+, a LaunchAgent plist otherwise. Set in main.
var launchAtLogin loginItem

// GetLaunchAtLogin reports whether DoToken opens at login. macOS is the source
// of truth, so switching it off in System Settings shows up here too.
func (t *DoToken) GetLaunchAtLogin() bool {
	if launchAtLogin == nil {
		return false
	}
	enabled, err := launchAtLogin.IsEnabled()
	if err != nil {
		log.Printf("login item: status failed: %v", err)
		return false
	}
	return enabled
}

// SetLaunchAtLogin adds DoToken to, or removes it from, the user's login items.
func (t *DoToken) SetLaunchAtLogin(enabled bool) error {
	if launchAtLogin == nil {
		return errors.New("open at login is not available")
	}
	var err error
	if enabled {
		err = launchAtLogin.Enable()
	} else {
		err = launchAtLogin.Disable()
	}
	if err != nil {
		log.Printf("login item: update failed: %v", err)
		return errors.New("couldn't update login items — add DoToken in System Settings → General → Login Items")
	}
	return nil
}
