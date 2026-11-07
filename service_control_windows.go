//go:build windows

package main

import (
	"errors"
	"fmt"
	"runtime"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// Open only the rights needed by the desktop app. mgr.Connect/OpenService ask
// for ALL_ACCESS, which fails for a normal, non-elevated user.
func openControlService(access uint32) (*mgr.Service, error) {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return nil, err
	}
	defer windows.CloseServiceHandle(m)
	name, _ := windows.UTF16PtrFromString(serviceName)
	h, err := windows.OpenService(m, name, access)
	if err != nil {
		return nil, err
	}
	return &mgr.Service{Name: serviceName, Handle: h}, nil
}

func queryServiceStatus() (svc.Status, error) {
	s, err := openControlService(windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return svc.Status{}, err
	}
	defer s.Close()
	return s.Query()
}

func startInstalledService() error {
	st, err := queryServiceStatus()
	if err != nil {
		return err
	}
	if st.State != svc.Stopped {
		return nil
	}
	s, err := openControlService(windows.SERVICE_START)
	if err != nil {
		return err
	}
	defer s.Close()
	err = s.Start()
	if errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return nil
	}
	return err
}

// Grant local users permission to start/query this service, not to stop it,
// change its configuration or change its executable. Keep the existing ACL.
func allowServiceStart(s *mgr.Service) error {
	sd, err := windows.GetSecurityInfo(s.Handle, windows.SE_SERVICE, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if acl == nil {
		return errors.New("service has no DACL")
	}
	sid, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	if err != nil {
		return err
	}
	var pin runtime.Pinner
	pin.Pin(sid)
	defer pin.Unpin()
	acl, err = windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.SERVICE_START | windows.SERVICE_QUERY_STATUS,
		AccessMode:        windows.GRANT_ACCESS,
		Trustee:           windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_WELL_KNOWN_GROUP, TrusteeValue: windows.TrusteeValueFromSID(sid)},
	}}, acl)
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(s.Handle, windows.SE_SERVICE, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func waitServiceState(s *mgr.Service, want svc.State, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == want {
			return nil
		}
		if want == svc.Running && st.State == svc.Stopped {
			return fmt.Errorf("service stopped (Windows error %d, service error %d)", st.Win32ExitCode, st.ServiceSpecificExitCode)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for service state %d (current %d)", want, st.State)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
