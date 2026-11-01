// frps avant frpc : relancés ensemble (mise à jour de frp, lancement du
// conteneur), un frpc local quitte aussitôt (« connection refused ») si frps
// n'écoute pas encore, ce qui arrive sur une machine lente ou émulée. Les frps
// sont donc lancés d'abord, puis le panel attend qu'ils écoutent sur leur port
// de contrôle (au plus frpsReadyTimeout) avant de lancer les frpc.
// frps before frpc: restarted together (frp update, container launch), a local
// frpc exits right away ("connection refused") if frps is not listening yet,
// which happens on a slow or emulated machine. So the frps are started first,
// then the panel waits for them to listen on their control port (at most
// frpsReadyTimeout) before starting the frpc.

package main

import (
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

var (
	// Délais et sonde remplaçables (tests).
	// Replaceable delays and probe (tests).
	frpsReadyTimeout = 15 * time.Second
	frpsReadyPoll    = 250 * time.Millisecond
	portListening    = localPortListening
)

// localPortListening : un programme écoute en TCP sur ce port. Lu dans
// /proc/1/net : l'espace réseau du PID 1 est celui de l'hôte (systemd, conteneur
// avec pid: host) ou celui du conteneur autonome, où tournent frps et frpc. Sans
// /proc, essai de connexion à 127.0.0.1.
// localPortListening: a program listens over TCP on this port. Read from
// /proc/1/net: PID 1's network namespace is the host's (systemd, container with
// pid: host) or the standalone container's, where frps and frpc run. Without
// /proc, connection attempt to 127.0.0.1.
func localPortListening(port int) bool {
	read := false
	for _, f := range []string{"/proc/1/net/tcp", "/proc/1/net/tcp6"} {
		if b, err := os.ReadFile(f); err == nil {
			read = true
			if procNetListening(string(b), port) {
				return true
			}
		}
	}
	if read {
		return false
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// procNetListening : le contenu de /proc/net/tcp(6) contient une socket en
// écoute (état 0A) sur ce port, quelle que soit l'adresse.
// procNetListening: the /proc/net/tcp(6) content holds a listening socket
// (state 0A) on this port, whatever the address.
func procNetListening(data string, port int) bool {
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[3] != "0A" {
			continue
		}
		i := strings.LastIndexByte(f[1], ':')
		if i < 0 {
			continue
		}
		if p, err := strconv.ParseUint(f[1][i+1:], 16, 16); err == nil && int(p) == port {
			return true
		}
	}
	return false
}

// frpsControlPort : port de contrôle (bindPort, 7000 par défaut) d'un frps,
// 0 si l'instance ou sa config est illisible.
// frpsControlPort: control port (bindPort, 7000 by default) of a frps, 0 if
// the instance or its config cannot be read.
func frpsControlPort(inst *Instance) int {
	if inst == nil || inst.Type != "frps" {
		return 0
	}
	text, ok := frpsConfigText(inst)
	if !ok {
		return 0
	}
	conf, err := parseFrpConfig(text)
	if err != nil {
		return 0
	}
	if p := tomlInt(conf["bindPort"]); p > 0 {
		return p
	}
	return 7000
}

// startServersFirst lance ids, frps d'abord ; avant le premier frpc, attend
// que les frps lancés écoutent (au plus frpsReadyTimeout en tout). Un frps qui
// ne tourne plus (alive) ou dont le port est inconnu (port → 0) n'est pas
// attendu. report reçoit chaque résultat de start, et l'avertissement d'attente.
// startServersFirst starts ids, frps first; before the first frpc, waits for
// the started frps to listen (at most frpsReadyTimeout in all). A frps that no
// longer runs (alive) or whose port is unknown (port → 0) is not waited for.
// report gets each start result, and the waiting warning.
func startServersFirst(l Lang, ids []string, start func(id string) (bool, string),
	alive func(id string) bool, port func(id string) int, report func(level, msg string)) {
	started := map[string]int{}
	waited := false
	for _, id := range serversFirst(append([]string(nil), ids...)) {
		isServer := strings.HasPrefix(id, "frps")
		if !isServer && !waited {
			waited = true
			if late := waitFrpsListening(started, alive); len(late) > 0 {
				secs := strconv.Itoa(int(frpsReadyTimeout / time.Second))
				list := strings.Join(late, ", ")
				report("[WARN]", l.M(list+" n'écoute pas encore après "+secs+" s : frpc lancé quand même",
					list+" is not listening yet after "+secs+" s: frpc started anyway"))
			}
		}
		ok, msg := start(id)
		if ok {
			report("[OK]", msg)
		} else {
			report("[WARN]", msg)
		}
		if ok && isServer {
			if p := port(id); p > 0 {
				started[id] = p
			}
		}
	}
}

// waitFrpsListening attend que chaque frps (id → port) écoute → ceux qui
// tournent encore sans écouter au bout de frpsReadyTimeout.
// waitFrpsListening waits for each frps (id → port) to listen → those still
// running without listening after frpsReadyTimeout.
func waitFrpsListening(ports map[string]int, alive func(id string) bool) []string {
	deadline := time.Now().Add(frpsReadyTimeout)
	for {
		var pending []string
		for _, id := range sortedKeys(ports) {
			if !portListening(ports[id]) && alive(id) {
				pending = append(pending, id)
			}
		}
		if len(pending) == 0 || !time.Now().Before(deadline) {
			return pending
		}
		time.Sleep(frpsReadyPoll)
	}
}
