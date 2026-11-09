package main

// Editing of /etc/hysteria/config.yaml over SSH. The file is parsed into a
// yaml.Node tree and written back as a whole: only the sections the admin
// panel owns (auth.userpass, trafficStats) are touched, everything else —
// including comments and key order — is preserved.

import (
	"errors"
	"go.yaml.in/yaml/v3"
	"strings"
)

// adminReserveUser is the owner whose password lives inside the shared
// hy2:// link; the admin panel must not rename, add or remove it.
const adminReserveUser = "Admin"

const (
	adminConfigOk        = "config_ok"
	adminConfigMissing   = "config_missing"
	adminServiceUnknown  = "unknown"
	adminStatsListenPort = 9765 // 127.0.0.1 listener written by the deploy script
)

func confTopMapping(root *yaml.Node) (*yaml.Node, error) {
	if root == nil || root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil, errors.New("yaml: the document is empty")
	}
	m := root.Content[0]
	if m.Kind != yaml.MappingNode {
		return nil, errors.New("yaml: the top level is not a mapping")
	}
	return m, nil
}

// confMapValue returns the value node of key (nil when absent).
func confMapValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Kind == yaml.ScalarNode && m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// confMapUpsert replaces value in place or appends the pair at the end.
func confMapUpsert(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Kind == yaml.ScalarNode && m.Content[i].Value == key {
			m.Content[i+1] = value
			return
		}
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

func confStrNode(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

// confUserPassNode returns the auth.userpass mapping. An error means Hysteria
// runs password/http/command authentication and cannot be managed here.
func confUserPassNode(root *yaml.Node) (*yaml.Node, error) {
	m, err := confTopMapping(root)
	if err != nil {
		return nil, err
	}
	auth := confMapValue(m, "auth")
	if auth == nil || auth.Kind != yaml.MappingNode {
		return nil, errors.New("auth: section is missing")
	}
	t := confMapValue(auth, "type")
	if t == nil || t.Kind != yaml.ScalarNode || t.Value != "userpass" {
		return nil, errors.New("auth: only userpass is supported by the admin panel")
	}
	up := confMapValue(auth, "userpass")
	if up == nil || up.Kind != yaml.MappingNode {
		return nil, errors.New("auth: the userpass map is missing")
	}
	return up, nil
}

// confUsers lists the userpass users in file order.
func confUsers(root *yaml.Node) ([]string, error) {
	up, err := confUserPassNode(root)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(up.Content)/2)
	for i := 0; i < len(up.Content); i += 2 {
		out = append(out, up.Content[i].Value)
	}
	return out, nil
}

// confUser returns the password of one user.
func confUser(root *yaml.Node, user string) (string, error) {
	up, err := confUserPassNode(root)
	if err != nil {
		return "", err
	}
	for i := 0; i+1 < len(up.Content); i += 2 {
		if up.Content[i].Value == user {
			return up.Content[i+1].Value, nil
		}
	}
	return "", errors.New("user not found")
}

// confSetUser adds a user or replaces its password. The reserved owner keeps
// its password out of the panel: it lives in the shared hy2:// link.
func confSetUser(root *yaml.Node, user, pass string) error {
	if user == adminReserveUser {
		return errors.New("the Admin user is managed through the server link")
	}
	if !provisionUserRE.MatchString(user) {
		return errors.New("the username is invalid (latin letters, digits, _ and -)")
	}
	if len(pass) == 0 || len(pass) > 128 || strings.ContainsAny(pass, "\r\n") {
		return errors.New("the password length must be 1-128")
	}
	up, err := confUserPassNode(root)
	if err != nil {
		return err
	}
	confMapUpsert(up, user, confStrNode(pass))
	return nil
}

// confRemoveUser deletes a user. Deleting the owner is rejected: the shared
// link profile would stop working.
func confRemoveUser(root *yaml.Node, user string) error {
	if user == adminReserveUser {
		return errors.New("the Admin user cannot be deleted")
	}
	up, err := confUserPassNode(root)
	if err != nil {
		return err
	}
	for i := 0; i+1 < len(up.Content); i += 2 {
		if up.Content[i].Value == user {
			up.Content = append(up.Content[:i], up.Content[i+2:]...)
			return nil
		}
	}
	return errors.New("user not found")
}

// confTrafficStats reads the Traffic Stats section: listen address and secret.
func confTrafficStats(root *yaml.Node) (listen, secret string, ok bool) {
	m, err := confTopMapping(root)
	if err != nil {
		return "", "", false
	}
	ts := confMapValue(m, "trafficStats")
	if ts == nil || ts.Kind != yaml.MappingNode {
		return "", "", false
	}
	if n := confMapValue(ts, "listen"); n != nil && n.Kind == yaml.ScalarNode {
		listen = n.Value
	}
	if n := confMapValue(ts, "secret"); n != nil && n.Kind == yaml.ScalarNode {
		secret = n.Value
	}
	return listen, secret, true
}

// confSetTrafficStats adds or replaces the Traffic Stats section.
func confSetTrafficStats(root *yaml.Node, listen, secret string) error {
	m, err := confTopMapping(root)
	if err != nil {
		return err
	}
	ts := &yaml.Node{Kind: yaml.MappingNode}
	ts.Content = []*yaml.Node{
		confStrNode("listen"), confStrNode(listen),
		confStrNode("secret"), confStrNode(secret),
	}
	confMapUpsert(m, "trafficStats", ts)
	return nil
}

// confEncode serializes the document back into text.
func confEncode(root *yaml.Node) ([]byte, error) {
	return yaml.Marshal(root)
}

// confParse decodes config text into the editable document.
func confParse(data string) (*yaml.Node, error) {
	if len(data) > 256*1024 {
		return nil, errors.New("config is too large")
	}
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(data), &root); err != nil {
		return nil, err
	}
	if _, err := confTopMapping(&root); err != nil {
		return nil, err
	}
	return &root, nil
}
