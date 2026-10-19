// Validation des noms, des configs TOML, des chemins et des archives.

package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// ── Noms de services, conteneurs, instances ─────────────────────────────────

func TestServiceNames(t *testing.T) {
	for _, n := range []string{"frps", "frpc", "frpc2", "frpc-maison", "frps_vps.eu", "frpc@1"} {
		if !validServiceName(n) {
			t.Errorf("%q refusé", n)
		}
	}
	for _, n := range []string{"", "sshd", "frp", "frpx", "frpc;reboot", "frpc reboot", "frpc$(id)", "../frpc", "frpc/../../x",
		"-frpc", "frpc\n", "frpc`id`", "frpc|sh", strings.Repeat("f", 100)} {
		if validServiceName(n) {
			t.Errorf("%q accepté", n)
		}
	}
}

func TestContainerNames(t *testing.T) {
	for n, ok := range map[string]bool{"frpc": true, "my-stack_frpc.1": true, "0123456789ab": true,
		"../../containers": false, "a/b": false, "-x": false, "x?y=1": false, "": false} {
		if validContainerName(n) != ok {
			t.Errorf("%q : %v", n, !ok)
		}
	}
}

func TestServiceActionRefusesBadName(t *testing.T) {
	setup(t)
	var calls [][]string
	execCmd = func(args []string, _ time.Duration, _ *string) (bool, string, string) {
		calls = append(calls, args)
		return true, "", ""
	}
	if ok, _ := FR.serviceAction("frpc; rm -rf /", "restart"); ok || len(calls) != 0 {
		t.Fatal("nom refusé exécuté")
	}
	if ok, _ := FR.serviceAction("frpc2", "restart"); !ok || !reflect.DeepEqual(calls, [][]string{{"systemctl", "restart", "frpc2"}}) {
		t.Fatalf("%v", calls)
	}
}

// ── Configs TOML ────────────────────────────────────────────────────────────

const validTOML = "serverAddr = \"vps.example.net\"\nserverPort = 7000\n\n[[proxies]]\nname = \"ssh\"\ntype = \"tcp\"\nlocalPort = 22\nremotePort = 6000\n"

func TestTOMLValidation(t *testing.T) {
	if err := FR.validateConfigContent("/etc/frp/frpc.toml", validTOML); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"serverAddr = \"vps.example.net\n",       // guillemet non fermé
		"[[proxies]\nname = 'x'\n",               // section mal formée
		"serverPort = 7000\nserverPort = 7001\n", // clé en double
		"serverAddr = \x00\n",
		"# " + strings.Repeat("x", configMaxBytes), // trop gros
	} {
		if FR.validateConfigContent("/etc/frp/frpc.toml", bad) == nil {
			t.Errorf("accepté : %q", truncRunes(bad, 40))
		}
	}
}

func TestInvalidTOMLNeverWritten(t *testing.T) {
	e := setup(t)
	dir := filepath.Join(e.dir, "frp")
	os.MkdirAll(dir, 0o755)
	target := filepath.Join(dir, "frpc.toml")
	os.WriteFile(target, []byte(validTOML), 0o644)
	configSearchPaths = []string{dir}
	ok, msg, code := FR.writeConfigFile(target, "serverAddr = \"cassé\n")
	if ok || code != 400 || !strings.Contains(msg, "TOML invalide") {
		t.Fatalf("%v %d %s", ok, code, msg)
	}
	if b, _ := os.ReadFile(target); string(b) != validTOML {
		t.Fatal("fichier modifié")
	}
	ok, _, code = FR.writeConfigFile(target, strings.Replace(validTOML, "7000", "7001", 1))
	if b, _ := os.ReadFile(target); !ok || code != 200 || !strings.Contains(string(b), "7001") {
		t.Fatal("écriture valide refusée")
	}
}

// ── Confinement des chemins ─────────────────────────────────────────────────

