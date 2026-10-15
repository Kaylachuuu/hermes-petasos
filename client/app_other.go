//go:build !windows && !linux

package main

// windowsApp: only petasos.exe is a window of its own (app_windows.go).
func windowsApp() bool { return false }

// computerVolume is the Windows app's; the Mac's is macVolume.
func computerVolume(int) {}
