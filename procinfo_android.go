package main

import "net/netip"

// procTable: on Android the per-app choice is made by VpnService
// (addAllowedApplication / addDisallowedApplication), so flows are not
// attributed to applications here.
type procTable struct{}

func newProcTable() *procTable { return &procTable{} }

func (pt *procTable) lookupTCP(srcPort uint16, dst netip.Addr, dstPort uint16) *procInfo { return nil }
func (pt *procTable) lookupUDP(srcPort uint16, v6 bool) *procInfo                        { return nil }
func (pt *procTable) runningWithNetwork() []runningProc                                  { return nil }
