package main

// The admin panel: management of a deployed Hysteria2 server over the SSH
// access kept in ssh-admin.json. Operations run one at a time; every command
// is a script delivered over stdin (config data travels base64-encoded,
// exactly like server deployment). SSH passwords, stats secrets and config
// contents never enter statuses, logs or errors.

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"
	"golang.org/x/crypto/ssh"
)

const adminTrustMarker = "need_trust:"

type adminManager struct {
	e     *Engine
	mu    sync.Mutex // one SSH operation at a time
	dial  func(context.Context, string) (net.Conn, error)
	cur   *adminSSHClient // cached connection of the currently managed server
	curID string
	idle  time.Time
}

type adminSSHClient struct {
	*ssh.Client
	conn net.Conn // deadlines are renewed per command, never per connection lifetime
}

const (
	adminConnectTimeout = 15 * time.Second
	adminCommandTimeout = 30 * time.Second
)

func newAdminManager(e *Engine) *adminManager {
	return &adminManager{e: e, dial: e.provisionDial}
}

// adminStatus is returned by the status polling; Attached=false means the
// server has no saved SSH access and the UI shows the attach form.
type adminStatus struct {
	Attached bool   `json:"attached"`
	SSH      bool   `json:"ssh,omitempty"`
	Service  string `json:"service,omitempty"`
	Config   string `json:"config,omitempty"`
	Auth     string `json:"auth,omitempty"` // "userpass" when manageable
	Version  string `json:"version,omitempty"`
	Stats    bool   `json:"stats,omitempty"`
	Error    string `json:"error,omitempty"`
}

type adminUser struct {
	User   string `json:"user"`
	Owner  bool   `json:"owner,omitempty"`
	Online int    `json:"online,omitempty"`
	Tx     int64  `json:"tx,omitempty"`
	Rx     int64  `json:"rx,omitempty"`
}

type adminUsersResult struct {
	Users      []adminUser `json:"users"`
	Stats      bool        `json:"stats"`
	Auth       string      `json:"auth,omitempty"`
	StatsError string      `json:"stats_error,omitempty"`
}

type userTraffic struct {
	Tx int64 `json:"tx"`
	Rx int64 `json:"rx"`
}

