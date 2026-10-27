package main

import "sync"

// The app sends the list of installed packages (PackageManager); the rules
// match on the package name.
var androidApps struct {
	mu   sync.Mutex
	list []installedApp
}

func setInstalledApps(list []installedApp) {
	androidApps.mu.Lock()
	androidApps.list = list
	androidApps.mu.Unlock()
}

func installedApps() []installedApp {
	androidApps.mu.Lock()
	defer androidApps.mu.Unlock()
	return append([]installedApp(nil), androidApps.list...)
}
