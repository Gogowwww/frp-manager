// Mode autonome : détection, choix des programmes, superviseur (démarrage,
// arrêt, relance après un arrêt imprévu), API et pare-feu.
// Standalone mode: detection, choice of programs, supervisor (start, stop,
// restart after an unexpected exit), API and firewall.

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStandaloneModeDetection(t *testing.T) {
	none := func(string) bool { return false }
	only := func(p string) func(string) bool { return func(s string) bool { return s == p } }
	cases := []struct {
		name, env string
		container bool
		exists    func(string) bool
		pid1      string
		want      bool
	}{
		{"hors conteneur", "", false, none, "systemd", false},
		{"conteneur seul", "", true, none, "frp-manager", true},
		{"conteneur avec docker-init", "", true, none, "docker-init", true},
		{"programmes de l'hôte montés", "", true, only("/host/usr/local/bin"), "frp-manager", false},
		{"systemd de l'hôte monté", "", true, only("/run/systemd/system"), "frp-manager", false},
		{"pid: host (init systemd)", "", true, none, "systemd", false},
		{"pid: host (init)", "", true, none, "init", false},
		{"forcé autonome", "standalone", true, only("/host/usr/local/bin"), "systemd", true},
		{"forcé autonome hors conteneur", "Standalone", false, none, "systemd", true},
		{"forcé hôte", "host", true, none, "frp-manager", false},
		{"valeur inconnue : détection", "auto", true, none, "frp-manager", true},
	}
	for _, c := range cases {
		if got := standaloneMode(c.env, c.container, c.exists, c.pid1); got != c.want {
			t.Errorf("%s : %v, attendu %v", c.name, got, c.want)
		}
	}
}

func TestStandaloneIDs(t *testing.T) {
	for conf, want := range map[string]string{
		"/etc/frp/frps.toml": "frps", "/etc/frp/frpc-paris.toml": "frpc-paris", "/etc/frp/frpc.ini": "frpc",
		"/etc/frp/frpc mon serveur.toml": "", "/etc/frp/frpc;id.toml": "",
	} {
		bin := "frpc"
		if strings.HasPrefix(filepath.Base(conf), "frps") {
			bin = "frps"
		}
		if got := standaloneID(conf, bin); got != want {
			t.Errorf("%s : %q, attendu %q", conf, got, want)
		}
	}
	if standaloneID("/etc/frp/frps.toml", "frpc") != "" {
		t.Error("frps.toml compté comme frpc")
	}
	if got := strings.Join(serversFirst([]string{"frpc", "frps-b", "frpc2", "frps"}), ","); got != "frps,frps-b,frpc,frpc2" {
		t.Errorf("ordre de lancement : %s", got)
	}
}

func TestFrpLogFile(t *testing.T) {
	dir := t.TempDir()
	saved := frpLogDir
	t.Cleanup(func() { frpLogDir = saved })
	frpLogDir = "/var/log/frp"
	for content, want := range map[string]string{
		"log.to = \"/var/log/frp/a.log\"\n":            "/var/log/frp/a.log",
		"[log]\nto = \"/data/log/b.log\"\nlevel = 1\n": "/data/log/b.log",
		"log.to = \"console\"\n":                       "/var/log/frp/frpc.log",
		"serverAddr = \"x\"\n":                         "/var/log/frp/frpc.log",
		"cassé = = \n":                                 "/var/log/frp/frpc.log",
	} {
		p := filepath.Join(dir, "frpc.toml")
		os.WriteFile(p, []byte(content), 0o600)
		if got := frpLogFile(p, "frpc"); filepath.ToSlash(got) != want {
			t.Errorf("%q : %s, attendu %s", content, got, want)
		}
	}
}

func TestRestartDelay(t *testing.T) {
	want := []time.Duration{2 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second,
		32 * time.Second, 60 * time.Second, 60 * time.Second}
	for i, w := range want {
		if got := restartDelay(i); got != w {
			t.Errorf("restartDelay(%d) = %s, attendu %s", i, got, w)
		}
	}
}

func TestFrpsBindPort(t *testing.T) {
	if frpsBindPort("7000") != 7000 || frpsBindPort(" 443 ") != 443 {
		t.Fatal("FRPS_BIND_PORT ignoré")
	}
	for _, env := range []string{"", "random", "0", "70000", "abc"} {
		if p := frpsBindPort(env); p < 10000 || p > 32767 {
			t.Errorf("%q → %d", env, p)
		}
	}
}

