# Windows service startup regression (1.1.0)

Validated on 2026-11-07 in Windows 11 under Parallels Desktop on Apple Silicon,
using the Windows amd64 application (x64 emulation).

## Reproduction on 1.0.0

- Fresh service registration set `Start=2` and `DelayedAutoStart=1`.
- After reboot: at 21.34 seconds uptime, SCM reported STOPPED and API was unavailable.
- At 133.17 seconds uptime, SCM reported RUNNING and API answered.
- The old GUI only polled the API; opening it did not request a service start.

## Validation with the fix

- Updating an existing service sets `DelayedAutoStart=0`, immediate AUTO_START,
  and recovery actions for non-crash failures.
- Existing service permissions are retained. BUILTIN Users get only QUERY_STATUS
  and START; administrator/system rights remain intact.
- After reboot: the service logged API readiness at 6.67 seconds Windows uptime
  (initialization itself took 368 ms). API health was independently verified.
- After stopping the service: launching the tray as the logged-on user with
  `Elevated=false` brought it and the API up in 876 ms, without UAC.
- With an intentionally occupied API port: `install quiet` exited with code 1;
  SCM reported STOPPED, Windows error 1066, service error 1. Reinstalling with the
  normal port restored RUNNING and a healthy API.
- The main package's compiled Go test suite passed inside Windows, including
  SCM startup heartbeats and the health endpoint's independence from engine locks.
- macOS `go test ./...` and `go vet ./...`, Windows `go vet ./...`, and Windows,
  universal macOS and Android release builds succeeded.

These are observations on one VM, not a guarantee of the same timings on every PC.
An affected PC must use the installer to migrate service settings and permissions;
replacing the executable alone does not disable delayed automatic startup.
