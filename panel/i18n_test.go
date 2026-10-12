// Messages du serveur dans la langue de l'interface (en-tête X-Lang).

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func badLogin(c *client, headers map[string]string) response {
	h := map[string]string{"X-CSRF-Token": c.token()}
	for k, v := range headers {
		h[k] = v
	}
	return c.do("POST", "/api/login", J{"username": "admin", "password": "mauvais"}, h)
}

func TestMessagesFollowXLang(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	c := e.client()
	if m := badLogin(c, map[string]string{"X-Lang": "fr"}).json()["msg"]; m != "Identifiants incorrects" {
		t.Fatal(m)
	}
	if m := badLogin(c, map[string]string{"X-Lang": "en"}).json()["msg"]; m != "Wrong username or password" {
		t.Fatal(m)
	}
}

func TestAcceptLanguageFallback(t *testing.T) {
	e := setup(t)
	e.setPassword(testPassword, "admin", false)
	for _, al := range []string{"en-US,en;q=0.9", "de-DE,de;q=0.9"} {
		if m := badLogin(e.client(), map[string]string{"Accept-Language": al}).json()["msg"]; m != "Wrong username or password" {
			t.Fatalf("%s : %v", al, m)
		}
	}
	if m := badLogin(e.client(), map[string]string{"Accept-Language": "fr-FR,fr;q=0.9,en;q=0.8"}).json()["msg"]; m != "Identifiants incorrects" {
		t.Fatal(m)
	}
}

func TestSetupValidationInEnglish(t *testing.T) {
	e := setup(t)
	c := e.client()
	r := c.do("POST", "/api/setup", J{"username": "admin", "password": "court"}, map[string]string{"X-CSRF-Token": c.token(), "X-Lang": "en"})
	if r.code != 400 || !strings.Contains(r.json()["msg"].(string), "at least 12 characters") {
		t.Fatalf("%d %s", r.code, r.text())
	}
}

func TestInvalidTOMLMessageInEnglish(t *testing.T) {
	e := setup(t)
	conf := filepath.Join(e.dir, "frp")
	os.MkdirAll(conf, 0o755)
	configSearchPaths = []string{conf}
	ok, msg, code := EN.writeConfigFile(filepath.Join(conf, "frpc.toml"), "serverAddr = \"x\n")
	if ok || code != 400 || !strings.HasPrefix(msg, "Invalid TOML") {
		t.Fatalf("%v %d %s", ok, code, msg)
	}
}

func TestFirewallRuleErrorsTranslated(t *testing.T) {
	setup(t)
	_, err := EN.fwNormalizeRule(map[string]any{"mode": "block", "ports": "7000", "sources": []any{}}, 0)
	if err == nil || err.Error() != "Rule 1: enter at least one address to block" {
		t.Fatalf("%v", err)
	}
}

func TestPortLabelsTranslated(t *testing.T) {
	items := []map[string]any{{"label": "Connexion des clients frpc"}, {"label": "Port « ssh »"}, {"label": "KCP"}, {"label": "Visiteur db (bindPort)"}}
	var got []any
	for _, i := range EN.withPortLabels(items) {
		got = append(got, i["label"])
	}
	if !reflect.DeepEqual(got, []any{"frpc client connections", "Port “ssh”", "KCP", "Visitor db (bindPort)"}) {
		t.Fatalf("%v", got)
	}
	if !reflect.DeepEqual(FR.withPortLabels(items), items) {
		t.Fatal("français modifié")
	}
}

func TestSystemLang(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "fr_FR.UTF-8")
	if systemLang() != FR {
		t.Fatal("fr")
	}
	t.Setenv("LANG", "C.UTF-8")
	if systemLang() != EN {
		t.Fatal("en")
	}
}