func TestLinkDataDirs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("liens symboliques : Linux (CI)")
	}
	root := t.TempDir()
	saved := dataLinks
	t.Cleanup(func() { dataLinks = saved })
	kept := filepath.Join(root, "etc", "kept")
	os.MkdirAll(kept, 0o755)
	dataLinks = [][2]string{{filepath.Join(root, "etc", "frp"), "frp"}, {kept, "kept"}}
	data := filepath.Join(root, "data")
	linkDataDirs(data) // /data absent : rien / missing /data: nothing
	if _, err := os.Lstat(dataLinks[0][0]); err == nil {
		t.Fatal("lien créé sans /data")
	}
	os.MkdirAll(data, 0o755)
	linkDataDirs(data)
	if target, err := os.Readlink(dataLinks[0][0]); err != nil || target != filepath.Join(data, "frp") {
		t.Fatalf("lien : %q %v", target, err)
	}
	if st, err := os.Lstat(kept); err != nil || st.Mode()&os.ModeSymlink != 0 {
		t.Fatal("dossier existant remplacé par un lien")
	}
	linkDataDirs(data) // relancement du conteneur / container restart
	if _, err := os.Readlink(dataLinks[0][0]); err != nil {
		t.Fatal(err)
	}
}

func TestTailLines(t *testing.T) {
	p := filepath.Join(t.TempDir(), "out.log")
	var b strings.Builder
	for i := 1; i <= 50000; i++ {
		fmt.Fprintf(&b, "ligne %d\n", i)
	}
	os.WriteFile(p, []byte(b.String()), 0o644)
	got, err := tailLines(p, 3)
	if err != nil || got != "ligne 49998\nligne 49999\nligne 50000" {
		t.Fatalf("%q %v", got, err)
	}
	os.WriteFile(p, []byte("une\ndeux\n"), 0o644)
	if got, _ := tailLines(p, 200); got != "une\ndeux" {
		t.Fatalf("%q", got)
	}
	if _, err := tailLines(filepath.Join(t.TempDir(), "absent"), 5); err == nil {
		t.Fatal("fichier absent sans erreur")
	}
}

func TestConsoleWriterRotation(t *testing.T) {
	saved := consoleMaxBytes
	t.Cleanup(func() { consoleMaxBytes = saved })
	consoleMaxBytes = 100
	p := filepath.Join(t.TempDir(), "frpc.console.log")
	w := newConsoleWriter(p)
	for i := 0; i < 12; i++ {
		w.Write([]byte("0123456789\n"))
	}
	w.Close()
	cur, _ := os.ReadFile(p)
	old, err := os.ReadFile(p + ".1")
	if err != nil || len(cur) > 100 || len(old) > 100 || len(cur)+len(old) == 0 {
		t.Fatalf("rotation : %d + %d octets (%v)", len(cur), len(old), err)
	}
}

// TestFakeFrp n'est pas un test : c'est le faux frps/frpc lancé par le
// superviseur (même programme de test, FRPM_FAKE_FRP donne son comportement).
// TestFakeFrp is not a test: it is the fake frps/frpc run by the supervisor
// (same test program, FRPM_FAKE_FRP gives its behavior).
func TestFakeFrp(t *testing.T) {
	switch os.Getenv("FRPM_FAKE_FRP") {
	case "":
		return
	case "run":
		fmt.Println("fake frp: login to server success")
		time.Sleep(time.Minute)
	case "crash":
		fmt.Println("fake frp: crashing")
		time.Sleep(150 * time.Millisecond)
		os.Exit(1)
	case "fail":
		fmt.Println("fake frp: parse config error")
		os.Exit(3)
	}
	os.Exit(0)
}