func TestConfigPathConfined(t *testing.T) {
	e := setup(t)
	dir := filepath.Join(e.dir, "frp")
	os.MkdirAll(dir, 0o755)
	configSearchPaths = []string{dir}
	if p, err := FR.safeConfigPath(filepath.Join(dir, "frpc.toml")); err != nil || filepath.Base(p) != "frpc.toml" {
		t.Fatal(err)
	}
	if _, err := FR.safeConfigPath(filepath.Join(dir, "sous", "frpc.toml")); err != nil {
		t.Fatal("sous-dossier refusé")
	}
	for _, bad := range []string{dir + "/../frpc.toml", filepath.Join(e.dir, "ailleurs.toml"), filepath.Join(dir, "frpc.sh"),
		"/etc/passwd", dir + "/sous/../../x.toml", dir} {
		if _, err := FR.safeConfigPath(bad); err == nil {
			t.Errorf("accepté : %s", bad)
		}
	}
}

func TestConfigSymlinkEscapeRefused(t *testing.T) {
	e := setup(t)
	dir := filepath.Join(e.dir, "frp")
	os.MkdirAll(dir, 0o755)
	outside := filepath.Join(e.dir, "secret.toml")
	os.WriteFile(outside, []byte("x = 1\n"), 0o644)
	link := filepath.Join(dir, "frpc.toml")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("liens symboliques indisponibles ici")
	}
	configSearchPaths = []string{dir}
	if _, err := FR.safeConfigPath(link); err == nil {
		t.Fatal("lien vers l'extérieur accepté")
	}
}

// ── Archives frp ────────────────────────────────────────────────────────────

var elf = append([]byte("\x7fELF"), make([]byte, 60)...)

type tarMember struct {
	name string
	data []byte
	sym  bool
}

func makeTar(t *testing.T, path string, members []tarMember) string {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, m := range members {
		if m.sym {
			tw.WriteHeader(&tar.Header{Name: m.name, Typeflag: tar.TypeSymlink, Linkname: "/etc/shadow"})
			continue
		}
		tw.WriteHeader(&tar.Header{Name: m.name, Typeflag: tar.TypeReg, Size: int64(len(m.data)), Mode: 0o755})
		tw.Write(m.data)
	}
	tw.Close()
	gz.Close()
	os.WriteFile(path, buf.Bytes(), 0o644)
	return path
}

func TestExtractsOnlyFrpBinaries(t *testing.T) {
	dir := t.TempDir()
	arc := makeTar(t, filepath.Join(dir, "frp.tar.gz"), []tarMember{
		{"frp_0.71.0_linux_amd64/frps", elf, false},
		{"frp_0.71.0_linux_amd64/frpc", elf, false},
		{"frp_0.71.0_linux_amd64/LICENSE", []byte("MIT"), false},
	})
	out := filepath.Join(dir, "out")
	os.MkdirAll(out, 0o755)
	found, err := extractFrpBinaries(FR, arc, out)
	if err != nil {
		t.Fatal(err)
	}
	keys := sortedKeys(found)
	entries, _ := os.ReadDir(out)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if !reflect.DeepEqual(keys, []string{"frpc", "frps"}) || !reflect.DeepEqual(names, []string{"frpc", "frps"}) {
		t.Fatalf("%v %v", keys, names)
	}
}

func TestMaliciousArchivesRejected(t *testing.T) {
	for _, members := range [][]tarMember{
		{{"../frps", elf, false}},
		{{"/usr/local/bin/frps", elf, false}},
		{{"frp/../../frpc", elf, false}},
		{{"frp/frps", nil, true}},
		{{"frp/frps", []byte("#!/bin/sh\nrm -rf /\n"), false}}, // pas un exécutable ELF
	} {
		dir := t.TempDir()
		arc := makeTar(t, filepath.Join(dir, "evil.tar.gz"), members)
		out := filepath.Join(dir, "out")
		os.MkdirAll(out, 0o755)
		if _, err := extractFrpBinaries(FR, arc, out); err == nil {
			t.Errorf("archive acceptée : %s", members[0].name)
		}
		if fileExists(filepath.Join(dir, "frps")) || fileExists(filepath.Join(dir, "frpc")) {
			t.Error("fichier écrit hors du dossier")
		}
	}
}

