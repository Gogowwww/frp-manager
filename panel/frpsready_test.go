// frps avant frpc : sonde du port de contrôle et ordre de lancement.
// frps before frpc: control port probe and start order.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProcNetListening(t *testing.T) {
	// 0x1B58 = 7000 en écoute (0A), 0x1F90 = 8080 seulement connecté (01)
	// 0x1B58 = 7000 listening (0A), 0x1F90 = 8080 only connected (01)
	tcp := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 00000000:1B58 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 0 100 0 0 10 0\n" +
		"   1: 0100007F:1F90 0100007F:D431 01 00000000:00000000 00:00000000 00000000     0        0 2 1 0 20 4 30 10 -1\n"
	tcp6 := "  sl  local_address                         remote_address                        st\n" +
		"   0: 00000000000000000000000000000000:2710 00000000000000000000000000000000:0000 0A 00000000:00000000\n"
	for _, c := range []struct {
		data string
		port int
		want bool
	}{
		{tcp, 7000, true}, {tcp, 8080, false}, {tcp, 9000, false},
		{tcp6, 10000, true}, {"", 7000, false}, {"en-tête seul\n", 7000, false},
	} {
		if got := procNetListening(c.data, c.port); got != c.want {
			t.Errorf("port %d : %v, attendu %v", c.port, got, c.want)
		}
	}
}

// fakeReady : frps « écoute » après n sondes ; les lancements sont notés dans l'ordre.
// fakeReady: frps "listens" after n probes; starts are recorded in order.
type fakeReady struct {
	mu      sync.Mutex
	probes  int
	after   int
	started []string
	logs    []string
}

func (f *fakeReady) install(t *testing.T) {
	savedProbe, savedTimeout, savedPoll := portListening, frpsReadyTimeout, frpsReadyPoll
	t.Cleanup(func() { portListening, frpsReadyTimeout, frpsReadyPoll = savedProbe, savedTimeout, savedPoll })
	frpsReadyPoll = time.Millisecond
	portListening = func(port int) bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.probes++
		return port == 7000 && f.probes > f.after
	}
}

func (f *fakeReady) start(id string) (bool, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, id)
	if id == "frpc" && f.probes <= f.after {
		return false, "frpc : connection refused"
	}
	return true, id
}

func (f *fakeReady) report(level, msg string) { f.logs = append(f.logs, level+" "+msg) }

func TestStartServersFirstWaitsForFrps(t *testing.T) {
	f := &fakeReady{after: 5}
	f.install(t)
	frpsReadyTimeout = 5 * time.Second
	alive := func(string) bool { return true }
	port := func(id string) int {
		if id == "frps" {
			return 7000
		}
		return 0
	}
	startServersFirst(EN, []string{"frpc", "frps"}, f.start, alive, port, f.report)
	if got := strings.Join(f.started, ","); got != "frps,frpc" {
		t.Fatalf("ordre : %s", got)
	}
	if f.probes <= f.after {
		t.Fatalf("frpc lancé avant que frps écoute (%d sondes)", f.probes)
	}
	if got := strings.Join(f.logs, "|"); got != "[OK] frps|[OK] frpc" {
		t.Fatalf("journal : %s", got)
	}
}

func TestStartServersFirstDoesNotBlock(t *testing.T) {
	alive := func(string) bool { return true }
	port7000 := func(string) int { return 7000 }
	cases := []struct {
		name  string
		ids   []string
		alive func(string) bool
		port  func(string) int
	}{
		// frps arrêté aussitôt : rien à attendre
		// frps stopped right away: nothing to wait for
		{"frps arrêté", []string{"frps", "frpc"}, func(string) bool { return false }, port7000},
		// Port inconnu (config illisible) : pas d'attente
		// Unknown port (unreadable config): no wait
		{"port inconnu", []string{"frps", "frpc"}, alive, func(string) int { return 0 }},
		// frpc seul (frps distant) : pas de sonde
		// frpc alone (remote frps): no probe
		{"frpc seul", []string{"frpc"}, alive, port7000},
	}
	for _, c := range cases {
		f := &fakeReady{after: 1 << 30}
		f.install(t)
		frpsReadyTimeout = time.Hour
		begin := time.Now()
		startServersFirst(EN, c.ids, func(id string) (bool, string) { f.started = append(f.started, id); return true, id }, c.alive, c.port, f.report)
		if time.Since(begin) > 5*time.Second {
			t.Errorf("%s : attente de %s", c.name, time.Since(begin))
		}
		if len(f.started) != len(c.ids) {
			t.Errorf("%s : lancés %v", c.name, f.started)
		}
	}
}

func TestStartServersFirstTimeout(t *testing.T) {
	f := &fakeReady{after: 1 << 30}
	f.install(t)
	frpsReadyTimeout = 30 * time.Millisecond
	startServersFirst(EN, []string{"frps", "frpc"}, func(id string) (bool, string) { f.started = append(f.started, id); return true, id },
		func(string) bool { return true }, func(string) int { return 7000 }, f.report)
	if got := strings.Join(f.started, ","); got != "frps,frpc" {
		t.Fatalf("frpc non lancé après le délai : %s", got)
	}
	if len(f.logs) != 3 || !strings.HasPrefix(f.logs[1], "[WARN] frps is not listening yet") {
		t.Fatalf("avertissement attendu : %v", f.logs)
	}
}

func TestFrpsControlPort(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) *Instance {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(content), 0o600)
		return &Instance{ID: "frps", Type: "frps", Source: "standalone", Config: p}
	}
	for _, c := range []struct {
		inst *Instance
		want int
	}{
		{write("a.toml", "bindPort = 12345\n"), 12345},
		{write("b.toml", "auth.token = \"x\"\n"), 7000},
		{write("c.toml", "bindPort = = \n"), 0},
		{&Instance{ID: "frps", Type: "frps", Config: filepath.Join(dir, "absent.toml")}, 0},
		{&Instance{ID: "frpc", Type: "frpc", Config: filepath.Join(dir, "a.toml")}, 0},
		{nil, 0},
	} {
		if got := frpsControlPort(c.inst); got != c.want {
			t.Errorf("%v : %d, attendu %d", c.inst, got, c.want)
		}
	}
}
