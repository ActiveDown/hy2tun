package main

import (
	"errors"
	"fmt"
	"os"
)

// The command line of the desktop builds does not exist on Android: the library
// is driven by the app through JNI (mobile_android.go).

func cmdService(args []string)   { notOnAndroid() }
func cmdInstall(args []string)   { notOnAndroid() }
func cmdUninstall(args []string) { notOnAndroid() }
func cmdGUI(args []string)       { notOnAndroid() }

func notOnAndroid() {
	fmt.Fprintln(os.Stderr, "not available on Android")
	os.Exit(2)
}

func tryElevateInstall() error       { return errors.New("not available on Android") }
func handoffConnect(id string) error { return errors.New("not available on Android") }
