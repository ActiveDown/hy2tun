package main

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

//go:embed provision_site.html
var provisionSiteHTML string

// provisionScript is sent on SSH stdin, never in a command line. User-supplied
// configuration is data: JSON strings are valid YAML scalars and the complete
// file is transported as base64, outside the shell's expansion rules.
func provisionScript(req provisionRequest, vpnPassword string) (string, error) {
	if req.Domain == "" || vpnPassword == "" || req.SSHPort < 1 || req.SSHPort > 65535 {
		return "", errors.New("incomplete deployment configuration")
	}
	scalar := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	config := fmt.Sprintf(`listen: 0.0.0.0:443
acme:
  type: http
  ca: letsencrypt
  domains:
    - %s
  email: %s
  dir: /var/lib/hysteria/acme
auth:
  type: userpass
  userpass:
    Admin: %s
masquerade:
  type: file
  file:
    dir: /var/www/masq
  listenHTTP: :80
  listenHTTPS: :443
  forceHTTPS: true
`, scalar(req.Domain), scalar(req.Email), scalar(vpnPassword))
	replacements := strings.NewReplacer(
		"@@DOMAIN@@", provisionShellQuote(req.Domain),
		"@@SSH_PORT@@", fmt.Sprint(req.SSHPort),
		"@@CONFIG@@", base64.StdEncoding.EncodeToString([]byte(config)),
		"@@SITE@@", base64.StdEncoding.EncodeToString([]byte(provisionSiteHTML)),
	)
	return replacements.Replace(provisionScriptTemplate), nil
}

func provisionShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

