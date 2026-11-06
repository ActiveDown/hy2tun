package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/apernet/hysteria/core/v2/client"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/idna"
)

type provisionRequest struct {
	Host     string `json:"host"`
	SSHPort  int    `json:"ssh_port"`
	Username string `json:"username"`
	Password string `json:"password"`
	Domain   string `json:"domain"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Connect  bool   `json:"connect"`
}

var provisionUserRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]{0,31}$`)

func provisionDomain(s string) (string, error) {
	s, err := idna.Lookup.ToASCII(strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), ".")))
	if err != nil || len(s) > 253 || !strings.Contains(s, ".") || net.ParseIP(s) != nil {
		return "", errors.New(tr("Укажите домен без https:// и пути.", "Enter a domain without https:// or a path."))
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New(tr("Некорректное доменное имя.", "Invalid domain name."))
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", errors.New(tr("Некорректное доменное имя.", "Invalid domain name."))
			}
		}
	}
	return s, nil
}

func (r *provisionRequest) normalize() error {
	r.Host = strings.TrimSpace(r.Host)
	if ip := net.ParseIP(r.Host); ip != nil {
		if ip.To4() == nil {
			return errors.New(tr("Нужен IPv4-адрес сервера.", "An IPv4 server address is required."))
		}
	} else {
		host, err := provisionDomain(r.Host)
		if err != nil {
			return err
		}
		r.Host = host
	}
	if r.SSHPort == 0 {
		r.SSHPort = 22
	}
	if r.SSHPort < 1 || r.SSHPort > 65535 {
		return errors.New(tr("Некорректный порт SSH.", "Invalid SSH port."))
	}
	r.Username = strings.TrimSpace(r.Username)
	if r.Username == "" {
		r.Username = "root"
	}
	if !provisionUserRE.MatchString(r.Username) {
		return errors.New(tr("Некорректный логин SSH.", "Invalid SSH username."))
	}
	if len(r.Password) == 0 || len(r.Password) > 4096 {
		return errors.New(tr("Укажите пароль SSH.", "Enter the SSH password."))
	}
	domain, err := provisionDomain(r.Domain)
	if err != nil {
		return err
	}
	r.Domain = domain
	r.Email = strings.TrimSpace(r.Email)
	if r.Email != "" {
		a, err := mail.ParseAddress(r.Email)
		if err != nil || a.Address != r.Email || len(r.Email) > 254 || strings.ContainsAny(r.Email, "\r\n") {
			return errors.New(tr("Некорректный email.", "Invalid email address."))
		}
	}
	r.Name = strings.TrimSpace(r.Name)
	if len(r.Name) > 120 || strings.ContainsAny(r.Name, "\r\n\x00") {
		return errors.New(tr("Название сервера слишком длинное или содержит недопустимые символы.", "Invalid or too long server name."))
	}
	if r.Name == "" {
		r.Name = r.Domain
	}
	return nil
}

