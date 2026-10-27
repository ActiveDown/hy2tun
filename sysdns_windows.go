package main

// systemDNSServers is not implemented on Windows yet: the fallback to the
// network's own resolvers stays off there.
func systemDNSServers(skip []string) []string { return nil }
