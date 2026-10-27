package main

import "os"

// defaultDataDir is only a fallback: the app passes its own files directory.
func defaultDataDir() string { return os.TempDir() }

func lockDownFile(path string, usersRead bool) { os.Chmod(path, 0o600) }