// Only this view is returned to the UI. SSH credentials, generated VPN secrets
// and arbitrary remote output never enter status, logs or diagnostics.
type provisionStatus struct {
	ID          string `json:"id,omitempty"`
	State       string `json:"state"`
	Stage       string `json:"stage,omitempty"`
	Error       string `json:"error,omitempty"`
	Warning     string `json:"warning,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	ServerID    string `json:"server_id,omitempty"`
}

type provisionJob struct {
	view    provisionStatus
	trust   chan struct{}
	trusted bool
	cancel  context.CancelFunc
}

type provisionManager struct {
	mu     sync.Mutex
	e      *Engine
	job    *provisionJob
	dial   func(context.Context, string) (net.Conn, error)
	verify func(context.Context, *hy2Link, net.IP) error
}

func newProvisionManager(e *Engine) *provisionManager {
	return &provisionManager{e: e, dial: e.provisionDial, verify: e.verifyProvision}
}

func (m *provisionManager) status() provisionStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil {
		return provisionStatus{State: "idle"}
	}
	return m.job.view
}

func (m *provisionManager) update(j *provisionJob, f func(*provisionStatus)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == j {
		f(&j.view)
	}
}

func (m *provisionManager) start(req provisionRequest) (provisionStatus, error) {
	if err := req.normalize(); err != nil {
		return provisionStatus{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job != nil && (m.job.view.State == "running" || m.job.view.State == "awaiting_host_key") {
		return provisionStatus{}, errors.New(tr("Уже выполняется развёртывание сервера.", "A server deployment is already in progress."))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	j := &provisionJob{view: provisionStatus{ID: newToken(), State: "running", Stage: "ssh"}, trust: make(chan struct{}), cancel: cancel}
	m.job = j
	go func() { defer cancel(); m.run(ctx, j, req) }()
	return j.view, nil
}

func (m *provisionManager) action(id string, trust, cancel bool) (provisionStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.job
	if j == nil || id == "" || j.view.ID != id {
		return provisionStatus{}, errors.New(tr("Развёртывание не найдено.", "Deployment not found."))
	}
	if cancel {
		j.cancel()
	} else if trust && j.view.State == "awaiting_host_key" && !j.trusted {
		j.trusted = true
		j.view.State = "running"
		close(j.trust)
	} else {
		return provisionStatus{}, errors.New(tr("Действие недоступно.", "Action is not available."))
	}
	return j.view, nil
}

func (m *provisionManager) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job != nil {
		m.job.cancel()
	}
}

func provisionLink(req provisionRequest, password string) string {
	u := url.URL{Scheme: "hy2", User: url.User("Admin:" + password), Host: net.JoinHostPort(req.Domain, "443"), Fragment: req.Name}
	q := url.Values{"sni": {req.Domain}}
	u.RawQuery = q.Encode()
	return u.String()
}

func (m *provisionManager) run(ctx context.Context, j *provisionJob, req provisionRequest) {
	configured, saved := false, false
	var profile Server
	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		m.update(j, func(s *provisionStatus) {
			s.State = "failed"
			s.Error = tr("Не удалось создать пароль VPN.", "Could not generate a VPN password.")
		})
		return
	}
	link := provisionLink(req, hex.EncodeToString(secret))
	save := func() error {
		if saved {
			return nil
		}
		var err error
		profile, err = m.e.store.add(link, req.Name)
		if err == nil {
			saved = true
			m.update(j, func(s *provisionStatus) { s.ServerID = profile.ID })
		}
		return err
	}
	fail := func(message string) {
		if configured && !saved {
			if err := save(); err != nil {
				message += tr(" Не удалось сохранить профиль. Пароль VPN находится в /etc/hysteria/config.yaml на сервере.", " Could not save the profile. The VPN password is in /etc/hysteria/config.yaml on the server.")
			} else {
				message += tr(" Профиль сохранён; изменения на сервере не отменены.", " The profile is saved; remote changes have not been rolled back.")
			}
		}
		m.update(j, func(s *provisionStatus) { s.State = "failed"; s.Error = message })
	}
	// Resolve once and pin the SSH connection, so name changes cannot switch the
	// target between host-key verification and authentication.
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", req.Host)
	if err != nil || len(ips) == 0 {
		fail(tr("Не удалось определить IPv4-адрес сервера.", "Could not resolve the server's IPv4 address."))
		return
	}
	ip := ips[0]
	addr := net.JoinHostPort(ip.String(), strconv.Itoa(req.SSHPort))
	conn, err := m.dial(ctx, addr)
	if err != nil {
		fail(tr("Не удалось подключиться по SSH. Проверьте адрес, порт и доступность сервера.", "Could not reach SSH. Check the address, port and server connectivity."))
		return
	}
	defer conn.Close()
	stopClose := context.AfterFunc(ctx, func() { conn.Close() })
	defer stopClose()
	conn.SetDeadline(time.Now().Add(3 * time.Minute))
	keyID := net.JoinHostPort(req.Host, strconv.Itoa(req.SSHPort))
	known, err := m.readHostKeys()
	if err != nil {
		fail(tr("Не удалось прочитать сохранённые SSH-ключи.", "Could not read saved SSH host keys."))
		return
	}
	var receivedKey, hostError string
	cfg := &ssh.ClientConfig{User: req.Username, Auth: []ssh.AuthMethod{ssh.Password(req.Password)}, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		receivedKey = string(ssh.MarshalAuthorizedKey(key))
		fingerprint := ssh.FingerprintSHA256(key)
		if old := known[keyID]; old != "" {
			if old != receivedKey {
				hostError = tr("SSH-ключ сервера изменился. Проверьте сервер перед повторным подключением.", "The SSH host key has changed. Verify the server before reconnecting.")
				return errors.New("host key changed")
			}
			return nil
		}
		m.update(j, func(s *provisionStatus) { s.State = "awaiting_host_key"; s.Fingerprint = fingerprint })
		timer := time.NewTimer(2 * time.Minute)
		defer timer.Stop()
		select {
		case <-j.trust:
			return nil
		case <-timer.C:
			hostError = tr("Время подтверждения SSH-ключа истекло.", "SSH host-key confirmation timed out.")
			return errors.New("host key timeout")
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	cc, chans, requests, err := ssh.NewClientConn(conn, addr, cfg)
	req.Password = ""
	cfg.Auth = nil
	if err != nil {
		if hostError != "" {
			fail(hostError)
		} else if ctx.Err() != nil {
			fail(tr("Развёртывание отменено или время ожидания истекло.", "Deployment cancelled or timed out."))
		} else {
			fail(tr("Ошибка SSH-аутентификации. Проверьте логин и пароль.", "SSH authentication failed. Check the username and password."))
		}
		return
	}
	conn.SetDeadline(time.Time{})
	sshClient := ssh.NewClient(cc, chans, requests)
	defer sshClient.Close()
	if known[keyID] == "" {
		known[keyID] = receivedKey
		if err := m.writeHostKeys(known); err != nil {
			fail(tr("Не удалось сохранить SSH-ключ сервера.", "Could not save the SSH host key."))
			return
		}
	}
	m.update(j, func(s *provisionStatus) { s.State = "running" })
	script, err := provisionScript(req, hex.EncodeToString(secret))
	if err != nil {
		fail(tr("Не удалось подготовить установку.", "Could not prepare installation."))
		return
	}
	sess, err := sshClient.NewSession()
	if err != nil {
		fail(tr("Не удалось открыть SSH-сеанс.", "Could not open an SSH session."))
		return
	}
	defer sess.Close()
	stdout, err := sess.StdoutPipe()
	if err != nil {
		fail(tr("Не удалось прочитать ход установки.", "Could not read installation progress."))
		return
	}
	sess.Stdin = strings.NewReader(script)
	sess.Stderr = io.Discard
	command := "bash -se"
	if req.Username != "root" {
		command = `sudo -n bash -se -- "$SSH_CONNECTION"`
	}
	if err = sess.Start(command); err != nil {
		fail(tr("Не удалось запустить установку. Нужен root или sudo без пароля.", "Could not start installation. Root or passwordless sudo is required."))
		return
	}
	ready, code := false, ""
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 16384)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "HY2TUN_CONFIGURED":
			configured = true
		case line == "HY2TUN_READY":
			ready = true
			configured = true
		case strings.HasPrefix(line, "HY2TUN_ERROR:"):
			code = strings.TrimPrefix(line, "HY2TUN_ERROR:")
		case strings.HasPrefix(line, "HY2TUN_STAGE:"):
			stage := strings.TrimPrefix(line, "HY2TUN_STAGE:")
			switch stage {
			case "preflight", "packages", "install", "configure", "firewall", "certificate":
				m.update(j, func(s *provisionStatus) { s.Stage = stage })
			}
		}
	}
	err = sess.Wait()
	if err != nil || scanner.Err() != nil || !ready {
		if ctx.Err() != nil {
			fail(tr("Развёртывание отменено или время ожидания истекло. Изменения на сервере не отменены.", "Deployment cancelled or timed out. Remote changes have not been rolled back."))
		} else {
			fail(provisionError(code))
		}
		return
	}
	// Save before testing the path from this device: a local UDP restriction must
	// not strand a successfully installed server with an unknown random password.
	m.update(j, func(s *provisionStatus) { s.Stage = "save" })
	if err := save(); err != nil {
		fail(tr("Сервер установлен, но профиль не сохранён.", "Server installed, but the profile could not be saved."))
		return
	}
	m.update(j, func(s *provisionStatus) { s.Stage = "verify" })
	l, _ := parseLink(link)
	verifyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	err = m.verify(verifyCtx, l, ip)
	cancel()
	if err != nil {
		m.update(j, func(s *provisionStatus) {
			s.Warning = tr("Сервер установлен и добавлен. Проверка VPN с этого устройства не прошла: проверьте UDP/443 и отключите другой VPN перед подключением.", "Server installed and added. The VPN test from this device failed: check UDP/443 and disconnect another VPN before connecting.")
		})
	} else if req.Connect && ctx.Err() == nil {
		m.update(j, func(s *provisionStatus) { s.Stage = "connect" })
		if err := m.e.Connect(profile.ID); err != nil {
			m.update(j, func(s *provisionStatus) {
				s.Warning = tr("Сервер добавлен. Для подключения отключите текущий VPN и выберите новый сервер.", "Server added. Disconnect the current VPN and select the new server to connect.")
			})
		}
	}
	m.update(j, func(s *provisionStatus) { s.State = "succeeded"; s.Stage = "done" })
}

func (m *provisionManager) readHostKeys() (map[string]string, error) {
	b, err := os.ReadFile(filepath.Join(m.e.dir, "ssh-host-keys.json"))
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	keys := map[string]string{}
	err = json.Unmarshal(b, &keys)
	if keys == nil {
		keys = map[string]string{}
	}
	return keys, err
}

func (m *provisionManager) writeHostKeys(keys map[string]string) error {
	if err := os.MkdirAll(m.e.dir, 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(m.e.dir, ".ssh-host-keys-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	path := filepath.Join(m.e.dir, "ssh-host-keys.json")
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	if m.e.store.secure {
		lockDownFile(path, false)
	}
	return nil
}

func (e *Engine) provisionDial(ctx context.Context, addr string) (net.Conn, error) {
	if rt, err := physicalDefaultRoute(e.opts.TunName); err == nil {
		if d, err := newDirectDialer(rt.IfIndex); err == nil {
			return d.dialTCP(ctx, addr)
		}
	}
	return (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", addr)
}

// A connected UDP socket retains platform binding/protection implemented by
// directDialer. Adapting it avoids changing system routes during the test.
type provisionPacketConn struct{ net.Conn }

func (p provisionPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n, err := p.Read(b)
	return n, p.RemoteAddr(), err
}
func (p provisionPacketConn) WriteTo(b []byte, _ net.Addr) (int, error) { return p.Write(b) }

type provisionConnFactory struct{ pc net.PacketConn }

func (p provisionConnFactory) New(net.Addr) (net.PacketConn, error) { return p.pc, nil }

func (e *Engine) verifyProvision(ctx context.Context, l *hy2Link, ip net.IP) error {
	addr := net.JoinHostPort(ip.String(), "443")
	var conn net.Conn
	var err error
	if rt, rerr := physicalDefaultRoute(e.opts.TunName); rerr == nil {
		if d, derr := newDirectDialer(rt.IfIndex); derr == nil {
			conn, err = d.dialUDP(ctx, addr)
		} else {
			err = derr
		}
	} else {
		conn, err = (&net.Dialer{}).DialContext(ctx, "udp", addr)
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	c, _, err := client.NewClient(&client.Config{ServerAddr: &net.UDPAddr{IP: ip, Port: 443}, Auth: l.Auth, TLSConfig: l.tlsConfig(), ConnFactory: provisionConnFactory{provisionPacketConn{conn}}})
	if err != nil {
		return err
	}
	defer c.Close()
	return nil
}

func provisionError(code string) string {
	switch code {
	case "root_required":
		return tr("Нужен root или пользователь с sudo без пароля.", "Root or a user with passwordless sudo is required.")
	case "unsupported_os":
		return tr("Поддерживаются Ubuntu и Debian.", "Ubuntu and Debian are supported.")
	case "systemd_required":
		return tr("Нужен сервер с systemd 245 или новее (Debian 11+ / Ubuntu 20.04+).", "The server must run systemd 245 or newer (Debian 11+ / Ubuntu 20.04+).")
	case "existing_installation":
		return tr("На сервере уже установлена Hysteria2. Существующая настройка сохранена; импортируйте её ссылку.", "Hysteria2 is already installed. The existing setup was preserved; import its link.")
	case "ports_busy":
		return tr("Порты 80 или 443 заняты. Нужен сервер со свободными портами 80/TCP, 443/TCP и 443/UDP.", "Ports 80 or 443 are busy. The server needs free 80/TCP, 443/TCP and 443/UDP.")
	case "dns_mismatch":
		return tr("Домен не указывает на IPv4 этого сервера. Исправьте DNS A-запись и повторите.", "The domain does not point to this server's IPv4. Update its DNS A record and retry.")
	case "packages_failed":
		return tr("Не удалось установить зависимости. Проверьте сеть и менеджер пакетов сервера.", "Could not install prerequisites. Check the server's network and package manager.")
	case "missing_prerequisites":
		return tr("Нужны стандартные утилиты сервера: ss, ip, getent и flock.", "Standard server tools are required: ss, ip, getent and flock.")
	case "deployment_busy":
		return tr("На сервере уже выполняется установка из другого клиента.", "Another client is already installing on this server.")
	case "disk_space":
		return tr("Для установки нужно хотя бы 150 МБ свободного места на сервере.", "At least 150 MB of free server disk space is required for installation.")
	case "configure_failed":
		return tr("Не удалось записать настройки сервера. Проверьте свободное место и /run/hy2tun-deploy.*/deploy.log на сервере.", "Could not write server configuration. Check free disk space and /run/hy2tun-deploy.*/deploy.log on the server.")

	case "install_failed":
		return tr("Не удалось установить Hysteria2. Подробности: /run/hy2tun-deploy.*/deploy.log на сервере.", "Hysteria2 installation failed. See /run/hy2tun-deploy.*/deploy.log on the server.")
	case "firewall_failed":
		return tr("Не удалось настроить firewall. Проверьте /run/hy2tun-deploy.*/deploy.log на сервере.", "Firewall configuration failed. Check /run/hy2tun-deploy.*/deploy.log on the server.")
	case "certificate_failed":
		return tr("Сертификат не выпущен. Проверьте DNS, доступ к порту 80 извне и journalctl --namespace=hy2tun -u hysteria-server на сервере.", "Certificate issuance failed. Check DNS, external access to port 80, and journalctl --namespace=hy2tun -u hysteria-server on the server.")
	default:
		return tr("Развёртывание не завершено. Нужен root или sudo без пароля; подробности: /run/hy2tun-deploy.*/deploy.log на сервере.", "Deployment did not complete. Root or passwordless sudo is required; see /run/hy2tun-deploy.*/deploy.log on the server.")
	}
}