// Réinstaller frpc : seul son binaire est remplacé, seuls ses services sont
// arrêtés puis relancés, et la version notée de frp ne change pas.
func TestReinstallOnlyOneBinary(t *testing.T) {
	setup(t)
	saved := frpBinDir
	defer func() { frpBinDir = saved }()
	frpBinDir = t.TempDir()
	os.WriteFile(filepath.Join(frpBinDir, "frps"), []byte("ancien frps"), 0o755)
	saveState(map[string]any{"installed_version": "0.70.0"})
	var calls []string
	execCmd = func(args []string, _ time.Duration, _ *string) (bool, string, string) {
		switch {
		case len(args) > 1 && args[1] == "list-units":
			return true, "frps.service loaded active running frps\nfrpc-maison.service loaded active running frpc\n", ""
		case len(args) > 2 && args[1] == "show":
			return true, "{ path=/usr/local/bin/" + strings.Split(strings.TrimSuffix(args[2], ".service"), "-")[0] + " ; }", ""
		}
		calls = append(calls, strings.Join(args, " "))
		return true, "", ""
	}
	dir := t.TempDir()
	arc := makeTar(t, filepath.Join(dir, "frp.tar.gz"), []tarMember{
		{"frp_0.70.0_linux_amd64/frps", elf, false},
		{"frp_0.70.0_linux_amd64/frpc", elf, false},
	})
	if !installFromArchive(FR, arc, "0.70.0", []string{"frpc"}, func(string) {}) {
		t.Fatal("échec")
	}
	if b, _ := os.ReadFile(filepath.Join(frpBinDir, "frps")); string(b) != "ancien frps" {
		t.Fatal("frps remplacé")
	}
	if b, _ := os.ReadFile(filepath.Join(frpBinDir, "frpc")); !bytes.Equal(b, elf) {
		t.Fatal("frpc non installé")
	}
	if !reflect.DeepEqual(calls, []string{"systemctl stop frpc-maison", "systemctl start frpc-maison"}) {
		t.Fatalf("%v", calls)
	}
	if v := loadState()["installed_version"]; v != "0.70.0" {
		t.Fatalf("version notée : %v", v)
	}
	if v, tag := reinstallVersion("0.70.0"); v != "0.70.0" || tag != "v0.70.0" {
		t.Fatal(v, tag)
	}
	for _, v := range []string{"", "manual", "0.70.0; rm -rf /"} {
		if got, _ := reinstallVersion(v); got != "" {
			t.Errorf("%q → %q", v, got)
		}
	}
}

