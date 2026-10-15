package main

import (
	"errors"
	"testing"
)

type fakeLoginItem struct {
	enabled bool
	err     error
}

func (f *fakeLoginItem) Enable() error {
	if f.err != nil {
		return f.err
	}
	f.enabled = true
	return nil
}

func (f *fakeLoginItem) Disable() error {
	if f.err != nil {
		return f.err
	}
	f.enabled = false
	return nil
}

func (f *fakeLoginItem) IsEnabled() (bool, error) { return f.enabled, f.err }

func withLoginItem(t *testing.T, item loginItem) {
	t.Helper()
	prev := launchAtLogin
	launchAtLogin = item
	t.Cleanup(func() { launchAtLogin = prev })
}

func TestLaunchAtLoginToggles(t *testing.T) {
	fake := &fakeLoginItem{}
	withLoginItem(t, fake)
	d := &DoToken{}

	if d.GetLaunchAtLogin() {
		t.Fatal("expected open at login to start off")
	}
	if err := d.SetLaunchAtLogin(true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !d.GetLaunchAtLogin() {
		t.Error("expected open at login to be on after enabling")
	}
	if err := d.SetLaunchAtLogin(false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if d.GetLaunchAtLogin() {
		t.Error("expected open at login to be off after disabling")
	}
}

func TestLaunchAtLoginReportsFailure(t *testing.T) {
	withLoginItem(t, &fakeLoginItem{err: errors.New("SMAppService register: denied")})
	d := &DoToken{}

	if d.GetLaunchAtLogin() {
		t.Error("a status error should read as off")
	}
	if err := d.SetLaunchAtLogin(true); err == nil {
		t.Error("expected an error when the login item can't be registered")
	}
}

func TestLaunchAtLoginUnavailable(t *testing.T) {
	withLoginItem(t, nil)
	d := &DoToken{}

	if d.GetLaunchAtLogin() {
		t.Error("expected off when no login item manager is set")
	}
	if err := d.SetLaunchAtLogin(true); err == nil {
		t.Error("expected an error when no login item manager is set")
	}
}