func adminNewSecret(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// trErr is a localized error.
func trErr(ru, en string) error { return errors.New(tr(ru, en)) }

// adminConnectRequest is the SSH access pasted into the admin panel (or
// confirmed by the "trust" round-trip).
type adminConnectRequest struct {
	ID          string `json:"id"`
	Host        string `json:"host"`
	Port        int    `json:"ssh_port"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	Trust       bool   `json:"trust"`
	Fingerprint string `json:"fingerprint"`
}

func (r *adminConnectRequest) normalize() error {
	r.Host = strings.TrimSpace(r.Host)
	if ip := net.ParseIP(r.Host); ip != nil {
		if ip.To4() == nil {
			return errors.New(tr("Нужен IPv4-адрес сервера.", "An IPv4 server address is required."))
		}
	} else {
		s, err := provisionDomain(r.Host)
		if err != nil {
			return err
		}
		r.Host = s
	}
	if r.Port == 0 {
		r.Port = 22
	}
	if r.Port < 1 || r.Port > 65535 {
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
	return nil
}

// adminTrustNeed reports the «confirm the fingerprint» phase of attaching.
func adminTrustNeed(err error) (string, bool) {
	if s, ok := strings.CutPrefix(err.Error(), adminTrustMarker); ok {
		return s, true
	}
	return "", false
}

// adminRelErr rewords trust errors for saved credentials (the key must be
// known by now; its disappearance means the store was touched).
func adminRelErr(err error) string {
	if _, ok := adminTrustNeed(err); ok {
		return tr("SSH-ключ сервера неизвестен. Проверьте сервер и прикрепите доступ заново.",
			"The server's SSH key is unknown. Verify the server and attach the access again.")
	}
	return err.Error()
}

// connect dials SSH with the same host-key discipline as deployment: the
// address is resolved once, the key must be known, and an unknown key needs
// an explicit trust confirmation (fingerprint round-trip to the UI).
func (m *adminManager) connect(ctx context.Context, c adminCredential, trustedFingerprint string) (*adminSSHClient, error) {
	ctx, cancel := context.WithTimeout(ctx, adminConnectTimeout)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", c.Host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New(tr("Не удалось определить IPv4-адрес сервера.", "Could not resolve the server's IPv4 address."))
	}
	addr := net.JoinHostPort(ips[0].String(), strconv.Itoa(c.Port))
	conn, err := m.dial(ctx, addr)
	if err != nil {
		return nil, errors.New(tr("Не удалось подключиться по SSH. Проверьте адрес, порт и доступность сервера.",
			"Could not reach SSH. Check the address, port and server connectivity."))
	}
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	keyID := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	known, err := loadSSHHostKeys(m.e.dir)
	if err != nil {
		conn.Close()
		return nil, errors.New(tr("Не удалось прочитать сохранённые SSH-ключи.", "Could not read saved SSH host keys."))
	}
	var receivedKey, fingerprint, hostError string
	cfg := &ssh.ClientConfig{User: c.Username, Auth: []ssh.AuthMethod{ssh.Password(c.Password)}, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		receivedKey = string(ssh.MarshalAuthorizedKey(key))
		fingerprint = ssh.FingerprintSHA256(key)
		if old := known[keyID]; old != "" {
			if old != receivedKey {
				hostError = tr("SSH-ключ сервера изменился. Проверьте сервер перед повторным подключением.",
					"The SSH host key has changed. Verify the server before reconnecting.")
				return errors.New("host key changed")
			}
			return nil
		}
		if trustedFingerprint == "" {
			hostError = adminTrustMarker + fingerprint
			return errors.New("unknown host key")
		}
		if trustedFingerprint != fingerprint {
			hostError = tr("SSH-ключ не совпадает с подтверждённым отпечатком. Подключитесь заново.",
				"The SSH key does not match the confirmed fingerprint. Connect again.")
			return errors.New("host key confirmation mismatch")
		}
		return nil
	}}
	cc, chans, requests, err := ssh.NewClientConn(conn, addr, cfg)
	c.Password = ""
	cfg.Auth = nil
	if err != nil {
		conn.Close()
		if hostError != "" {
			return nil, errors.New(hostError)
		}
		if ctx.Err() != nil {
			return nil, errors.New(tr("Время ожидания истекло или операция отменена.", "Timed out or cancelled."))
		}
		return nil, errors.New(tr("Ошибка SSH-аутентификации. Проверьте логин и пароль SSH.",
			"SSH authentication failed. Check the SSH username and password."))
	}
	if trustedFingerprint != "" && known[keyID] == "" && receivedKey != "" {
		known[keyID] = receivedKey
		if err = saveSSHHostKeys(m.e.dir, known, m.e.store.secure); err != nil {
			cc.Close()
			return nil, errors.New(tr("Не удалось сохранить SSH-ключ сервера.", "Could not save the SSH host key."))
		}
	}
	conn.SetDeadline(time.Time{})
	return &adminSSHClient{Client: ssh.NewClient(cc, chans, requests), conn: conn}, nil
}

// exec runs a script over stdin and scans the result markers:
//
//	HY2TUN_ERROR:<code>  — a guarded failure, translated on the app side
//	HY2TUN_PAYLOAD       — everything after this line is the answer
func (m *adminManager) exec(cc *adminSSHClient, username, script string) (string, string, error) {
	cc.conn.SetDeadline(time.Now().Add(adminCommandTimeout))
	defer cc.conn.SetDeadline(time.Time{})
	sess, err := cc.NewSession()
	if err != nil {
		return "", "", err
	}
	defer sess.Close()
	sess.Stdin = strings.NewReader(script)
	sess.Stderr = io.Discard
	command := "bash -se"
	if username != "root" {
		command = `sudo -n bash -se -- "$SSH_CONNECTION"`
	}
	stdout, perr := sess.StdoutPipe()
	if perr != nil {
		return "", "", perr
	}
	if err = sess.Start(command); err != nil {
		log.Printf("admin: exec start failed on %s as %s: %v", cc.Conn.RemoteAddr(), username, err)
		return "", "", err
	}
	var payload strings.Builder
	code := ""
	in := false
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 4096), 512*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case in:
			payload.WriteString(line)
			payload.WriteByte('\n')
		case strings.HasPrefix(line, "HY2TUN_ERROR:"):
			code = strings.TrimPrefix(line, "HY2TUN_ERROR:")
		case line == "HY2TUN_PAYLOAD":
			in = true
		}
	}
	werr := sess.Wait()
	if sc.Err() != nil {
		werr = sc.Err()
	}
	if werr != nil {
		// the reason is plain local data (an exit status, a channel refusal) — no
		// remote output or secrets pass through
		log.Printf("admin: exec on %s as %s failed: %v", cc.Conn.RemoteAddr(), username, werr)
	}
	return payload.String(), code, werr
}

// adminSSHError explains a failing remote exec. The SSH password was already
// accepted at this point; the wording must not cast doubt on it.
func adminSSHError(err error) string {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return tr("Сервер не ответил на SSH-команду вовремя. Проверьте соединение и повторите запрос.",
			"The server did not answer the SSH command in time. Check connectivity and retry.")
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return tr("SSH-соединение оборвалось. Повторите запрос для переподключения.",
			"The SSH connection was lost. Retry to reconnect.")
	}
	detail := ""
	if e, ok := err.(*ssh.ExitError); ok {
		detail = adminExitDetail(e.ExitStatus())
	} else if err != nil {
		if _, ok := err.(*ssh.ExitMissingError); ok {
			detail = tr("соединение оборвалось до окончания команды", "the connection dropped before the command finished")
		} else {
			detail = err.Error()
		}
	}
	return tr("SSH-пароль принят, но команду на сервере выполнить не удалось ("+detail+"). Нужен полноценный shell-доступ (bash) для этого пользователя.",
		"The SSH password was accepted, but the command could not be executed ("+detail+"). A full shell access (bash) is required for this user.")
}

func adminExitDetail(status int) string {
	switch status {
	case 126, 127:
		return fmt.Sprintf(tr("код %d — bash не найден или вход ограничен", "exit code %d — bash unavailable or the login is restricted"), status)
	case 255:
		return tr("код 255 — sshd отклонил запуск команды", "exit code 255 — sshd refused to run the command")
	default:
		return fmt.Sprintf(tr("код %d", "exit code %d"), status)
	}
}

func adminOpsError(code string) string {
	switch code {
	case "config_missing":
		return tr("Hysteria2 не найден на сервере: нет /etc/hysteria/config.yaml.",
			"Hysteria2 was not found on the server: /etc/hysteria/config.yaml is missing.")
	case "config_invalid":
		return tr("Сервер не принял новую конфигурацию.", "The server rejected the new configuration.")
	case "tmp_failed":
		return tr("Не удалось создать временный файл на сервере. Проверьте свободное место.",
			"Could not create a temporary file on the server. Check free disk space.")
	case "permissions_failed":
		return tr("Не удалось выставить права файлу конфигурации.", "Could not set the configuration file permissions.")
	case "backup_failed":
		return tr("Не удалось сохранить резервную копию /etc/hysteria/config.yaml.",
			"Could not keep a backup of /etc/hysteria/config.yaml.")
	case "configure_failed":
		return tr("Не удалось записать конфигурацию на сервере.", "Could not write the configuration on the server.")
	case "restart_failed":
		return tr("Не удалось перезапустить hysteria-server. Изменения применятся при следующем рестарте.",
			"Could not restart hysteria-server. The changes apply on the next restart.")
	case "service_down":
		return tr("Служба не приняла изменения. Предыдущая конфигурация восстановлена.",
			"The service rejected the changes. The previous configuration was restored.")
	case "rollback_failed":
		return tr("Не удалось восстановить службу после изменения. Проверьте hysteria-server и резервную копию config.yaml на сервере.",
			"Could not restore the service after the change. Check hysteria-server and the config.yaml backup on the server.")
	case "stats_unavailable":
		return tr("Traffic Stats API недоступен: он не включён на сервере или порт изменён.",
			"The Traffic Stats API is unavailable: it is not enabled on the server or its port has changed.")
	default:
		return tr("Операция не выполнена на сервере: "+code, "Operation failed on the server: "+code)
	}
}

func adminCtx(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

const adminIdleKeep = 2 * time.Minute

// acquire returns a cached and still-answering connection for id, or dials a
// fresh one. Servers throttle rapid unauthenticated SSH handshakes (MaxStartups
// and hoster guards): the panel keeps one connection across poll cycles.
func (m *adminManager) acquire(id string, c adminCredential) (*adminSSHClient, error) {
	if m.cur != nil && m.curID == id && time.Since(m.idle) < adminIdleKeep {
		m.cur.conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, _, err := m.cur.SendRequest("keepalive@openssh.com", true, nil)
		m.cur.conn.SetDeadline(time.Time{})
		if err == nil {
			m.idle = time.Now()
			return m.cur, nil
		}
	}
	m.drop(m.cur)
	ctx, cancel := adminCtx(adminConnectTimeout)
	defer cancel()
	cc, err := m.connect(ctx, c, "")
	if err != nil {
		return nil, err
	}
	m.cur, m.curID, m.idle = cc, id, time.Now()
	return cc, nil
}

// drop discards the cached connection: on any failure inside an operation.
func (m *adminManager) drop(cc *adminSSHClient) {
	if cc == nil {
		return
	}
	if cc != m.cur {
		cc.Close()
		return
	}
	cc.Close()
	m.cur, m.curID = nil, ""
}

func (m *adminManager) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.drop(m.cur)
}

// withSSH runs op with a cached or fresh connection; on success the
// connection stays cached for later polls, on error it is dropped.
func (m *adminManager) withSSH(id string, c adminCredential, op func(*adminSSHClient) error) error {
	cc, err := m.acquire(id, c)
	if err != nil {
		return errors.New(adminRelErr(err))
	}
	if err = op(cc); err != nil {
		m.drop(cc)
		return err
	}
	m.idle = time.Now()
	return nil
}

func (m *adminManager) serverCert(id string) (Server, adminCredential, error) {
	srv, ok := m.e.store.getExact(id)
	if !ok {
		return Server{}, adminCredential{}, trErr("сервер не найден", "server not found")
	}
	cred, ok := m.e.admin.get(id)
	if !ok {
		return Server{}, adminCredential{}, trErr("SSH-доступ не сохранён. Прикрепите его в админ-панели.",
			"No SSH access is saved for this server. Attach it in the admin panel.")
	}
	return srv, cred, nil
}

// readConfig fetches /etc/hysteria/config.yaml and parses it.
func (m *adminManager) readConfig(cc *adminSSHClient, c adminCredential) (*yaml.Node, error) {
	payload, code, werr := m.exec(cc, c.Username, adminScriptReadConfig())
	if code != "" {
		return nil, errors.New(adminOpsError(code))
	}
	if werr != nil {
		return nil, errors.New(adminSSHError(werr))
	}
	root, err := confParse(payload)
	if err != nil {
		return nil, trErr("Не удалось разобрать config.yaml сервера.", "Could not parse the server's config.yaml.")
	}
	return root, nil
}

// probe queries the Traffic Stats API from the server itself (curl on the
// loopback) and returns the raw JSON.
func (m *adminManager) probe(cc *adminSSHClient, c adminCredential, path string) (string, error) {
	if c.StatsPort == 0 {
		return "", trErr("Статистика трафика ещё не включена.", "Traffic statistics are not enabled yet.")
	}
	payload, code, werr := m.exec(cc, c.Username, adminScriptProbe(c.StatsPort, c.StatsSecret, path))
	if code != "" {
		return "", errors.New(adminOpsError(code))
	}
	if werr != nil {
		return "", errors.New(adminSSHError(werr))
	}
	return payload, nil
}

// writeAndRestart pushes the new config: the script backs up, rewrites,
// restarts the unit and verifies that it is active again.
func (m *adminManager) writeAndRestart(cc *adminSSHClient, c adminCredential, root *yaml.Node) error {
	body, err := confEncode(root)
	if err != nil {
		return err
	}
	_, code, werr := m.exec(cc, c.Username, adminScriptWrite(base64.StdEncoding.EncodeToString(body)))
	if code != "" {
		return errors.New(adminOpsError(code))
	}
	if werr != nil {
		return errors.New(adminSSHError(werr))
	}
	return nil
}

// Status answers a poll of the panel; it never fails: errors land in the
// Error field so the UI keeps rendering the modal.
func (m *adminManager) Status(id string) adminStatus {
	st := adminStatus{Attached: true}
	cred, ok := m.e.admin.get(id)
	if !ok {
		return adminStatus{Attached: false}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.withSSH(id, cred, func(cc *adminSSHClient) error {
		st.SSH = true
		payload, code, werr := m.exec(cc, cred.Username, adminScriptStatus())
		if code != "" {
			return errors.New(adminOpsError(code))
		}
		if werr != nil {
			return errors.New(adminSSHError(werr))
		}
		meta, cfgText, _ := strings.Cut(payload, "HY2TUN_CONFIG\n")
		for _, line := range strings.Split(meta, "\n") {
			switch {
			case strings.HasPrefix(line, "service="):
				st.Service = strings.TrimPrefix(line, "service=")
			case strings.HasPrefix(line, "config="):
				st.Config = strings.TrimPrefix(line, "config=")
			case strings.HasPrefix(line, "version="):
				st.Version = strings.TrimPrefix(line, "version=")
			}
		}
		if st.Config == adminConfigMissing {
			return errors.New(adminOpsError("config_missing"))
		}
		root, err := confParse(cfgText)
		if err != nil {
			return errors.New(tr("Не удалось разобрать config.yaml сервера.", "Could not parse the server's config.yaml."))
		}
		if _, uerr := confUserPassNode(root); uerr != nil {
			return nil // password/http/command auth: nothing user-related to show
		}
		st.Auth = "userpass"
		listen, secret, enabled := confTrafficStats(root)
		st.Stats = enabled
		if enabled {
			cred.StatsPort, cred.StatsSecret = adminListenPort(listen), secret
		} else {
			cred.StatsPort, cred.StatsSecret = 0, ""
		}
		old, _ := m.e.admin.get(id)
		if old.StatsPort != cred.StatsPort || old.StatsSecret != cred.StatsSecret {
			return m.e.admin.set(id, cred)
		}
		return nil
	}); err != nil {
		st.Error = err.Error()
	}
	return st
}

// Attach verifies SSH access against the profile, confirms that the server
// runs userpass Hysteria2 and stores the credential (adopting the Traffic
// Stats data when the server already has them). The typed credentials always
// open their own connection; on success it becomes the cached one.
func (m *adminManager) Attach(req adminConnectRequest) (adminStatus, error) {
	if err := req.normalize(); err != nil {
		return adminStatus{}, err
	}
	if _, ok := m.e.store.getExact(req.ID); !ok {
		return adminStatus{}, trErr("сервер не найден", "server not found")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cred := adminCredential{Host: req.Host, Port: req.Port, Username: req.Username, Password: req.Password}
	ctx, cancel := adminCtx(adminConnectTimeout)
	defer cancel()
	fingerprint := ""
	if req.Trust {
		fingerprint = req.Fingerprint
	}
	cc, err := m.connect(ctx, cred, fingerprint)
	if err != nil {
		return adminStatus{}, err
	}
	root, err := m.readConfig(cc, cred)
	if err != nil {
		cc.Close()
		return adminStatus{}, err
	}
	if _, uerr := confUserPassNode(root); uerr != nil {
		cc.Close()
		return adminStatus{}, trErr("Панель управления доступна только для конфигурации с auth: userpass.",
			"The panel works only with the auth: userpass configuration.")
	}
	st := adminStatus{Attached: true, SSH: true, Config: adminConfigOk, Auth: "userpass"}
	if listen, secret, ok := confTrafficStats(root); ok {
		cred.StatsPort = adminListenPort(listen)
		cred.StatsSecret = secret
		st.Stats = true
	}
	if err = m.e.admin.set(req.ID, cred); err != nil {
		cc.Close()
		return adminStatus{}, err
	}
	m.drop(m.cur) // replace the cache: the verified connection is already open
	m.cur, m.curID, m.idle = cc, req.ID, time.Now()
	return st, nil
}

func (m *adminManager) Detach(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.curID == id {
		m.drop(m.cur)
	}
	return m.e.admin.remove(id)
}

// Users lists the userpass users with their online state and traffic.
func (m *adminManager) Users(id string) (adminUsersResult, error) {
	_, cred, err := m.serverCert(id)
	if err != nil {
		return adminUsersResult{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	res := adminUsersResult{}
	uerr := m.withSSH(id, cred, func(cc *adminSSHClient) error {
		root, err := m.readConfig(cc, cred)
		if err != nil {
			return err
		}
		users, uerr := confUsers(root)
		if uerr != nil {
			return trErr("Управление пользователями доступно только при auth: userpass.",
				"User management requires the auth: userpass configuration.")
		}
		res = adminUsersResult{Users: make([]adminUser, 0, len(users)), Auth: "userpass"}
		if listen, secret, enabled := confTrafficStats(root); enabled {
			cred.StatsPort, cred.StatsSecret = adminListenPort(listen), secret
		} else {
			cred.StatsPort, cred.StatsSecret = 0, ""
		}
		traffic := map[string]userTraffic{}
		online := map[string]int{}
		if cred.StatsPort != 0 {
			res.Stats = true
			if p, perr := m.probe(cc, cred, "traffic"); perr != nil || json.Unmarshal([]byte(p), &traffic) != nil {
				res.Stats = false
				res.StatsError = adminOpsError("stats_unavailable")
			}
			if p, perr := m.probe(cc, cred, "online"); perr != nil || json.Unmarshal([]byte(p), &online) != nil {
				res.Stats = false
				res.StatsError = adminOpsError("stats_unavailable")
			}
		}
		for _, u := range users {
			row := adminUser{User: u, Owner: u == adminReserveUser, Online: online[u]}
			if t, ok := traffic[u]; ok {
				row.Tx, row.Rx = t.Tx, t.Rx
			}
			res.Users = append(res.Users, row)
		}
		sort.Slice(res.Users, func(i, j int) bool {
			if res.Users[i].Owner != res.Users[j].Owner {
				return res.Users[i].Owner
			}
			return res.Users[i].User < res.Users[j].User
		})
		return nil
	})
	if uerr != nil {
		return adminUsersResult{}, uerr
	}
	return res, nil
}

func adminValidateUser(user, pass string) error {
	if user == adminReserveUser {
		return trErr("Пользователь Admin управляется через ссылку сервера.", "The Admin user is managed via the server link.")
	}
	if !provisionUserRE.MatchString(user) {
		return trErr("Логин: латиница, цифры, _ и -, до 32 символов.", "Username: latin letters, digits, _ and -, up to 32 characters.")
	}
	if len(pass) == 0 || len(pass) > 128 || strings.ContainsAny(pass, "\r\n") {
		return trErr("Пароль: от 1 до 128 символов.", "The password must be 1-128 characters.")
	}
	return nil
}

// AddUser adds a user or replaces the password of an existing one.
func (m *adminManager) AddUser(id, user, pass string) error {
	if err := adminValidateUser(user, pass); err != nil {
		return err
	}
	_, cred, err := m.serverCert(id)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.withSSH(id, cred, func(cc *adminSSHClient) error {
		root, err := m.readConfig(cc, cred)
		if err != nil {
			return err
		}
		if err = confSetUser(root, user, pass); err != nil {
			return err
		}
		return m.writeAndRestart(cc, cred, root)
	})
}

func (m *adminManager) DeleteUser(id, user string) error {
	if user == adminReserveUser {
		return trErr("Пользователь Admin является владельцем сервера и не удаляется.", "The Admin user owns the server and cannot be deleted.")
	}
	_, cred, err := m.serverCert(id)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.withSSH(id, cred, func(cc *adminSSHClient) error {
		root, err := m.readConfig(cc, cred)
		if err != nil {
			return err
		}
		if err = confRemoveUser(root, user); err != nil {
			return trErr("Пользователь не найден.", "User not found.")
		}
		return m.writeAndRestart(cc, cred, root)
	})
}

// UserLink builds the hy2:// link of one user out of the server link and the
// password stored on the server.
func (m *adminManager) UserLink(id, user string) (string, string, error) {
	srv, cred, err := m.serverCert(id)
	if err != nil {
		return "", "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	link := ""
	uerr := m.withSSH(id, cred, func(cc *adminSSHClient) error {
		root, err := m.readConfig(cc, cred)
		if err != nil {
			return err
		}
		pass, perr := confUser(root, user)
		if perr != nil {
			return trErr("Пользователь не найден.", "User not found.")
		}
		l, lerr := parseLink(srv.Link)
		if lerr != nil {
			return lerr
		}
		u := &url.URL{Scheme: "hy2", User: url.User(user + ":" + pass), Host: net.JoinHostPort(l.Host, l.Port)}
		q := url.Values{}
		q.Set("sni", l.SNI)
		if l.Ports != "" {
			u.Host = net.JoinHostPort(l.Host, l.Ports)
			q.Set("mport", l.Ports)
		}
		if l.Insecure {
			q.Set("insecure", "1")
		}
		if l.PinSHA256 != "" {
			q.Set("pinSHA256", l.PinSHA256)
		}
		if l.Obfs != "" {
			q.Set("obfs", l.Obfs)
			q.Set("obfs-password", l.ObfsPassword)
		}
		if l.HopInterval > 0 {
			q.Set("hop-interval", strconv.FormatInt(int64(l.HopInterval/time.Second), 10))
		}
		u.RawQuery = q.Encode()
		u.Fragment = l.Name
		if user != adminReserveUser {
			u.Fragment = strings.TrimSpace(l.Name + " · " + user)
		}
		link = u.String()
		return nil
	})
	if uerr != nil {
		return "", "", uerr
	}
	return srv.Name, link, nil
}

// EnableStats turns the Traffic Stats section on (a random secret plus a
// restart) or adopts the settings already present on the server.
func (m *adminManager) EnableStats(id string) error {
	_, cred, err := m.serverCert(id)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.withSSH(id, cred, func(cc *adminSSHClient) error {
		root, err := m.readConfig(cc, cred)
		if err != nil {
			return err
		}
		if listen, secret, ok := confTrafficStats(root); ok {
			cred.StatsPort = adminListenPort(listen)
			cred.StatsSecret = secret
			return m.e.admin.set(id, cred)
		}
		secret, rerr := adminNewSecret(24)
		if rerr != nil {
			return rerr
		}
		if err = confSetTrafficStats(root, fmt.Sprintf("127.0.0.1:%d", adminStatsListenPort), secret); err != nil {
			return err
		}
		if err = m.writeAndRestart(cc, cred, root); err != nil {
			return err
		}
		cred.StatsPort = adminStatsListenPort
		cred.StatsSecret = secret
		return m.e.admin.set(id, cred)
	})
}

func (m *adminManager) ResetStats(id string) error {
	_, cred, err := m.serverCert(id)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.withSSH(id, cred, func(cc *adminSSHClient) error {
		_, err := m.probe(cc, cred, "traffic?clear=1")
		return err
	})
}

func adminListenPort(listen string) int {
	if i := strings.LastIndexByte(listen, ':'); i >= 0 {
		if p, err := strconv.Atoi(listen[i+1:]); err == nil && p > 0 {
			return p
		}
	}
	return adminStatsListenPort
}

/* ---- remote scripts: data reaches them base64-encoded through stdin ---- */

const adminScriptHeader = `set -uo pipefail
umask 077
fail() { printf 'HY2TUN_ERROR:%s\n' "$1"; exit 1; }
`

func adminScriptPing() string {
	return adminScriptHeader + `
printf 'HY2TUN_PAYLOAD\n'
echo pong
`
}

func adminScriptStatus() string {
	return adminScriptHeader + `
printf 'HY2TUN_PAYLOAD\n'
service=$(systemctl is-active hysteria-server.service 2>/dev/null || true)
[ -n "$service" ] || service=unknown
echo "service=$service"
if [ -f /etc/hysteria/config.yaml ]; then
  echo "config=config_ok"
else
  echo "config=config_missing"
fi
binver=$(hysteria version 2>/dev/null | head -n1 || true)
echo "version=$binver"
printf 'HY2TUN_CONFIG\n'
if [ -f /etc/hysteria/config.yaml ]; then
  cat /etc/hysteria/config.yaml
fi
`
}

func adminScriptReadConfig() string {
	return adminScriptHeader + `
[ -f /etc/hysteria/config.yaml ] || fail config_missing
printf 'HY2TUN_PAYLOAD\n'
cat /etc/hysteria/config.yaml
`
}

func adminScriptWrite(configB64 string) string {
	return adminScriptHeader + `
[ -f /etc/hysteria/config.yaml ] || fail config_missing
tmp=$(mktemp /run/hy2tun-admin.XXXXXX) || fail tmp_failed
trap 'rm -f "$tmp"' EXIT
base64 -d > "$tmp" <<'HY2TUN_CONFIG'
` + configB64 + `
HY2TUN_CONFIG
if getent group hysteria >/dev/null 2>&1; then
  chown root:hysteria "$tmp" && chmod 640 "$tmp" || fail permissions_failed
else
  chown root:root "$tmp" && chmod 600 "$tmp" || fail permissions_failed
fi
backup=$(mktemp /etc/hysteria/config.yaml.bak-XXXXXXXX) || fail backup_failed
cp -p /etc/hysteria/config.yaml "$backup" || fail backup_failed
mv "$tmp" /etc/hysteria/config.yaml || fail configure_failed
rollback() {
  cp -p "$backup" /etc/hysteria/config.yaml || fail rollback_failed
  systemctl restart hysteria-server.service || fail rollback_failed
  sleep 1
  systemctl is-active --quiet hysteria-server.service || fail rollback_failed
  fail service_down
}
systemctl restart hysteria-server.service || rollback
sleep 1
systemctl is-active --quiet hysteria-server.service || rollback
printf 'HY2TUN_PAYLOAD\n'
echo ok
`
}

func adminScriptProbe(port int, secret, path string) string {
	return adminScriptHeader + fmt.Sprintf(`
secret=$(base64 -d <<'HY2TUN_SECRET'
%s
HY2TUN_SECRET
)
out=$(curl -fsS -m 5 -H "Authorization: $secret" "http://127.0.0.1:%d/%s") || fail stats_unavailable
printf 'HY2TUN_PAYLOAD\n'
echo "$out"
`, base64.StdEncoding.EncodeToString([]byte(secret)), port, path)
}

/* ---- API handlers (the routes live in api.go) ---- */

func (a *apiServer) adminStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.e.store.getExact(r.URL.Query().Get("id")); !ok {
		http.Error(w, tr("сервер не найден", "server not found"), http.StatusNotFound)
		return
	}
	writeJSON(w, a.admin.Status(r.URL.Query().Get("id")))
}

func (a *apiServer) adminAttach(w http.ResponseWriter, r *http.Request) {
	var body adminConnectRequest
	if !readJSON(w, r, &body, 8<<10) {
		return
	}
	st, err := a.admin.Attach(body)
	if err != nil {
		if fp, ok := adminTrustNeed(err); ok {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusConflict)
			writeJSON(w, map[string]any{"awaiting_host_key": true, "fingerprint": fp})
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, st)
}

func (a *apiServer) adminDetach(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &body, 1024) {
		return
	}
	if err := a.admin.Detach(body.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *apiServer) adminUsers(w http.ResponseWriter, r *http.Request) {
	res, err := a.admin.Users(r.URL.Query().Get("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, res)
}

func (a *apiServer) adminUserAdd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID       string `json:"id"`
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body, 8<<10) {
		return
	}
	if err := a.admin.AddUser(body.ID, body.User, body.Password); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *apiServer) adminUserDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID   string `json:"id"`
		User string `json:"user"`
	}
	if !readJSON(w, r, &body, 4<<10) {
		return
	}
	if err := a.admin.DeleteUser(body.ID, body.User); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *apiServer) adminUserLink(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID   string `json:"id"`
		User string `json:"user"`
	}
	if !readJSON(w, r, &body, 4<<10) {
		return
	}
	name, link, err := a.admin.UserLink(body.ID, body.User)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, map[string]string{"name": name, "link": link})
}

func (a *apiServer) adminStatsEnable(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &body, 1024) {
		return
	}
	if err := a.admin.EnableStats(body.ID); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (a *apiServer) adminStatsReset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &body, 1024) {
		return
	}
	if err := a.admin.ResetStats(body.ID); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
