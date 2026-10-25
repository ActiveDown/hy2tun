package main

// installedApp is an application that can be picked for the app rules.
type installedApp struct {
	Label string `json:"label"` // display name
	Name  string `json:"name"`  // process name the rules match on (lower case)
	Path  string `json:"path"`  // application bundle / executable
}
