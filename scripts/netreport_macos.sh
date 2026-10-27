#!/bin/bash
# Read-only network snapshot for macOS: proxies, DNS, routes, TUN interfaces,
# launchd jobs, listening ports and a few live connectivity probes.
# Nothing is changed and no passwords are collected.
#
#   bash netreport_macos.sh [label]
#
# Writes ~/Desktop/netreport-<label>-<time>.txt and prints the path.

label="${1:-snapshot}"
out="$HOME/Desktop/netreport-${label}-$(date +%Y%m%d-%H%M%S).txt"

sec() { printf '\n\n===== %s =====\n' "$*" >>"$out"; }
run() {
  printf '\n$ %s\n' "$*" >>"$out"
  "$@" >>"$out" 2>&1
  printf '[exit %s]\n' "$?" >>"$out"
}
sh_() {
  printf '\n$ %s\n' "$1" >>"$out"
  bash -c "$1" >>"$out" 2>&1
  printf '[exit %s]\n' "$?" >>"$out"
}

: >"$out"
{
  echo "label: $label"
  echo "date:  $(date '+%Y-%m-%d %H:%M:%S %z')"
  echo "user:  $(id -un)"
} >>"$out"
run sw_vers

sec "Network services"
run networksetup -listallnetworkservices
run networksetup -listnetworkserviceorder
networksetup -listallnetworkservices 2>/dev/null | tail -n +2 | sed 's/^\*//' | while IFS= read -r s; do
  sec "Service: $s"
  for q in -getinfo -getwebproxy -getsecurewebproxy -getsocksfirewallproxy \
           -getautoproxyurl -getproxyautodiscovery -getftpproxy -getstreamingproxy \
           -getdnsservers -getsearchdomains -getproxybypassdomains; do
    printf '\n$ networksetup %s "%s"\n' "$q" "$s" >>"$out"
    networksetup "$q" "$s" >>"$out" 2>&1
  done
done

sec "System proxy as seen by apps (scutil --proxy)"
run scutil --proxy

sec "Environment proxies (credentials stripped)"
sh_ "env | grep -i -E '(^|_)(http|https|all|no|socks|ftp)_proxy=' | sed -E 's#(://)[^/@]*@#\\1***@#'"

sec "DNS"
run scutil --dns
run scutil --nwi
sh_ "ls -la /etc/resolver 2>&1; for f in /etc/resolver/*; do [ -f \"\$f\" ] && { echo \"--- \$f\"; cat \"\$f\"; }; done"
sh_ "grep -v '^[[:space:]]*#' /etc/hosts | sed '/^[[:space:]]*\$/d'"

sec "Interfaces"
run ifconfig -l
run ifconfig
sh_ "ipconfig getsummary en0 | head -40"

sec "Routes"
run netstat -rn -f inet
sh_ "netstat -rn -f inet6 | head -40"
run route -n get default
run route -n get 1.1.1.1
run route -n get 8.8.8.8

sec "configd state (hy2tun and other VPN services)"
sh_ "echo 'list State:/Network/Service/.*' | scutil | head -60"
sh_ "printf 'show State:/Network/Service/hy2tun/DNS\nshow State:/Network/Service/hy2tun/IPv4\nshow State:/Network/Global/IPv4\nshow State:/Network/Global/DNS\nshow State:/Network/Global/Proxies\n' | scutil"
run scutil --nc list
run systemextensionsctl list

sec "VPN / proxy software"
sh_ "ps -axo pid,user,etime,command | grep -i -E 'neko|sing-box|singbox|clash|mihomo|v2ray|xray|hysteria|hy2tun|wireguard|openvpn|tailscale|amnezia|shadowsocks|proxy|tun2socks|checkpoint|trac' | grep -v grep | cut -c1-220"
sh_ "launchctl list | grep -i -E 'neko|sing|clash|mihomo|v2ray|xray|hy2|wireguard|openvpn|tailscale|amnezia|proxy|vpn'"
sh_ "ls -la /Library/LaunchDaemons /Library/LaunchAgents \"\$HOME/Library/LaunchAgents\" 2>&1 | grep -i -E 'neko|sing|clash|v2ray|hy2|wireguard|openvpn|tailscale|amnezia|proxy|vpn|total|:\$'"
sh_ "ls -la /Applications | grep -i -E 'neko|clash|v2ray|sing|hy2|wireguard|openvpn|tailscale|amnezia|surge|proxy|vpn'"

sec "Listening ports"
sh_ "lsof -nP -iTCP -sTCP:LISTEN 2>/dev/null | awk 'NR==1 || /127.0.0.1|\\*:|\\[::/' | cut -c1-160 | head -60"

sec "Hy2Tun service"
sh_ "ls -la /usr/local/bin/hy2tun /Library/LaunchDaemons/com.activedown.hy2tun.plist '/Library/Application Support/Hy2Tun' \"\$HOME/Library/Application Support/Hy2Tun\" 2>&1"
sh_ "launchctl print system/com.activedown.hy2tun 2>&1 | head -30"
sh_ "for f in '/Library/Application Support/Hy2Tun'/*.log \"\$HOME/Library/Application Support/Hy2Tun\"/*.log; do [ -f \"\$f\" ] && { echo \"--- \$f\"; tail -n 60 \"\$f\"; }; done"
sh_ "curl -s --noproxy '*' --max-time 3 http://127.0.0.1:7880/api/state | head -c 600; echo"

sec "Live probes (each one bypasses the system proxy; second set uses it)"
sh_ "ping -c 2 -t 4 1.1.1.1"
sh_ "ping -c 2 -t 4 8.8.8.8"
sh_ "ping -c 2 -t 4 \$(route -n get default 2>/dev/null | awk '/gateway/{print \$2}')"
sh_ "dscacheutil -q host -a name example.com"
sh_ "dig +time=3 +tries=1 @1.1.1.1 example.com +short"
sh_ "dig +time=3 +tries=1 example.com +short"
sh_ "curl -sS --noproxy '*' --max-time 6 -o /dev/null -w 'ip   1.1.1.1  http=%{http_code} t=%{time_total}s\n' https://1.1.1.1"
sh_ "curl -sS --noproxy '*' --max-time 6 -o /dev/null -w 'name example.com http=%{http_code} ip=%{remote_ip} t=%{time_total}s\n' https://example.com"
sh_ "curl -sS --noproxy '*' --max-time 6 -o /dev/null -w 'name google.com http=%{http_code} ip=%{remote_ip} t=%{time_total}s\n' https://www.google.com"
sh_ "curl -sS --noproxy '*' --max-time 6 http://captive.apple.com | head -c 200"
sh_ "curl -sS --max-time 6 -o /dev/null -w 'system-proxy example.com http=%{http_code} t=%{time_total}s\n' https://example.com"
sh_ "curl -sS --noproxy '*' --max-time 6 -o /dev/null -w 'ipv6 google http=%{http_code}\n' -6 https://www.google.com"

sec "Packet filter / firewall (needs no root where possible)"
sh_ "/usr/libexec/ApplicationFirewall/socketfilterfw --getglobalstate 2>&1"
sh_ "sudo -n pfctl -s info 2>&1 | head -12"

echo "done" >>"$out"
echo "$out"