// Service supprimé puis réinstallé : unité et config par défaut recréées, rien
// n'est démarré ; une config conservée n'est pas réécrite.
func TestReinstallService(t *testing.T) {
	e := setup(t)
	saved := []string{unitDir, frpConfDir, frpLogDir, frpBinDir}
	savedSearch, savedInstall := binarySearchPaths, installMissingBinary
	defer func() {
		unitDir, frpConfDir, frpLogDir, frpBinDir = saved[0], saved[1], saved[2], saved[3]
		binarySearchPaths, installMissingBinary = savedSearch, savedInstall
	}()
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	unitDir = filepath.Join(dir, "systemd") + string(filepath.Separator)
	os.MkdirAll(unitDir, 0o755)
	frpConfDir, frpLogDir, frpBinDir = filepath.Join(dir, "etc-frp"), filepath.Join(dir, "log"), filepath.Join(dir, "bin")
	binarySearchPaths = []string{frpBinDir}
	downloads := 0
	installMissingBinary = func(Lang, string, string) bool { downloads++; return true }
	var calls []string
	execCmd = func(args []string, _ time.Duration, _ *string) (bool, string, string) {
		calls = append(calls, strings.Join(args, " "))
		return true, "", ""
	}
	frpsConf := filepath.Join(frpConfDir, "frps.toml")
	cfgUpdate(Cfg{"dismissed_configs": []any{frpsConf}})

	if !reinstallService(FR, "frpc", "0.70.0") {
		t.Fatal(updateLog.snapshot())
	}
	unit, _ := os.ReadFile(unitDir + "frpc.service")
	exec := "ExecStart=" + filepath.Join(frpBinDir, "frpc") + " -c " + filepath.Join(frpConfDir, "frpc.toml") + "\n"
	if !strings.Contains(string(unit), exec) || !strings.Contains(string(unit), "Description=frp client (frpc)") {
		t.Fatalf("unité :\n%s", unit)
	}
	if b, _ := os.ReadFile(filepath.Join(frpConfDir, "frpc.toml")); string(b) != defaultFrpcConfig {
		t.Fatalf("config : %q", b)
	}
	if downloads != 1 {
		t.Fatalf("%d téléchargements", downloads)
	}
	for _, call := range calls {
		if strings.Contains(call, " start") || strings.Contains(call, " enable") {
			t.Fatalf("service démarré : %v", calls)
		}
	}
	if !reflect.DeepEqual(calls[len(calls)-1:], []string{"systemctl daemon-reload"}) {
		t.Fatalf("%v", calls)
	}

	// frps : programme présent, config conservée et rendue à la détection
	os.MkdirAll(frpBinDir, 0o755)
	os.WriteFile(filepath.Join(frpBinDir, "frps"), elf, 0o755)
	os.WriteFile(frpsConf, []byte("bindPort = 7100\n"), 0o600)
	if !isExecutable(filepath.Join(frpBinDir, "frps")) {
		binarySearchPaths = nil // Windows : pas de bit exécutable, le programme passe pour absent
	}
	if !reinstallService(FR, "frps", "0.70.0") {
		t.Fatal(updateLog.snapshot())
	}
	if b, _ := os.ReadFile(frpsConf); string(b) != "bindPort = 7100\n" {
		t.Fatalf("config conservée réécrite : %q", b)
	}
	if len(stringList(cfg()["dismissed_configs"])) != 0 {
		t.Fatal("config toujours écartée")
	}

	// La route : programme inconnu refusé, service existant pas écrasé
	e.setPassword(testPassword, "admin", false)
	c := e.client()
	expectCode(t, c.login("admin", testPassword), 200)
	expectCode(t, c.post("/api/instance/reinstall/sshd", nil), 400)
	expectCode(t, c.post("/api/instance/reinstall/frpc", nil), 409)
}

