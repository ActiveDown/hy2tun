package main

import "time"

// diagCommands: an app cannot run the network tools; the bundle holds the log
// and the live test only.
func diagCommands() []diagCmd { return nil }

func diagRun(timeout time.Duration, argv []string) string { return "" }