// standaloneEnv : mode autonome dans des dossiers temporaires, frps et frpc
// remplacés par TestFakeFrp (comportement mode).
// standaloneEnv: standalone mode in temporary folders, frps and frpc replaced
// by TestFakeFrp (behavior mode).
func standaloneEnv(t *testing.T, mode *string) *testEnv {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("superviseur : Linux (CI, image Docker)")
	}
	e := setup(t)
	saved := struct {
		standalone                              bool
		persist, embedded, binDir, logDir, conf string
		search                                  []string
		cmd                                     func(*Instance) *exec.Cmd
		base, observe, stop                     time.Duration
	}{standalone, frpPersistDir, frpEmbeddedDir, frpBinDir, frpLogDir, frpConfDir, binarySearchPaths, frpCommand,
		restartBase, startObserve, stopTimeout}
	t.Cleanup(func() {
		stopAllProcs()
		procsMu.Lock()
		procs = map[string]*frpProc{}
		procsMu.Unlock()
		standalone, frpPersistDir, frpEmbeddedDir, frpBinDir = saved.standalone, saved.persist, saved.embedded, saved.binDir
		frpLogDir, frpConfDir, binarySearchPaths, frpCommand = saved.logDir, saved.conf, saved.search, saved.cmd
		restartBase, startObserve, stopTimeout = saved.base, saved.observe, saved.stop
	})
	standalone = true
	frpPersistDir = filepath.Join(e.dir, "persist")
	frpEmbeddedDir = filepath.Join(e.dir, "embedded")
	frpBinDir = frpPersistDir
	frpLogDir = filepath.Join(e.dir, "log")
	frpConfDir = configSearchPaths[0]
	os.MkdirAll(frpEmbeddedDir, 0o755)
	os.MkdirAll(frpConfDir, 0o755)
	for _, b := range frpBinaries {
		os.WriteFile(filepath.Join(frpEmbeddedDir, b), []byte("#!/bin/sh\n"), 0o755)
	}
	restartBase, startObserve, stopTimeout = 50*time.Millisecond, 400*time.Millisecond, 3*time.Second
	frpCommand = func(inst *Instance) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestFakeFrp$")
		cmd.Env = append(os.Environ(), "FRPM_FAKE_FRP="+*mode)
		return cmd
	}
	discoverFn, readStatusesFn = discover, readStatuses
	invalidateCache(true)
	return e
}

func statusOfInst(t *testing.T, c *client, iid string) map[string]any {
	t.Helper()
	insts, _ := c.get("/api/detect").json()["instances"].(map[string]any)
	inst, _ := insts[iid].(map[string]any)
	if inst == nil {
		t.Fatalf("%s absent : %v", iid, insts)
	}
	st, _ := inst["status"].(map[string]any)
	return st
}

func TestStandaloneSupervisor(t *testing.T) {
	mode := "run"
	e := standaloneEnv(t, &mode)
	ensureStandaloneConfigs(FR)
	conf, err := os.ReadFile(filepath.Join(frpConfDir, "frps.toml"))
	if err != nil || strings.Contains(string(conf), "changeme") || strings.Contains(string(conf), "bindPort = 7000") {
		t.Fatalf("frps.toml par défaut : %q %v", conf, err)
	}
	// Une config supprimée ne revient pas au lancement suivant
	// A deleted config does not come back on the next launch
	os.Remove(filepath.Join(frpConfDir, "frpc.toml"))
	ensureStandaloneConfigs(FR)
	if fileExists(filepath.Join(frpConfDir, "frpc.toml")) {
		t.Fatal("frpc.toml recréé")
	}
	os.WriteFile(filepath.Join(frpConfDir, "frpc.toml"), []byte(defaultFrpcConfig), 0o600)

	c := e.owner()
	r := c.get("/api/detect").json()
	if r["standalone"] != true {
		t.Fatalf("standalone absent : %v", r)
	}
	insts := r["instances"].(map[string]any)
	frps := insts["frps"].(map[string]any)
	if frps["source"] != "standalone" || frps["binary_found"] != true || len(insts) != 2 {
		t.Fatalf("instances : %v", insts)
	}

	// Démarrage, état, sortie dans la page Logs, arrêt
	// Start, state, output in the Logs page, stop
	d := c.post("/api/service/frps/start", nil).json()
	if d["ok"] != true {
		t.Fatalf("start : %v", d)
	}
	if st := statusOfInst(t, c, "frps"); st["running"] != true || st["active"] != "active" {
		t.Fatalf("état après start : %v", st)
	}
	time.Sleep(200 * time.Millisecond)
	if logs := c.get("/api/logs/frps").json()["content"].(string); !strings.Contains(logs, "login to server success") ||
		!strings.Contains(logs, "[frp-manager]") {
		t.Fatalf("logs : %q", logs)
	}
	if d := c.post("/api/service/frps/restart", nil).json(); d["ok"] != true {
		t.Fatalf("restart : %v", d)
	}
	if d := c.post("/api/service/frps/stop", nil).json(); d["ok"] != true {
		t.Fatalf("stop : %v", d)
	}
	if st := statusOfInst(t, c, "frps"); st["running"] != false || st["active"] != "inactive" {
		t.Fatalf("état après stop : %v", st)
	}

	// Démarrage automatique : enregistré, sans démarrer
	// Automatic start: saved, without starting
	if d := c.post("/api/service/frpc/enable", nil).json(); d["ok"] != true {
		t.Fatalf("enable : %v", d)
	}
	if st := statusOfInst(t, c, "frpc"); st["enabled"] != true || st["running"] != false {
		t.Fatalf("état après enable : %v", st)
	}
	if got := stringList(e.savedConfig()["autostart"]); len(got) != 1 || got[0] != "frpc" {
		t.Fatalf("autostart : %v", got)
	}
	// Au lancement du conteneur, frpc démarre seul
	// On container launch, frpc starts on its own
	standaloneBoot()
	if st := statusOfInst(t, c, "frpc"); st["running"] != true {
		t.Fatalf("frpc non démarré au lancement : %v", st)
	}
	// Arrêt du conteneur : tout s'arrête
	// Container stop: everything stops
	stopAllProcs()
	if st := statusOfInst(t, c, "frpc"); st["running"] != false {
		t.Fatalf("frpc tourne encore : %v", st)
	}
	if d := c.post("/api/service/frpc/disable", nil).json(); d["ok"] != true || len(stringList(e.savedConfig()["autostart"])) != 0 {
		t.Fatalf("disable : %v", d)
	}
	if d := c.post("/api/service/frpc/bogus", nil); d.code != 400 {
		t.Fatalf("action inconnue : %d", d.code)
	}
}

