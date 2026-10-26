// Création d'instances : noms, réécriture des configs (journal, ports),
// unités systemd, configs sans service, permissions, mode autonome.
// Instance creation: names, config rewriting (log, ports), systemd units,
// configs without a service, permissions, standalone mode.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNewInstanceID(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"frpc", "maison"}: "frpc-maison", {"frpc", " Maison "}: "frpc-maison", {"frps", "vps_2"}: "frps-vps_2",
		{"frpc", "a-b"}: "frpc-a-b", {"frpc", "0"}: "frpc-0",
		{"frpc", ""}: "", {"frpc", "-x"}: "", {"frpc", "_x"}: "", {"frpc", "a.b"}: "", {"frpc", "a@b"}: "",
		{"frpc", "a b"}: "", {"frpc", "../x"}: "", {"frpc", "x;reboot"}: "", {"frpc", "x$(id)"}: "",
		{"frpc", "maison\n"}: "frpc-maison", {"frpc", "é"}: "", {"frpc", strings.Repeat("a", 33)}: "",
		{"sshd", "x"}: "", {"frp", "x"}: "",
	} {
		if got := newInstanceID(in[0], in[1]); got != want {
			t.Errorf("%q %q : %q, attendu %q", in[0], in[1], got, want)
		}
	}
}

func TestSetTOMLKey(t *testing.T) {
	cases := []struct{ in, key, value, want string }{
		// Clé à points existante / Existing dotted key
		{"log.to = \"a\"\nlog.level = \"info\"\n", "log.to", `"b"`, "log.to = \"b\"\nlog.level = \"info\"\n"},
		// Clé dans sa table / Key inside its table
		{"[webServer]\naddr = \"x\"\nport = 7400 # admin\n", "webServer.port", "7401", "[webServer]\naddr = \"x\"\nport = 7401\n"},
		// Table existante sans la clé / Existing table without the key
		{"x = 1\n\n[log]\nlevel = \"info\"\n", "log.to", `"c"`, "x = 1\n\n[log]\nto = \"c\"\nlevel = \"info\"\n"},
		// Ni clé ni table : avant la première table / Neither key nor table: before the first table
		{"a = 1\n\n# Tunnels\n[[proxies]]\nname = \"x\"\n", "bindPort", "9", "a = 1\nbindPort = 9\n\n# Tunnels\n[[proxies]]\nname = \"x\"\n"},
		// Pas dans une boucle de modèle frp / Not inside an frp template loop
		{"a = 1\n{{- range $_, $v := parseNumberRangePair \"1-2\" \"3-4\" }}\n[[proxies]]\n", "log.to", `"d"`,
			"a = 1\nlog.to = \"d\"\n{{- range $_, $v := parseNumberRangePair \"1-2\" \"3-4\" }}\n[[proxies]]\n"},
		// Fin de fichier / End of file
		{"a = 1\n", "bindPort", "9", "a = 1\nbindPort = 9\n"},
		// Jamais dans un [[tableau]] / Never inside an [[array]]
		{"[[visitors]]\nbindPort = 6000\n", "bindPort", "9", "bindPort = 9\n[[visitors]]\nbindPort = 6000\n"},
	}
	for _, c := range cases {
		if got := setTOMLKey(c.in, c.key, c.value); got != c.want {
			t.Errorf("%s dans %q :\n%q\nattendu\n%q", c.key, c.in, got, c.want)
		}
	}
}

const clientWithTunnels = `# frpc.toml — généré par FRP Manager

serverAddr = "vps.example.net"
serverPort = 7000
includes = [
  "./confd/*.toml",
]
start = ["ssh"]

[auth]
token = "secret"

[webServer]
addr = "127.0.0.1"
port = 7400

[[proxies]]
name = "ssh"
type = "tcp"
localPort = 22
remotePort = 6000

[proxies.transport]
useEncryption = true

{{- range $_, $v := parseNumberRangePair "7000-7001" "8000-8001" }}
[[proxies]]
name = "r-{{ $v.First }}"
type = "tcp"
localPort = {{ $v.First }}
remotePort = {{ $v.Second }}
{{- end }}

[[visitors]]
name = "v"
bindPort = 9000

[log]
level = "info"
to = "/var/log/frp/frpc.log"
`

