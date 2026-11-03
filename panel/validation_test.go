// Validation des noms, des configs TOML, des chemins et des archives.

package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"debug/elf"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	toml "github.com/pelletier/go-toml/v2"
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

// Plage de ports telle que l'écrit la page Ports (generateProxy de toml.js)
// Port range as written by the Ports page (generateProxy in toml.js)
const rangeTOML = "{{- range $_, $v := parseNumberRangePair \"30000-30002\" \"40000-40001,40005\" }}\n[[proxies]]\n" +
	"name = \"jeu-{{ $v.First }}\"\ntype = \"udp\"\nlocalIP = \"127.0.0.1\"\nlocalPort = {{ $v.First }}\nremotePort = {{ $v.Second }}\n{{- end }}\n"

// Plages des autres types, telles que les écrit la page Ports : une seule plage
// (parseNumberRange) pour stcp/xtcp et http/https, {port} des domaines compris ;
// un visiteur par port, qui rejoint le tunnel de même rang
// Ranges of the other types, as written by the Ports page: a single range
// (parseNumberRange) for stcp/xtcp and http/https, {port} of the domains
// included; one visitor per port, which joins the tunnel of the same rank
const (
	stcpRangeTOML = "{{- range $_, $v := parseNumberRange \"6000-6002\" }}\n[[proxies]]\n" +
		"name = \"db-{{ $v }}\"\ntype = \"stcp\"\nlocalIP = \"127.0.0.1\"\nlocalPort = {{ $v }}\nsecretKey = \"s\"\n{{- end }}\n"
	httpRangeTOML = "{{- range $_, $v := parseNumberRange \"8080-8081\" }}\n[[proxies]]\n" +
		"name = \"web-{{ $v }}\"\ntype = \"http\"\nlocalIP = \"127.0.0.1\"\nlocalPort = {{ $v }}\ncustomDomains = [\"app-{{ $v }}.example.com\"]\n{{- end }}\n"
	visitorRangeTOML = "{{- range $_, $v := parseNumberRangePair \"16000-16002\" \"6000-6002\" }}\n[[visitors]]\n" +
		"name = \"acc-{{ $v.First }}\"\ntype = \"stcp\"\nserverName = \"db-{{ $v.Second }}\"\nsecretKey = \"s\"\nbindAddr = \"0.0.0.0\"\nbindPort = {{ $v.First }}\n{{- end }}\n"
)