func TestStandaloneCrashRestart(t *testing.T) {
	mode := "crash"
	e := standaloneEnv(t, &mode)
	os.WriteFile(filepath.Join(frpConfDir, "frpc.toml"), []byte(defaultFrpcConfig), 0o600)
	c := e.owner()
	c.post("/api/service/frpc/start", nil)
	// Relances successives (50 ms, 100 ms, 200 ms…) après chaque arrêt
	// Successive restarts (50 ms, 100 ms, 200 ms…) after each exit
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(consoleLog("frpc"))
		if strings.Count(string(b), "fake frp: crashing") >= 3 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	b, _ := os.ReadFile(consoleLog("frpc"))
	if n := strings.Count(string(b), "fake frp: crashing"); n < 3 {
		t.Fatalf("%d lancement(s) seulement :\n%s", n, b)
	}
	// Arrêt demandé : plus aucune relance
	// Requested stop: no more restarts
	if d := c.post("/api/service/frpc/stop", nil).json(); d["ok"] != true {
		t.Fatalf("stop : %v", d)
	}
	b, _ = os.ReadFile(consoleLog("frpc"))
	time.Sleep(600 * time.Millisecond)
	after, _ := os.ReadFile(consoleLog("frpc"))
	if strings.Count(string(after), "fake frp: crashing") != strings.Count(string(b), "fake frp: crashing") {
		t.Fatal("relancé après l'arrêt")
	}
}

func TestStandaloneStartFailsRightAway(t *testing.T) {
	mode := "fail"
	e := standaloneEnv(t, &mode)
	os.WriteFile(filepath.Join(frpConfDir, "frps.toml"), []byte(defaultFrpsConfig), 0o600)
	c := e.owner()
	d := c.post("/api/service/frps/start", nil).json()
	if d["ok"] != false || !strings.Contains(fmt.Sprint(d["msg"]), "parse config error") {
		t.Fatalf("échec non signalé : %v", d)
	}
	// Config absente : refus clair, rien n'est lancé
	// Missing config: clear refusal, nothing is run
	os.Remove(filepath.Join(frpConfDir, "frps.toml"))
	invalidateCache(true)
	if r := c.post("/api/service/frps/start", nil); r.code != 404 && r.json()["ok"] != false {
		t.Fatalf("config absente : %d %s", r.code, r.text())
	}
}