// Port attribué par frps (remotePort = 0) : lu sur l'API d'administration de frpc.
func TestFrpcProxyStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); r.URL.Path != "/api/status" || u != "admin" || p != "secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"tcp":[{"name":"ssh","type":"tcp","status":"running","err":"","local_addr":"127.0.0.1:22","remote_addr":"vps.example.com:34567"}],
			"udp":[{"name":"jeu","type":"udp","status":"start error","err":"port already used","remote_addr":""}]}`))
	}))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	conf := func(password string) string {
		return "serverAddr = \"vps.example.com\"\n[webServer]\naddr = \"127.0.0.1\"\nport = " + port +
			"\nuser = \"admin\"\npassword = \"" + password + "\"\n"
	}

	proxies, admin, err := frpcProxyStatus(conf("secret"))
	if err != nil || !admin {
		t.Fatal(admin, err)
	}
	if proxies["ssh"]["remote_addr"] != "vps.example.com:34567" || proxies["jeu"]["err"] != "port already used" {
		t.Fatalf("%v", proxies)
	}
	if _, admin, err := frpcProxyStatus(conf("faux")); !admin || err == nil {
		t.Fatal("mauvais mot de passe accepté")
	}
	// Sans [webServer], ou config illisible : pas d'API, pas d'erreur
	for _, c := range []string{"serverAddr = \"vps.example.com\"\n", "serverAddr = [", ""} {
		if p, admin, err := frpcProxyStatus(c); admin || err != nil || p != nil {
			t.Errorf("%q : %v %v %v", c, p, admin, err)
		}
	}
}

func TestDecompressionBombRejected(t *testing.T) {
	saved := archiveMaxUnpacked
	defer func() { archiveMaxUnpacked = saved }()
	archiveMaxUnpacked = 1024
	dir := t.TempDir()
	arc := makeTar(t, filepath.Join(dir, "bomb.tar.gz"), []tarMember{{"frp/padding", make([]byte, 4096), false}})
	if _, err := extractFrpBinaries(FR, arc, dir); err == nil {
		t.Fatal("bombe acceptée")
	}
}

func TestPanelZipChecks(t *testing.T) {
	mk := func(name string) *zip.Reader {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create(name)
		w.Write([]byte("x"))
		zw.Close()
		zr, _ := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		return zr
	}
	if err := checkPanelZip(FR, mk("frp-manager/bin/frp-manager-linux-amd64")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../app.py", "/etc/cron.d/x", "C:/x.py", "frp-manager\\..\\..\\x"} {
		if checkPanelZip(FR, mk(name)) == nil {
			t.Errorf("accepté : %s", name)
		}
	}
}

func TestChecksumFileParsing(t *testing.T) {
	sums := parseChecksums("84f27e39f11169f7adcef8e8b70c9329de17747b1f14dad9fb95eef5682ea716  frp_0.71.0_linux_amd64.tar.gz\ngarbage line\n")
	if !reflect.DeepEqual(sums, map[string]string{"frp_0.71.0_linux_amd64.tar.gz": "84f27e39f11169f7adcef8e8b70c9329de17747b1f14dad9fb95eef5682ea716"}) {
		t.Fatalf("%v", sums)
	}
}

func TestMirrorsCanBeDisabled(t *testing.T) {
	setup(t)
	cfgUpdate(Cfg{"download_mirrors": true})
	if len(buildDownloadMirrors("v0.71.0", "f.tar.gz")) < 2 {
		t.Fatal("miroirs absents")
	}
	cfgUpdate(Cfg{"download_mirrors": false})
	if urls := buildDownloadMirrors("v0.71.0", "f.tar.gz"); !reflect.DeepEqual(urls, []string{"https://github.com/fatedier/frp/releases/download/v0.71.0/f.tar.gz"}) {
		t.Fatalf("%v", urls)
	}
}

// ── Versions ────────────────────────────────────────────────────────────────

func TestVersionOrdering(t *testing.T) {
	for _, c := range []struct {
		a, b  string
		newer bool
	}{
		{"0.0.57", "0.0.56", true}, {"v0.1.0", "0.0.99", true}, {"0.0.56", "0.0.57", false},
		{"0.1.0", "0.1.0-pre.2", true}, {"0.1.0-pre.10", "0.1.0-pre.2", true}, {"0.1.0-pre.2", "0.1.0-beta", true}, {"0.1.0-beta", "0.1.0-beta.1", false},
		{"0.0.57", "0.0.57", false}, {"dev-abc", "0.0.1", false},
	} {
		if versionNewer(c.a, c.b) != c.newer {
			t.Errorf("%s > %s : %v attendu", c.a, c.b, c.newer)
		}
	}
	if !isPrereleaseVersion("0.1.0-pre.1") || !isPrereleaseVersion("dev-abc1234") || isPrereleaseVersion("0.0.57") {
		t.Fatal("pré-releases")
	}
}

// ── Ports d'une config ──────────────────────────────────────────────────────

func TestExtractPorts(t *testing.T) {
	frpc := validTOML + "\n[[visitors]]\nname = \"db\"\nbindAddr = \"0.0.0.0\"\nbindPort = 9000\n\n[[visitors]]\nname = \"local\"\nbindPort = 9001\n\n[webServer]\nport = 7400\n"
	var got []string
	for _, p := range extractPortsFromConfig(frpc, "frpc") {
		got = append(got, p["label"].(string)+":"+p["proto"].(string)+":"+strings.TrimSpace(pyStr(float64(p["port"].(int)))))
	}
	want := []string{"Connexion serveur:tcp:7000", "Tunnel ssh (remotePort):tcp:6000", "Visiteur db (bindPort):tcp:9000", "Dashboard web:tcp:7400"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	frps := "bindPort = 7000\nkcpBindPort = 7000\nvhostHTTPPort = 80\n"
	if n := len(extractPortsFromConfig(frps, "frps")); n != 3 {
		t.Fatalf("%d ports frps", n)
	}
}
