package main

import (
	"reflect"
	"testing"
)

func TestParseScutilDNS(t *testing.T) {
	dump := `<dictionary> {
  ServerAddresses : <array> {
    0 : 192.168.10.4
    1 : 8.8.8.8
    2 : 127.0.0.1
    3 : fe80::1
  }
  __FLAGS__ : 2
}`
	got := parseScutilDNS(dump, []string{"8.8.8.8"})
	if want := []string{"192.168.10.4"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