const provisionScriptTemplate = `set -euo pipefail
umask 077
exec 3>&1
stage() { printf 'HY2TUN_STAGE:%s\n' "$1" >&3; }
fail() { printf 'HY2TUN_ERROR:%s\n' "$1" >&3; exit 1; }
failure=unexpected
trap 'fail "$failure"' ERR
stage preflight
[ "$(id -u)" = 0 ] || fail root_required
[ -r /etc/os-release ] || fail unsupported_os
. /etc/os-release
case "$ID" in debian|ubuntu) ;; *) fail unsupported_os ;; esac
[ -d /run/systemd/system ] && command -v systemctl >/dev/null || fail systemd_required
systemd_version=$(systemctl --version | awk 'NR==1 {print $2}')
[ "$systemd_version" -ge 245 ] || fail systemd_required
# Never replace a server installed by the user or by a previous attempt.
if [ -e /etc/hysteria ] || [ -L /etc/hysteria ] ||
   [ -e /var/www/masq ] || [ -L /var/www/masq ] || command -v hysteria >/dev/null ||
   systemctl cat hysteria-server.service >/dev/null 2>&1; then
  fail existing_installation
fi
for required in ss ip getent flock; do command -v "$required" >/dev/null || fail missing_prerequisites; done
# Reserve headroom for package lists, prerequisites and the ~24 MB binary.
available_kb=$(df -Pk / | awk 'NR==2 {print $4}')
[ "$available_kb" -ge 153600 ] || fail disk_space
exec 9>/run/hy2tun-deploy.lock
flock -n 9 || fail deployment_busy
# Recheck after taking the lock (another client may have just completed).
[ ! -e /etc/hysteria ] && [ ! -L /etc/hysteria ] &&
  [ ! -e /var/www/masq ] && [ ! -L /var/www/masq ] || fail existing_installation
if [ -n "$(ss -H -ltn 'sport = :80 or sport = :443')" ] ||
   [ -n "$(ss -H -lun 'sport = :443')" ]; then
  fail ports_busy
fi
domain=@@DOMAIN@@
ssh_port=@@SSH_PORT@@
local_ips=$(ip -o -4 addr show | awk '{split($4,a,"/"); print a[1]}')
domain_ips=$(getent ahostsv4 "$domain" | awk '{print $1}' | sort -u) || fail dns_mismatch
[ -n "$domain_ips" ] || fail dns_mismatch
for address in $domain_ips; do
  printf '%s\n' "$local_ips" | grep -Fxq "$address" || fail dns_mismatch
done
# This log stays on the server, readable only by root. Never return installer,
# package manager or journal output to the browser (it may contain secrets).
log_dir=$(mktemp -d /run/hy2tun-deploy.XXXXXX)
exec >"$log_dir/deploy.log" 2>&1
installer=''
ready=false
cleanup() {
  rm -f "$installer"
  apt-get clean >/dev/null 2>&1 || true
  rm -rf /var/lib/apt/lists/*
  if [ "$ready" = true ]; then rm -rf "$log_dir"; fi
}
trap cleanup EXIT
stage install
failure=packages_failed
export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a
apt-get -o DPkg::Lock::Timeout=120 update </dev/null
apt-get -o DPkg::Lock::Timeout=120 install -y --no-install-recommends ca-certificates curl ufw </dev/null
failure=install_failed
installer=$(mktemp)
curl --proto '=https' --tlsv1.2 -fsSL --retry 2 --connect-timeout 15 --max-time 120 https://get.hy2.sh/ -o "$installer"
HYSTERIA_USER=hysteria bash "$installer" --version v2.13.0 </dev/null
stage configure
failure=configure_failed
install -d -m 755 /var/www/masq
base64 -d > /var/www/masq/index.html <<'HY2TUN_HTML'
@@SITE@@
HY2TUN_HTML
chmod 644 /var/www/masq/index.html
base64 -d > /etc/hysteria/config.yaml <<'HY2TUN_CONFIG'
@@CONFIG@@
HY2TUN_CONFIG
chown root:hysteria /etc/hysteria/config.yaml
chmod 640 /etc/hysteria/config.yaml
install -d -o hysteria -g hysteria -m 700 /var/lib/hysteria/acme
# Keep this service's journals in RAM, bounded independently of OS logs.
install -d -m 755 /etc/systemd/system/hysteria-server.service.d
cat > /etc/systemd/system/hysteria-server.service.d/logging.conf <<'HY2TUN_UNIT'
[Service]
LogNamespace=hy2tun
LogRateLimitIntervalSec=30s
LogRateLimitBurst=30
HY2TUN_UNIT
cat > /etc/systemd/journald@hy2tun.conf <<'HY2TUN_JOURNAL'
[Journal]
Storage=volatile
RuntimeMaxUse=2M
RuntimeMaxFileSize=512K
HY2TUN_JOURNAL
printf 'HY2TUN_CONFIGURED\n' >&3
stage firewall
failure=firewall_failed
# Preserve SSH even when the public endpoint is forwarded to a different port.
actual_ssh_port=$(printf '%s\n' "${SSH_CONNECTION:-${1:-}}" | awk '{print $4}')
case "$actual_ssh_port" in ''|*[!0-9]*) ;; *) ufw allow "$actual_ssh_port/tcp" ;; esac
ufw allow "$ssh_port/tcp"
ufw allow 80/tcp
ufw allow 443/tcp
ufw allow 443/udp
ufw --force enable
stage certificate
failure=certificate_failed
systemctl daemon-reload
systemctl enable --now hysteria-server.service
# Verify a trusted certificate for this exact domain on the local service, not
# a cached or unrelated website. Hysteria manages subsequent renewals itself.
ready=false
deadline=$((SECONDS + 150))
while [ "$SECONDS" -lt "$deadline" ]; do
  if systemctl is-active --quiet hysteria-server.service &&
     curl --noproxy '*' -fsS --max-time 3 --resolve "$domain:443:127.0.0.1" "https://$domain/" >/dev/null; then
    ready=true
    break
  fi
  sleep 2
done
[ "$ready" = true ] || fail certificate_failed
systemctl is-enabled --quiet hysteria-server.service
printf 'HY2TUN_READY\n' >&3
`