func TestClientConnectionOnly(t *testing.T) {
	got := clientConnectionOnly(clientWithTunnels)
	for _, gone := range []string{"proxies", "visitors", "remotePort", "{{", "includes", "confd", "start", "useEncryption"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q gardé :\n%s", gone, got)
		}
	}
	conf, err := parseFrpConfig(got)
	if err != nil || str(conf, "serverAddr", "") != "vps.example.net" || str(mapv(conf, "auth"), "token", "") != "secret" ||
		str(mapv(conf, "log"), "to", "") != "/var/log/frp/frpc.log" || tomlInt(mapv(conf, "webServer")["port"]) != 7400 {
		t.Fatalf("%v %v\n%s", conf, err, got)
	}
}

func noPortCheck(t *testing.T) {
	saved := portFree
	t.Cleanup(func() { portFree = saved })
	portFree = func(int, bool) bool { return true }
}

func TestCopyRewritesPortsAndLog(t *testing.T) {
	noPortCheck(t)
	saved := frpLogDir
	t.Cleanup(func() { frpLogDir = saved })
	frpLogDir = "/var/log/frp"
	server := "bindPort = 7000\nkcpBindPort = 7000\nvhostHTTPPort = 80\nauth.token = \"tok\"\n\n[webServer]\nport = 7500\n\n[log]\nto = \"/var/log/frp/frps.log\"\n"
	used := map[int]bool{7000: true, 80: true, 7500: true, 8765: true}
	out, err := FR.copyInstanceConfig("frps", "frps", server, "frps-b", used)
	if err != nil {
		t.Fatal(err)
	}
	conf, err := parseFrpConfig(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	bind := tomlInt(conf["bindPort"])
	if bind < 10000 || bind > 32767 || tomlInt(conf["kcpBindPort"]) != bind {
		t.Errorf("bindPort %d, kcpBindPort %v", bind, conf["kcpBindPort"])
	}
	if p := tomlInt(conf["vhostHTTPPort"]); p == 80 || p == 0 {
		t.Errorf("vhostHTTPPort %d", p)
	}
	if p := tomlInt(mapv(conf, "webServer")["port"]); p != 7501 {
		t.Errorf("webServer.port %d", p)
	}
	if to := str(mapv(conf, "log"), "to", ""); filepath.ToSlash(to) != "/var/log/frp/frps-b.log" {
		t.Errorf("log.to %q", to)
	}
	if str(mapv(conf, "auth"), "token", "") != "tok" {
		t.Error("jeton perdu")
	}

	// frps sans bindPort écoute sur 7000 : la copie reçoit un bindPort
	// frps without bindPort listens on 7000: the copy gets a bindPort
	out, err = FR.copyInstanceConfig("frps", "frps", "[auth]\ntoken = \"x\"\n", "frps-c", map[int]bool{7000: true})
	if err != nil || tomlInt(must(parseFrpConfig(out))["bindPort"]) < 10000 {
		t.Fatalf("%v\n%s", err, out)
	}

	// frpc : sans tunnels, tableau de bord local déplacé, en-tête renommé
	// frpc: without tunnels, local dashboard moved, header renamed
	out, err = FR.copyInstanceConfig("frpc", "frpc", clientWithTunnels, "frpc-maison", map[int]bool{7400: true})
	if err != nil {
		t.Fatal(err)
	}
	conf = must(parseFrpConfig(out))
	if tomlInt(mapv(conf, "webServer")["port"]) != 7401 || len(listv(conf, "proxies")) != 0 ||
		!strings.HasPrefix(out, "# frpc-maison.toml — généré par FRP Manager") {
		t.Fatalf("%s", out)
	}

	// Port impossible à réécrire (table en ligne) : refus plutôt qu'un conflit
	// Port that cannot be rewritten (inline table): refusal rather than a conflict
	if _, err := FR.copyInstanceConfig("frpc", "frpc", "webServer = { port = 7400 }\n", "frpc-x", map[int]bool{7400: true}); err == nil {
		t.Fatal("conflit de port accepté")
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// systemdEnv : mode systemd dans des dossiers temporaires (unités, configs,
// journaux, programmes frps/frpc factices), aucune commande réelle : elles
// sont notées dans calls.
// systemdEnv: systemd mode in temporary folders (units, configs, logs, fake
// frps/frpc programs), no real command: they are recorded in calls.
func systemdEnv(t *testing.T) (*testEnv, *[]string) {
	t.Helper()
	e := setup(t)
	noPortCheck(t)
	saved := struct {
		unit, conf, log, bin string
		search               []string
		standalone           bool
	}{unitDir, frpConfDir, frpLogDir, frpBinDir, binarySearchPaths, standalone}
	t.Cleanup(func() {
		unitDir, frpConfDir, frpLogDir, frpBinDir = saved.unit, saved.conf, saved.log, saved.bin
		binarySearchPaths, standalone = saved.search, saved.standalone
	})
	standalone = false
	unitDir = filepath.Join(e.dir, "systemd") + string(filepath.Separator)
	frpConfDir, frpLogDir, frpBinDir = configSearchPaths[0], filepath.Join(e.dir, "log"), filepath.Join(e.dir, "bin")
	for _, d := range []string{unitDir, frpConfDir, frpBinDir} {
		os.MkdirAll(d, 0o755)
	}
	for _, b := range frpBinaries {
		os.WriteFile(filepath.Join(frpBinDir, b), []byte("#!/bin/sh\n"), 0o755)
		// Windows : pas de bit exécutable, le programme est trouvé par PATH
		// Windows: no executable bit, the program is found through PATH
		os.WriteFile(filepath.Join(frpBinDir, b+".exe"), nil, 0o755)
	}
	binarySearchPaths = []string{frpBinDir}
	t.Setenv("PATH", frpBinDir)
	calls := &[]string{}
	execCmd = func(args []string, _ time.Duration, _ *string) (bool, string, string) {
		*calls = append(*calls, strings.Join(args, " "))
		return true, "", ""
	}
	return e, calls
}

const testClientConf = "serverAddr = \"vps.example.net\"\nserverPort = 7000\nauth.token = \"secret\"\nlog.to = \"/var/log/frp/frpc.log\"\n\n" +
	"[webServer]\naddr = \"127.0.0.1\"\nport = 7400\n\n[[proxies]]\nname = \"ssh\"\ntype = \"tcp\"\nlocalPort = 22\nremotePort = 6000\n"

// existingInstances : frps et frpc systemd, configs dans le dossier de configuration.
// existingInstances: systemd frps and frpc, configs in the configuration folder.
func existingInstances() {
	insts := map[string]*Instance{}
	for bin, content := range map[string]string{
		"frps": "bindPort = 7000\nkcpBindPort = 7000\nauth.token = \"tok\"\n\n[webServer]\nport = 7500\n",
		"frpc": testClientConf,
	} {
		conf := filepath.Join(frpConfDir, bin+".toml")
		os.WriteFile(conf, []byte(content), 0o600)
		insts[bin] = &Instance{ID: bin, Type: bin, Source: "systemd", Service: bin, Config: conf}
	}
	setDiscovery(insts)
}

func listDir(d string) []string {
	entries, _ := os.ReadDir(d)
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestCreateInstanceSystemd(t *testing.T) {
	e, calls := systemdEnv(t)
	existingInstances()
	c := e.owner()

	// Noms refusés : rien n'est écrit, aucune commande n'est lancée
	// Rejected names: nothing is written, no command is run
	confsBefore := listDir(frpConfDir)
	for _, body := range []J{
		{"type": "frpc", "name": ""}, {"type": "frpc", "name": "../../etc/x"}, {"type": "frpc", "name": "a b"},
		{"type": "frpc", "name": "x;reboot"}, {"type": "frpc", "name": strings.Repeat("a", 40)}, {"type": "frpc", "name": "a@b"},
		{"type": "sshd", "name": "x"}, {"type": "frpc"},
	} {
		expectCode(t, c.post("/api/instance/create", body), 400)
	}
	if !reflect.DeepEqual(listDir(frpConfDir), confsBefore) || len(listDir(unitDir)) != 0 || len(*calls) != 0 {
		t.Fatalf("écrit ou lancé : %v %v %v", listDir(frpConfDir), listDir(unitDir), *calls)
	}

	// Nouveau frpc : config par défaut, journal propre, unité arrêtée
	// New frpc: default config, own log, stopped unit
	r := c.post("/api/instance/create", J{"type": "frpc", "name": "Maison"})
	expectCode(t, r, 200)
	if r.json()["id"] != "frpc-maison" {
		t.Fatalf("%s", r.text())
	}
	conf := filepath.Join(frpConfDir, "frpc-maison.toml")
	content, err := os.ReadFile(conf)
	if err != nil || logTo(string(content)) != instanceLogPath("frpc-maison") {
		t.Fatalf("config : %q %v", content, err)
	}
	if st, _ := os.Stat(conf); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("droits %v", st.Mode())
	}
	unit, err := os.ReadFile(unitDir + "frpc-maison.service")
	if err != nil || !strings.Contains(string(unit), "Description=frp client (frpc-maison)") ||
		!strings.Contains(string(unit), " -c "+conf+"\n") {
		t.Fatalf("unité : %s %v", unit, err)
	}
	if (*calls)[len(*calls)-1] != "systemctl daemon-reload" {
		t.Fatalf("commandes : %v", *calls)
	}

	// Même nom : refusé, rien n'est écrasé
	// Same name: rejected, nothing is overwritten
	os.WriteFile(conf, []byte("# gardé\n"), 0o600)
	expectCode(t, c.post("/api/instance/create", J{"type": "frpc", "name": "maison"}), 409)
	expectCode(t, c.post("/api/instance/create", J{"type": "frpc", "name": "c", "copy_from": "frps"}), 400)
	if b, _ := os.ReadFile(conf); string(b) != "# gardé\n" {
		t.Fatal("config écrasée")
	}
	// Une unité au même nom (sans config) suffit aussi à refuser
	// A unit with the same name (without config) is also enough to refuse
	os.WriteFile(unitDir+"frps-vps.service", []byte("[Unit]\n"), 0o644)
	expectCode(t, c.post("/api/instance/create", J{"type": "frps", "name": "vps"}), 409)

	// Copie de frps : autres ports, même jeton, journal propre
	// Copy of frps: other ports, same token, own log
	r = c.post("/api/instance/create", J{"type": "frps", "name": "b", "copy_from": "frps"})
	expectCode(t, r, 200)
	srv := must(parseFrpConfig(string(must(os.ReadFile(filepath.Join(frpConfDir, "frps-b.toml"))))))
	if bind := tomlInt(srv["bindPort"]); bind == 7000 || tomlInt(srv["kcpBindPort"]) != bind ||
		tomlInt(mapv(srv, "webServer")["port"]) != 7501 || str(mapv(srv, "auth"), "token", "") != "tok" {
		t.Fatalf("frps-b : %v", srv)
	}
	// frps par défaut : port et jeton tirés au hasard
	// Default frps: random port and token
	expectCode(t, c.post("/api/instance/create", J{"type": "frps", "name": "d"}), 200)
	def := must(parseFrpConfig(string(must(os.ReadFile(filepath.Join(frpConfDir, "frps-d.toml"))))))
	if bind := tomlInt(def["bindPort"]); bind < 10000 || bind == tomlInt(srv["bindPort"]) || str(mapv(def, "auth"), "token", "") == "changeme" {
		t.Fatalf("frps-d : %v", def)
	}

	// Copie de frpc : connexion gardée, tunnels retirés, tableau de bord déplacé
	// Copy of frpc: connection kept, tunnels removed, dashboard moved
	expectCode(t, c.post("/api/instance/create", J{"type": "frpc", "name": "c", "copy_from": "frpc"}), 200)
	cli := must(parseFrpConfig(string(must(os.ReadFile(filepath.Join(frpConfDir, "frpc-c.toml"))))))
	if str(cli, "serverAddr", "") != "vps.example.net" || len(listv(cli, "proxies")) != 0 || tomlInt(mapv(cli, "webServer")["port"]) != 7401 {
		t.Fatalf("frpc-c : %v", cli)
	}
	for _, call := range *calls {
		if strings.Contains(call, " start") || strings.Contains(call, " enable") {
			t.Fatalf("service démarré : %v", *calls)
		}
	}
}

func TestCreateInstancePermissions(t *testing.T) {
	e, _ := systemdEnv(t)
	existingInstances()
	if userPermissions(User{"role": "operator"})["instances.create"] || userPermissions(User{"role": "viewer"})["instances.create"] {
		t.Fatal("rôle prédéfini autorisé à créer")
	}
	if !permissions["instances.create"].sensitive || permissions["instances.create"].scoped {
		t.Fatal("instances.create : sensible, non limitable")
	}
	o := e.owner()
	createUser(o, J{"username": "op", "role": "operator"})
	createUser(o, J{"username": "limited", "role": "custom", "permissions": []any{"instances.create"}, "instances": []any{"frpc"}})
	createUser(o, J{"username": "maker", "role": "custom", "permissions": []any{"instances.create"}})
	expectCode(t, e.loginAs("op", otherPassword).post("/api/instance/create", J{"type": "frpc", "name": "a"}), 403)
	expectCode(t, e.loginAs("limited", otherPassword).post("/api/instance/create", J{"type": "frpc", "name": "a"}), 403)
	expectCode(t, e.loginAs("op", otherPassword).get("/api/instance/orphans"), 403)
	maker := e.loginAs("maker", otherPassword)
	if !reflect.DeepEqual(stringList(maker.get("/api/me").json()["user"].(map[string]any)["permissions"]), []string{"instances.create", "instances.view"}) {
		t.Fatal("implications")
	}
	expectCode(t, maker.post("/api/instance/create", J{"type": "frpc", "name": "a"}), 200)
	expectCode(t, e.client().post("/api/instance/create", J{"type": "frpc", "name": "b"}), 401)
}

func TestCreateInstanceNeedsProgram(t *testing.T) {
	e, _ := systemdEnv(t)
	for _, b := range frpBinaries {
		os.Remove(filepath.Join(frpBinDir, b))
		os.Remove(filepath.Join(frpBinDir, b+".exe"))
	}
	r := e.owner().post("/api/instance/create", J{"type": "frpc", "name": "a"})
	if r.code != 400 || !strings.Contains(r.text(), "introuvable") || len(listDir(frpConfDir)) != 0 {
		t.Fatalf("%d %s %v", r.code, r.text(), listDir(frpConfDir))
	}
}

// Cas réel : frpc-ancelade.toml déposé à côté de frpc.toml (copie, avec le même
// journal, le même port d'administration et les mêmes tunnels).
// Real case: frpc-ancelade.toml dropped next to frpc.toml (copy, with the same
// log, the same admin port and the same tunnels).
func TestOrphanConfigs(t *testing.T) {
	e, calls := systemdEnv(t)
	existingInstances()
	orphan := filepath.Join(frpConfDir, "frpc-ancelade.toml")
	os.WriteFile(orphan, []byte(testClientConf), 0o600)
	os.WriteFile(filepath.Join(frpConfDir, "frpc mon.toml"), []byte(testClientConf), 0o600) // nom refusé / rejected name
	gone := filepath.Join(frpConfDir, "frps-old.toml")
	os.WriteFile(gone, []byte("bindPort = 7100\n"), 0o600)
	cfgUpdate(Cfg{"dismissed_configs": []any{gone}})
	c := e.owner()

	list := c.get("/api/instance/orphans").json()["orphans"].([]any)
	if len(list) != 1 {
		t.Fatalf("%v", list)
	}
	o := list[0].(map[string]any)
	conflicts := strings.Join(stringList(o["conflicts"]), " | ")
	if o["id"] != "frpc-ancelade" || o["type"] != "frpc" || o["fixable"] != true ||
		!strings.Contains(conflicts, "frpc.log") || !strings.Contains(conflicts, "port 7400") || !strings.Contains(conflicts, ": ssh") {
		t.Fatalf("%v", o)
	}

	// Sans correction : unité créée, config intacte
	// Without fixing: unit created, config untouched
	expectCode(t, c.post("/api/instance/adopt", J{"config_path": "/etc/passwd"}), 404)
	expectCode(t, c.post("/api/instance/adopt", J{"config_path": orphan}), 200)
	unit, _ := os.ReadFile(unitDir + "frpc-ancelade.service")
	if !strings.Contains(string(unit), " -c "+orphan+"\n") {
		t.Fatalf("unité : %s", unit)
	}
	if b, _ := os.ReadFile(orphan); string(b) != testClientConf {
		t.Fatal("config modifiée")
	}
	if (*calls)[len(*calls)-1] != "systemctl daemon-reload" {
		t.Fatalf("%v", *calls)
	}
	expectCode(t, c.post("/api/instance/adopt", J{"config_path": orphan}), 409)

	// Avec correction : journal et port d'administration propres, tunnels gardés
	// With fixing: own log and admin port, tunnels kept
	other := filepath.Join(frpConfDir, "frpc-b.toml")
	os.WriteFile(other, []byte(testClientConf), 0o600)
	expectCode(t, c.post("/api/instance/adopt", J{"config_path": other, "fix": true}), 200)
	conf := must(parseFrpConfig(string(must(os.ReadFile(other)))))
	if logTo(string(must(os.ReadFile(other)))) != instanceLogPath("frpc-b") || tomlInt(mapv(conf, "webServer")["port"]) == 7400 ||
		len(listv(conf, "proxies")) != 1 {
		t.Fatalf("%v", conf)
	}
}

func TestFindSystemdUnitsWithSuffix(t *testing.T) {
	setup(t)
	execCmd = func(args []string, _ time.Duration, _ *string) (bool, string, string) {
		switch {
		case len(args) > 1 && args[1] == "list-unit-files":
			return true, "frpc.service enabled\nfrpc-maison.service disabled\nfrps-exporter.service enabled\nfrpc@.service static\nsshd.service enabled", ""
		case len(args) > 1 && args[1] == "show":
			return true, "Id=frpc.service\nExecStart={ path=/usr/local/bin/frpc ; argv[]=/usr/local/bin/frpc -c /etc/frp/frpc.toml ; }\n\n" +
				"Id=frpc-maison.service\nExecStart={ path=/usr/local/bin/frpc ; argv[]=/usr/local/bin/frpc -c /etc/frp/frpc-maison.toml ; }\n\n" +
				"Id=frps-exporter.service\nExecStart={ path=/usr/bin/frps_exporter ; argv[]=/usr/bin/frps_exporter ; }\n", ""
		}
		return false, "", ""
	}
	got := findSystemdUnits()
	want := []unitCfg{{"frpc", "/etc/frp/frpc.toml"}, {"frpc-maison", "/etc/frp/frpc-maison.toml"}}
	if !reflect.DeepEqual(got["frpc"], want) || len(got["frps"]) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestCreateInstanceStandalone(t *testing.T) {
	mode := "run"
	e := standaloneEnv(t, &mode)
	os.WriteFile(filepath.Join(frpConfDir, "frps.toml"), []byte(defaultFrpsConfig), 0o600)
	savedUnit := unitDir
	t.Cleanup(func() { unitDir = savedUnit })
	unitDir = filepath.Join(e.dir, "systemd") + "/"
	c := e.owner()
	r := c.post("/api/instance/create", J{"type": "frps", "name": "b", "copy_from": "frps"})
	expectCode(t, r, 200)
	if st := statusOfInst(t, c, "frps-b"); st["running"] != false || st["enabled"] != false {
		t.Fatalf("état : %v", st)
	}
	if fileExists(unitDir) {
		t.Fatal("unité systemd en mode autonome")
	}
	conf := must(parseFrpConfig(string(must(os.ReadFile(filepath.Join(frpConfDir, "frps-b.toml"))))))
	if tomlInt(conf["bindPort"]) == 7000 {
		t.Fatalf("même port : %v", conf)
	}
	if d := c.post("/api/service/frps-b/start", nil).json(); d["ok"] != true {
		t.Fatalf("start : %v", d)
	}
	if r := c.get("/api/instance/orphans").json(); fmt.Sprint(r["orphans"]) != "[]" {
		t.Fatalf("configs sans service en mode autonome : %v", r)
	}
}