func TestStandaloneBinaryChoice(t *testing.T) {
	mode := "run"
	standaloneEnv(t, &mode)
	// Image seule / Image only
	if got := findBinary("frps"); got != filepath.Join(frpEmbeddedDir, "frps") {
		t.Fatalf("image : %s", got)
	}
	// Mise à jour depuis le panel : le dossier persistant passe avant
	// Update from the panel: the persistent folder comes first
	os.MkdirAll(frpPersistDir, 0o755)
	os.WriteFile(filepath.Join(frpPersistDir, "frps"), []byte("#!/bin/sh\n"), 0o755)
	versions := map[string]string{filepath.Join(frpPersistDir, "frps"): "0.72.0", filepath.Join(frpEmbeddedDir, "frps"): "0.71.1"}
	execCmd = func(args []string, _ time.Duration, _ *string) (bool, string, string) {
		if v, ok := versions[args[0]]; ok && len(args) == 2 && args[1] == "--version" {
			return true, v, ""
		}
		return false, "", "command not found"
	}
	versionCacheMu.Lock()
	versionCache = map[string]versionEntry{}
	versionCacheMu.Unlock()
	if got := findBinary("frps"); got != filepath.Join(frpPersistDir, "frps") {
		t.Fatalf("persistant : %s", got)
	}
	if v := frpInstalledVersion(); v != "0.72.0" {
		t.Fatalf("version installée : %v", v)
	}
	// Nouvelle image plus récente que la mise à jour : l'image reprend la main
	// New image newer than the update: the image takes over again
	versions[filepath.Join(frpEmbeddedDir, "frps")] = "0.73.0"
	versionCacheMu.Lock()
	versionCache = map[string]versionEntry{}
	versionCacheMu.Unlock()
	if got := findBinary("frps"); got != filepath.Join(frpEmbeddedDir, "frps") {
		t.Fatalf("image plus récente : %s", got)
	}
}

func TestStandaloneDeleteAndReinstall(t *testing.T) {
	mode := "run"
	e := standaloneEnv(t, &mode)
	conf := filepath.Join(frpConfDir, "frpc.toml")
	os.WriteFile(conf, []byte(defaultFrpcConfig), 0o600)
	c := e.owner()
	c.post("/api/service/frpc/enable", nil)
	if d := c.post("/api/service/frpc/start", nil).json(); d["ok"] != true {
		t.Fatalf("start : %v", d)
	}
	// Supprimée (config gardée) : arrêtée, plus au démarrage automatique, retirée
	// Deleted (config kept): stopped, no longer started automatically, removed
	if d := c.del("/api/instance/frpc").json(); d["ok"] != true {
		t.Fatalf("suppression : %v", d)
	}
	if procFor("frpc").status(false)["running"] != false || len(stringList(e.savedConfig()["autostart"])) != 0 {
		t.Fatal("frpc encore lancé ou au démarrage automatique")
	}
	if _, ok := c.get("/api/detect").json()["instances"].(map[string]any)["frpc"]; ok || !fileExists(conf) {
		t.Fatal("frpc toujours affiché, ou config supprimée")
	}
	// Réinstallation : la config revient, sans unité systemd
	// Reinstall: the config comes back, without a systemd unit
	if d := c.post("/api/instance/reinstall/frpc", nil).json(); d["ok"] != true {
		t.Fatalf("réinstallation : %v", d)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if running, _ := updateLog.state(); !running {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, ok := c.get("/api/detect").json()["instances"].(map[string]any)["frpc"]; !ok {
		t.Fatalf("frpc non réinstallé : %v", updateLog.snapshot())
	}
}

func TestStandaloneFirewall(t *testing.T) {
	mode := "run"
	e := standaloneEnv(t, &mode)
	os.WriteFile(filepath.Join(frpConfDir, "frps.toml"), []byte(defaultFrpsConfig), 0o600)
	var calls [][]string
	netAdmin := false
	execCmd = func(args []string, _ time.Duration, _ *string) (bool, string, string) {
		calls = append(calls, args)
		switch {
		case args[0] == "nft" && args[1] == "--version":
			return true, "nftables v1.1.3", ""
		case args[0] == "nft" && !netAdmin:
			return false, "", "Operation not permitted"
		case args[0] == "nft":
			return true, "", ""
		}
		return false, "", args[0] + ": command not found"
	}
	c := e.owner()
	d := c.get("/api/firewall").json()
	if d["available"] != false || d["standalone"] != true || !strings.Contains(fmt.Sprint(d["nft"]), "not permitted") {
		t.Fatalf("sans NET_ADMIN : %v", d)
	}
	for _, call := range calls {
		if call[0] == "nsenter" {
			t.Fatalf("nsenter en mode autonome : %v", call)
		}
	}
	netAdmin = true
	if d := c.get("/api/firewall").json(); d["available"] != true {
		t.Fatalf("avec NET_ADMIN : %v", d)
	}
	// Pas d'installation de paquet dans le conteneur
	// No package installation in the container
	if r := c.post("/api/firewall/install", nil); r.code != 400 || !strings.Contains(r.text(), "NET_ADMIN") {
		t.Fatalf("installation : %d %s", r.code, r.text())
	}
}