func TestFrpTemplateValidation(t *testing.T) {
	for _, good := range []string{
		validTOML + "\n" + rangeTOML,
		rangeTOML,
		validTOML + "\n" + stcpRangeTOML + "\n" + httpRangeTOML + "\n" + visitorRangeTOML,
		"serverAddr = \"{{ .Envs.FRP_SERVER }}\"\nserverPort = {{ .Envs.FRP_PORT }}\n",
	} {
		if err := FR.validateConfigContent("/etc/frp/frpc.toml", good); err != nil {
			t.Errorf("%q : %v", truncRunes(good, 40), err)
		}
	}
	for _, bad := range []string{
		// Pas autant de ports des deux côtés, plage à l'envers, port hors limites,
		// range sans end, fonction inconnue, « {{ » que frp lirait comme un modèle
		// Not as many ports on both sides, reversed range, port out of bounds,
		// range without end, unknown function, "{{" that frp would read as a template
		strings.Replace(rangeTOML, "40000-40001,40005", "40000", 1),
		strings.Replace(rangeTOML, "30000-30002", "30002-30000", 1),
		strings.Replace(rangeTOML, "30000-30002", "65534-65536", 1),
		strings.Replace(rangeTOML, "{{- end }}\n", "", 1),
		strings.Replace(rangeTOML, "parseNumberRangePair", "inconnue", 1),
		"token = \"a{{b\"\n",
		// Une seule plage invalide, plages d'un visiteur de tailles différentes
		// An invalid single range, visitor ranges of different sizes
		strings.Replace(stcpRangeTOML, "6000-6002", "6002-6000", 1),
		strings.Replace(httpRangeTOML, "8080-8081", "70000", 1),
		strings.Replace(visitorRangeTOML, "6000-6002", "6000", 1),
	} {
		if err := FR.validateConfigContent("/etc/frp/frpc.toml", bad); err == nil {
			t.Errorf("accepté : %q", truncRunes(bad, 80))
		}
	}
	// Les numéros de ligne restent ceux du fichier, pour les messages d'erreur
	// Line numbers stay those of the file, for error messages
	if got := frpTOML(rangeTOML); strings.Count(got, "\n") != strings.Count(rangeTOML, "\n") || strings.Contains(got, "{{") {
		t.Fatalf("%q", got)
	}
	if ports, err := frpPortRange(" 6000-6002, 6007 "); err != nil || !reflect.DeepEqual(ports, []int{6000, 6001, 6002, 6007}) {
		t.Fatal(ports, err)
	}
	for _, bad := range []string{"", "0", "70000", "10-5", "1-2-3", "a", "1-65535,1"} {
		if _, err := frpPortRange(bad); err == nil {
			t.Errorf("plage acceptée : %q", bad)
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

// elfBin : en-tête ELF minimal pour le processeur de cette machine.
// elfBin: minimal ELF header for this machine's processor.
var elfBin = elfFor(elfMachines[runtime.GOARCH])

// elfFor : en-tête ELF minimal pour le processeur m.
// elfFor: minimal ELF header for the processor m.
func elfFor(m elf.Machine) []byte {
	var buf bytes.Buffer
	h := elf.Header64{Type: uint16(elf.ET_EXEC), Machine: uint16(m), Version: 1, Ehsize: 64}
	copy(h.Ident[:], "\x7fELF")
	h.Ident[elf.EI_CLASS], h.Ident[elf.EI_DATA], h.Ident[elf.EI_VERSION] = byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), 1
	binary.Write(&buf, binary.LittleEndian, h)
	return buf.Bytes()
}

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
		{"frp_0.71.0_linux_amd64/frps", elfBin, false},
		{"frp_0.71.0_linux_amd64/frpc", elfBin, false},
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
		{{"../frps", elfBin, false}},
		{{"/usr/local/bin/frps", elfBin, false}},
		{{"frp/../../frpc", elfBin, false}},
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
		{"frp_0.70.0_linux_amd64/frps", elfBin, false},
		{"frp_0.70.0_linux_amd64/frpc", elfBin, false},
	})
	if !installFromArchive(FR, arc, "0.70.0", []string{"frpc"}, func(string) {}) {
		t.Fatal("échec")
	}
	if b, _ := os.ReadFile(filepath.Join(frpBinDir, "frps")); string(b) != "ancien frps" {
		t.Fatal("frps remplacé")
	}
	if b, _ := os.ReadFile(filepath.Join(frpBinDir, "frpc")); !bytes.Equal(b, elfBin) {
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

// Archive refusée (illisible, tronquée, sans frps, autre processeur) : aucun
// service arrêté, aucun binaire remplacé ; une archive valide arrête puis
// relance les services.
// Rejected archive (unreadable, truncated, without frps, other processor): no
// service stopped, no binary replaced; a valid archive stops then restarts the
// services.
func TestBadArchiveKeepsServicesRunning(t *testing.T) {
	setup(t)
	saved := frpBinDir
	defer func() { frpBinDir = saved }()
	frpBinDir = t.TempDir()
	os.WriteFile(filepath.Join(frpBinDir, "frps"), []byte("ancien frps"), 0o755)
	var calls []string
	execCmd = func(args []string, _ time.Duration, _ *string) (bool, string, string) {
		switch {
		case len(args) > 1 && args[1] == "list-units":
			return true, "frps.service loaded active running frps\nfrpc.service loaded active running frpc\n", ""
		case len(args) > 2 && args[1] == "show":
			return true, "{ path=/usr/local/bin/" + strings.TrimSuffix(args[2], ".service") + " ; }", ""
		}
		calls = append(calls, strings.Join(args, " "))
		return true, "", ""
	}
	dir := t.TempDir()
	good := makeTar(t, filepath.Join(dir, "good.tar.gz"), []tarMember{
		{"frp_0.70.0_linux_amd64/frps", elfBin, false},
		{"frp_0.70.0_linux_amd64/frpc", elfBin, false},
	})
	data, _ := os.ReadFile(good)
	other := elf.EM_AARCH64
	if runtime.GOARCH == "arm64" {
		other = elf.EM_X86_64
	}
	bad := map[string]string{
		"illisible": filepath.Join(dir, "fake.tar.gz"),
		"tronquée":  filepath.Join(dir, "short.tar.gz"),
		"sans frps": makeTar(t, filepath.Join(dir, "nofrps.tar.gz"), []tarMember{{"frp/frpc", elfBin, false}}),
		"autre processeur": makeTar(t, filepath.Join(dir, "arm.tar.gz"), []tarMember{
			{"frp/frps", elfFor(other), false}, {"frp/frpc", elfFor(other), false}}),
	}
	os.WriteFile(bad["illisible"], []byte("pas une archive"), 0o644)
	os.WriteFile(bad["tronquée"], data[:len(data)/2], 0o644)
	for name, arc := range bad {
		calls = nil
		var lines []string
		if installFromArchive(EN, arc, "0.70.0", nil, func(s string) { lines = append(lines, s) }) {
			t.Errorf("%s : archive acceptée", name)
		}
		if len(calls) != 0 {
			t.Errorf("%s : services touchés %v", name, calls)
		}
		if b, _ := os.ReadFile(filepath.Join(frpBinDir, "frps")); string(b) != "ancien frps" {
			t.Errorf("%s : frps remplacé", name)
		}
		if len(lines) == 0 || !strings.HasPrefix(lines[len(lines)-1], "[ERROR]") {
			t.Errorf("%s : %v", name, lines)
		}
	}
	calls = nil
	if !installFromArchive(FR, good, "0.70.0", nil, func(string) {}) {
		t.Fatal("archive valide refusée")
	}
	if !reflect.DeepEqual(calls, []string{"systemctl stop frps", "systemctl stop frpc", "systemctl start frps", "systemctl start frpc"}) {
		t.Fatalf("%v", calls)
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
	os.WriteFile(filepath.Join(frpBinDir, "frps"), elfBin, 0o755)
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
	// Une plage de ports (modèle frp) n'empêche pas de lire [webServer]
	// A port range (frp template) does not prevent reading [webServer]
	if _, admin, err := frpcProxyStatus(conf("secret") + rangeTOML); !admin || err != nil {
		t.Fatal(admin, err)
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

func TestEnableFrpcAdmin(t *testing.T) {
	base := "serverAddr = \"vps.example.com\"\nserverPort = 7000\n\n[[proxies]]\nname = \"ssh\"\ntype = \"tcp\"\nlocalPort = 22\n"
	for _, c := range []string{base, "serverAddr = \"vps.example.com\"", ""} {
		out, changed, err := FR.enableFrpcAdmin(c, 7401)
		if err != nil || !changed {
			t.Fatal(c, err)
		}
		var conf map[string]any
		if err := toml.Unmarshal([]byte(out), &conf); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		ws := mapv(conf, "webServer")
		if ws["addr"] != "127.0.0.1" || tomlInt(ws["port"]) != 7401 || len(str(ws, "password", "")) != 32 {
			t.Fatalf("%v", ws)
		}
		// Clés à la racine, pas dans le dernier [[proxies]]
		if c == base && len(listv(conf, "proxies")) != 1 {
			t.Fatalf("%s", out)
		}
		if again, changed, _ := FR.enableFrpcAdmin(out, 7402); changed || again != out {
			t.Fatal("ajouté deux fois")
		}
	}
	// Plage de ports juste après la connexion : le bloc va avant la ligne
	// « range », pas dans la boucle
	// Port range right after the connection: the block goes before the "range"
	// line, not inside the loop
	out, changed, err := FR.enableFrpcAdmin("serverAddr = \"vps.example.com\"\n\n"+rangeTOML, 7401)
	if err != nil || !changed || strings.Index(out, "webServer.port") > strings.Index(out, "{{- range") {
		t.Fatalf("%v\n%s", err, out)
	}
	if err := FR.validateConfigContent("/etc/frp/frpc.toml", out); err != nil {
		t.Fatal(err)
	}
	if _, _, err := FR.enableFrpcAdmin("[webServer]\naddr = \"127.0.0.1\"\n", 7400); err == nil {
		t.Fatal("webServer sans port accepté")
	}
	// Commentaire dans la langue de l'interface, une seule langue par fichier
	// Comment in the interface language, a single language per file
	for _, tc := range []struct {
		l       Lang
		in, out string
	}{{FR, "# Tableau de bord local de frpc", "Local frpc dashboard"}, {EN, "# Local frpc dashboard", "Tableau de bord"}} {
		out, _, _ := tc.l.enableFrpcAdmin(base, 7401)
		if !strings.Contains(out, tc.in) || strings.Contains(out, tc.out) {
			t.Errorf("%s : %s", tc.l, out)
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
	// Zéros après la fin du tar, dans le flux gzip : plafonnés aussi
	// Zeros after the end of the tar, inside the gzip stream: capped too
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.Close()
	gz.Write(make([]byte, 8192))
	gz.Close()
	tail := filepath.Join(dir, "tail.tar.gz")
	os.WriteFile(tail, buf.Bytes(), 0o644)
	if _, err := extractFrpBinaries(FR, tail, dir); err == nil || !strings.Contains(err.Error(), "volumineuse") {
		t.Fatalf("bombe après le tar acceptée : %v", err)
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
	// Plage de ports : un tunnel par paire, nommé comme frpc le fera
	// Port range: one tunnel per pair, named the way frpc will name it
	got = nil
	for _, p := range extractPortsFromConfig(validTOML+"\n"+rangeTOML, "frpc") {
		got = append(got, p["label"].(string)+":"+p["proto"].(string)+":"+strings.TrimSpace(pyStr(float64(p["port"].(int)))))
	}
	want = []string{"Connexion serveur:tcp:7000", "Tunnel ssh (remotePort):tcp:6000",
		"Tunnel jeu-30000 (remotePort):udp:40000", "Tunnel jeu-30001 (remotePort):udp:40001", "Tunnel jeu-30002 (remotePort):udp:40005"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	// Plage stcp ou http : aucun port côté serveur ; visiteur en plage ouvert au
	// réseau : un port par visiteur. La ligne « range » d'un visiteur ne passe pas
	// au tunnel suivant.
	// stcp or http range: no port on the server side; range visitor open to the
	// network: one port per visitor. A visitor's "range" line does not pass to the
	// next tunnel.
	got = nil
	conf := validTOML + "\n" + stcpRangeTOML + "\n" + httpRangeTOML + "\n" + visitorRangeTOML +
		"\n[[proxies]]\nname = \"web\"\ntype = \"tcp\"\nlocalPort = 80\nremotePort = 8000\n"
	for _, p := range extractPortsFromConfig(conf, "frpc") {
		got = append(got, p["label"].(string)+":"+p["proto"].(string)+":"+strings.TrimSpace(pyStr(float64(p["port"].(int)))))
	}
	want = []string{"Connexion serveur:tcp:7000", "Tunnel ssh (remotePort):tcp:6000", "Tunnel web (remotePort):tcp:8000",
		"Visiteur acc-16000 (bindPort):tcp:16000", "Visiteur acc-16001 (bindPort):tcp:16001", "Visiteur acc-16002 (bindPort):tcp:16002"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	if ports := extractPortsFromConfig(strings.Replace(visitorRangeTOML, "0.0.0.0", "127.0.0.1", 1), "frpc"); len(ports) != 0 {
		t.Fatalf("visiteur local listé : %v", ports)
	}
	frps := "bindPort = 7000\nkcpBindPort = 7000\nvhostHTTPPort = 80\n"
	if n := len(extractPortsFromConfig(frps, "frps")); n != 3 {
		t.Fatalf("%d ports frps", n)
	}
}

// Archive au flux deflate abîmé : le détail Go est traduit en français
// Archive with a damaged deflate stream: the Go detail is translated into French
func TestCorruptDeflateMessageFrench(t *testing.T) {
	dir := t.TempDir()
	good := makeTar(t, filepath.Join(dir, "good.tar.gz"), []tarMember{
		{"frp/frps", elfBin, false}, {"frp/frpc", elfBin, false},
	})
	data, _ := os.ReadFile(good)
	found := false
	for off := 10; off < 100 && off < len(data); off++ {
		d := append([]byte(nil), data...)
		for i := 0; i < 4 && off+i < len(d); i++ {
			d[off+i] ^= 0xff
		}
		p := filepath.Join(dir, "bad.tar.gz")
		os.WriteFile(p, d, 0o644)
		_, err := extractFrpBinaries(FR, p, t.TempDir())
		if err == nil {
			continue
		}
		for _, en := range []string{"flate:", "gzip:", "unexpected EOF", "archive/tar"} {
			if strings.Contains(err.Error(), en) {
				t.Fatalf("offset %d : détail en anglais dans « %s »", off, err)
			}
		}
		if strings.Contains(err.Error(), "abîmé") {
			found = true
		}
	}
	if !found {
		t.Error("aucune corruption flate rencontrée")
	}
}

// Ports lus malgré un commentaire ou des espaces dans l'en-tête de bloc
// Ports read despite a comment or spaces in the block header
func TestExtractPortsCommentedHeader(t *testing.T) {
	setup(t)
	for _, head := range []string{"[[proxies]]", "[[proxies]] # note", "  [[ proxies ]]", "[[proxies]]\t# note"} {
		content := "serverAddr = \"x\"\nserverPort = 7000\n\n" + head + "\nname = \"a\"\ntype = \"tcp\"\nlocalPort = 22\nremotePort = 18801\n\n[[visitors]] # v\nname = \"v\"\nbindAddr = \"0.0.0.0\"\nbindPort = 18999\n"
		ports := extractPortsFromConfig(content, "frpc")
		var got []int
		for _, p := range ports {
			got = append(got, p["port"].(int))
		}
		has := func(n int) bool {
			for _, g := range got {
				if g == n {
					return true
				}
			}
			return false
		}
		if !has(18801) || !has(18999) {
			t.Errorf("en-tête %q : ports lus %v", head, got)
		}
	}
}

// Forme en ligne « proxies = [ { … } ] » : ports lus
// Inline form "proxies = [ { … } ]": ports read
func TestExtractPortsInlineForm(t *testing.T) {
	setup(t)
	content := "serverAddr = \"x\"\nserverPort = 7000\nproxies = [\n  { name = \"a\", type = \"tcp\", localPort = 22, remotePort = 18801 },\n  { name = \"b\", type = \"udp\", localPort = 53, remotePort = 18802 },\n]\nvisitors = [ { name = \"v\", type = \"stcp\", bindAddr = \"0.0.0.0\", bindPort = 18999 } ]\n\n[webServer] # c\nport = 7500\n"
	got := map[int]string{}
	for _, p := range extractPortsFromConfig(content, "frpc") {
		got[p["port"].(int)] = p["proto"].(string)
	}
	for port, proto := range map[int]string{18801: "tcp", 18802: "udp", 18999: "tcp", 7500: "tcp"} {
		if got[port] != proto {
			t.Errorf("port %d : proto %q, attendu %q (lus %v)", port, got[port], proto, got)
		}
	}
}

// Plage commentée, forme en ligne sans doublon de ports, copie sans tunnels en ligne
// Commented range, inline form without duplicated ports, copy without inline tunnels
func TestInlineFormAndCommentedRange(t *testing.T) {
	setup(t)
	rng := "serverAddr = \"x\"\nserverPort = 7000\n{{- range $_, $v := parseNumberRangePair \"6000-6001\" \"18850-18851\" }} # r\n[[proxies]]\nname = \"rg-{{ $v.First }}\"\ntype = \"tcp\"\nlocalPort = {{ $v.First }}\nremotePort = {{ $v.Second }}\n{{- end }} # fin\n"
	if _, err := parseFrpConfig(rng); err != nil {
		t.Errorf("plage commentée refusée : %v", err)
	}
	count := map[int]int{}
	for _, p := range extractPortsFromConfig(rng, "frpc") {
		count[p["port"].(int)]++
	}
	if count[18850] != 1 || count[18851] != 1 {
		t.Errorf("ports de la plage commentée : %v", count)
	}
	mixed := "serverAddr = \"x\"\nserverPort = 7000\nvisitors = [ { name = \"v\", type = \"stcp\", bindAddr = \"0.0.0.0\", bindPort = 18999 } ]\n\n[[proxies]]\nname = \"a\"\ntype = \"tcp\"\nlocalPort = 1\nremotePort = 18801\n"
	count = map[int]int{}
	for _, p := range extractPortsFromConfig(mixed, "frpc") {
		count[p["port"].(int)]++
	}
	if count[18801] != 1 || count[18999] != 1 {
		t.Errorf("ports en doublon : %v", count)
	}
	inline := "serverAddr = \"x\"\nserverPort = 7000\nproxies = [\n  { name = \"a\", type = \"tcp\", localPort = 1, remotePort = 18801 },\n]\nvisitors = [ { name = \"v\" } ]\n[log]\nlevel = \"info\"\n"
	out := clientConnectionOnly(inline)
	if strings.Contains(out, "proxies") || strings.Contains(out, "visitors") || !strings.Contains(out, "[log]") {
		t.Errorf("copie : tunnels en ligne gardés ou connexion perdue :\n%s", out)
	}
}

// Clé entre guillemets et entiers à tirets bas : lus côté Go comme côté interface
// Quoted key and integers with underscores: read on the Go side like the interface
func TestQuotedInlineKeyAndUnderscoreInts(t *testing.T) {
	c := "serverAddr = \"127.0.0.1\"\nserverPort = 7000\n\"proxies\" = [ { name = \"q\", type = \"tcp\", localPort = 22, remotePort = 18_801 } ]\n"
	found := false
	for _, p := range extractPortsFromConfig(c, "frpc") {
		if p["port"] == 18801 {
			found = true
		}
	}
	if !found {
		t.Fatalf("port 18801 attendu / expected: %v", extractPortsFromConfig(c, "frpc"))
	}
	if out := clientConnectionOnly(c); strings.Contains(out, "proxies") {
		t.Fatalf("tunnel gardé / tunnel kept: %q", out)
	}
	b := "[[proxies]]\nname = \"a\"\ntype = \"tcp\"\nremotePort = 18_801\n"
	ok := false
	for _, p := range extractPortsFromConfig(b, "frpc") {
		if p["port"] == 18801 {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("bloc 18_801 / block 18_801: %v", extractPortsFromConfig(b, "frpc"))
	}
}

// Chaîne sur plusieurs lignes dont des lignes ressemblent à des en-têtes ou à
// des clés : ni la copie, ni l'écriture d'une clé, ni la lecture des ports ne
// s'y trompent
// Multi-line string with lines that look like headers or keys: neither the
// copy, nor writing a key, nor reading the ports is fooled
func TestMultilineStringLines(t *testing.T) {
	conn := "serverAddr = \"x\"\nserverPort = 7000\nmetadatas.m = \"\"\"\nu\n[[proxies]]\nremotePort = 18802\n[log]\n\"\"\"\n# note = '''\n"
	tail := "\n[[proxies]]\nname = \"a\"\ntype = \"tcp\"\nlocalPort = 22\nremotePort = 18801\nannotations.n = '''\n[[visitors]]\nremotePort = 18803\n'''\n\n[webServer]\nuser = \"\"\"\n[[proxies]]\nport = 18804\n\"\"\"\npassword = \"\"\"p\"\"\"\nport = 7400\n"
	c := conn + tail
	if _, err := parseFrpConfig(c); err != nil {
		t.Fatalf("entrée invalide / invalid input: %v", err)
	}
	got := map[int]int{}
	for _, p := range extractPortsFromConfig(c, "frpc") {
		got[p["port"].(int)]++
	}
	if len(got) != 3 || got[7000] != 1 || got[18801] != 1 || got[7400] != 1 {
		t.Errorf("ports : %v", got)
	}
	out := clientConnectionOnly(c)
	conf, err := parseFrpConfig(out)
	if err != nil {
		t.Fatalf("copie invalide / invalid copy: %v\n%s", err, out)
	}
	ws, _ := conf["webServer"].(map[string]any)
	if conf["proxies"] != nil || ws["user"] != "[[proxies]]\nport = 18804\n" || ws["port"] != int64(7400) || !strings.Contains(out, "remotePort = 18802\n[log]\n") {
		t.Errorf("copie / copy:\n%s", out)
	}
	set := setTOMLKey(c, "log.to", `"/tmp/x.log"`)
	if conf, err := parseFrpConfig(set); err != nil || tomlLookup(conf, "log.to") != "/tmp/x.log" || !strings.Contains(set, "u\n[[proxies]]\nremotePort = 18802\n[log]\n\"\"\"\n") {
		t.Errorf("setTOMLKey : %v\n%s", err, set)
	}
	if set := setTOMLKey(c, "webServer.port", "7401"); !strings.Contains(set, "port = 18804\n") || !strings.Contains(set, "port = 7401\n") {
		t.Errorf("setTOMLKey webServer.port :\n%s", set)
	}
	// Chaîne fermée sur sa ligne d'ouverture, commentaire qui ressemble à une ouverture
	// String closed on its opening line, comment that looks like an opening
	want := []bool{false, false, true, true, false, false, false}
	for i, in := range tomlStringLines("a = \"\"\"x\"\"\"\nb = '''\nl\n'''\n# c = \"\"\"\n[x]\nd = 1") {
		if in != want[i] {
			t.Errorf("ligne %d : %v", i, in)
		}
	}
}
