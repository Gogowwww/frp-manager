// Mode autonome (conteneur Docker sans accès à l'hôte) : le panel lance lui-même
// frps et frpc, embarqués dans l'image, comme processus enfants. Pas de systemd
// ni de nsenter : démarrage, arrêt, démarrage automatique au lancement du
// conteneur, relance après un arrêt imprévu (délai croissant), arrêt propre au
// SIGTERM. La sortie de chaque programme va dans /var/log/frp/<id>.console.log.
// Standalone mode (Docker container without access to the host): the panel
// itself runs frps and frpc, shipped in the image, as child processes. No
// systemd and no nsenter: start, stop, start on container launch, restart after
// an unexpected exit (growing delay), clean stop on SIGTERM. Each program's
// output goes to /var/log/frp/<id>.console.log.

package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

// Programmes frp de l'image (hors des dossiers où le mode hôte cherche ceux de
// l'hôte) et dossier persistant où la mise à jour depuis le panel les installe.
// frp programs of the image (outside the folders where host mode looks for the
// host's) and persistent folder where an update from the panel installs them.
var (
	frpEmbeddedDir = "/opt/frp-manager/bin"
	frpPersistDir  = "/var/lib/frp-manager/bin"
)

// standaloneMode : FRP_MANAGER_MODE (standalone ou host) décide s'il est donné ;
// sinon, autonome dans un conteneur qui ne voit pas l'hôte (ni montages de
// l'hôte, ni systemd, ni pid: host, où le PID 1 est l'init de l'hôte).
// standaloneMode: FRP_MANAGER_MODE (standalone or host) decides when set;
// otherwise, standalone in a container that does not see the host (no host
// mounts, no systemd, no pid: host, where PID 1 is the host's init).
func standaloneMode(env string, container bool, exists func(string) bool, pid1 string) bool {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "standalone":
		return true
	case "host":
		return false
	}
	if !container {
		return false
	}
	hostVisible := exists("/host/usr/local/bin") || exists("/run/systemd/system") || pid1 == "systemd" || pid1 == "init"
	return !hostVisible
}

func pid1Name() string {
	b, _ := os.ReadFile("/proc/1/comm")
	return strings.TrimSpace(string(b))
}

// ── Dossier de données unique ───────────────────────────────────────────────
// Le docker-compose.yml autonome monte un seul volume sur /data : les chemins
// habituels du panel (/etc/frp…) y deviennent des liens, recréés à chaque
// lancement du conteneur. Un chemin déjà présent (volume monté à part) est
// laissé tel quel.
// The standalone docker-compose.yml mounts a single volume on /data: the
// panel's usual paths (/etc/frp…) become links into it, recreated at each
// container launch. A path that already exists (separately mounted volume) is
// left as is.

var dataLinks = [][2]string{
	{"/etc/frp-manager", "panel"},
	{"/etc/frp", "frp"},
	{"/var/log/frp", "log"},
	{"/var/lib/frp-manager", "lib"},
}

func linkDataDirs(data string) {
	if st, err := os.Stat(data); err != nil || !st.IsDir() {
		return
	}
	for _, l := range dataLinks {
		target := filepath.Join(data, l[1])
		if err := os.MkdirAll(target, 0o755); err != nil {
			log.Printf("[WARN] %s : %v", target, err)
			continue
		}
		if _, err := os.Lstat(l[0]); err == nil {
			continue
		}
		os.MkdirAll(filepath.Dir(l[0]), 0o755)
		if err := os.Symlink(target, l[0]); err != nil {
			log.Printf("[WARN] %s → %s : %v", l[0], target, err)
		}
	}
	// La configuration du panel contient sa clé de session et ses comptes
	// The panel configuration holds its session key and accounts
	os.Chmod(filepath.Join(data, "panel"), 0o700)
}

// ── Programmes ──────────────────────────────────────────────────────────────

// standaloneBinary : le programme du dossier persistant (mise à jour depuis le
// panel) passe avant celui de l'image, sauf si l'image en apporte un plus récent
// (nouvelle image téléchargée depuis).
// standaloneBinary: the program of the persistent folder (update from the
// panel) comes before the image's one, unless the image brings a newer one (new
// image pulled since).
func standaloneBinary(name string) string {
	persist, embedded := filepath.Join(frpPersistDir, name), filepath.Join(frpEmbeddedDir, name)
	switch {
	case !isExecutable(persist) && !isExecutable(embedded):
		return ""
	case !isExecutable(persist):
		return embedded
	case !isExecutable(embedded):
		return persist
	}
	if versionNewer(semverOf(readVersion(embedded)), semverOf(readVersion(persist))) {
		return embedded
	}
	return persist
}

var semverRe = regexp.MustCompile(`\d+\.\d+\.\d+`)

func semverOf(s string) string { return semverRe.FindString(s) }

// ── Instances ───────────────────────────────────────────────────────────────

// standaloneID : identifiant d'une config (/etc/frp/frpc-paris.toml → frpc-paris),
// "" si son nom ne ferait pas un nom de service valide.
// standaloneID: identifier of a config (/etc/frp/frpc-paris.toml → frpc-paris),
// "" if its name would not make a valid service name.
func standaloneID(conf, binType string) string {
	id := stem(conf)
	if !strings.HasPrefix(id, binType) || !validServiceName(id) {
		return ""
	}
	return id
}

// frpLogFile : fichier de log écrit par frp lui-même (log.to, en clé à points ou
// dans [log]), sinon <id>.log.
// frpLogFile: log file written by frp itself (log.to, as a dotted key or in
// [log]), otherwise <id>.log.
func frpLogFile(conf, id string) string {
	if b, err := os.ReadFile(conf); err == nil {
		var v map[string]any
		if toml.Unmarshal([]byte(frpTOML(string(b))), &v) == nil {
			if to := str(mapv(v, "log"), "to", ""); strings.HasPrefix(to, "/") {
				return to
			}
		}
	}
	return filepath.Join(frpLogDir, id+".log")
}

func consoleLog(id string) string { return filepath.Join(frpLogDir, id+".console.log") }

// standaloneInstances : une instance par config frps*/frpc* des dossiers de
// configuration, sauf celles retirées du tableau de bord.
// standaloneInstances: one instance per frps*/frpc* config of the configuration
// folders, except those removed from the dashboard.
func standaloneInstances() map[string]*Instance {
	instances := map[string]*Instance{}
	dismissed := map[string]bool{}
	for _, d := range stringList(cfg()["dismissed_configs"]) {
		dismissed[d] = true
	}
	for _, binType := range frpBinaries {
		bin := findBinary(binType)
		version := ""
		if bin != "" {
			version = readVersion(bin)
		} else {
			bin = filepath.Join(frpEmbeddedDir, binType)
		}
		for _, conf := range findAllConfigs(binType) {
			id := standaloneID(conf, binType)
			if id == "" || dismissed[conf] || instances[id] != nil {
				continue
			}
			instances[id] = &Instance{ID: id, Type: binType, Source: "standalone", Binary: bin, Version: version,
				Config: conf, Service: id, Log: frpLogFile(conf, id)}
		}
	}
	return instances
}

// ── Démarrage automatique (clé « autostart » de la config du panel) ─────────

func autostartIDs() map[string]bool {
	out := map[string]bool{}
	for _, id := range stringList(cfg()["autostart"]) {
		out[id] = true
	}
	return out
}

func setAutostart(id string, on bool) error {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	ids := autostartIDs()
	if ids[id] == on {
		return nil
	}
	if on {
		ids[id] = true
	} else {
		delete(ids, id)
	}
	list := make([]string, 0, len(ids))
	for k := range ids {
		list = append(list, k)
	}
	sort.Strings(list)
	_, err := cfgUpdateLocked(Cfg{"autostart": toAnyList(list)})
	return err
}

// ── Superviseur ─────────────────────────────────────────────────────────────

type frpProc struct {
	id       string
	mu       sync.Mutex
	cmd      *exec.Cmd
	wanted   bool          // doit tourner : relancé s'il s'arrête / must run: restarted if it stops
	exited   chan struct{} // fermé à la fin du processus en cours / closed when the current process ends
	retry    *time.Timer
	failures int // arrêts rapprochés, pour le délai de relance / close exits, for the restart delay
	lastExit string
}

var (
	procsMu sync.Mutex
	procs   = map[string]*frpProc{}

	// Délais de relance et d'arrêt : remplaçables (tests).
	// Restart and stop delays: replaceable (tests).
	restartBase  = 2 * time.Second
	restartMax   = 60 * time.Second
	stableAfter  = 60 * time.Second
	stopTimeout  = 8 * time.Second
	startObserve = 800 * time.Millisecond

	// frpCommand : commande lancée pour une instance. Remplaçable (tests).
	// frpCommand: command run for an instance. Replaceable (tests).
	frpCommand = func(inst *Instance) *exec.Cmd { return exec.Command(inst.Binary, "-c", inst.Config) }
)

func procFor(id string) *frpProc {
	procsMu.Lock()
	defer procsMu.Unlock()
	p := procs[id]
	if p == nil {
		p = &frpProc{id: id}
		procs[id] = p
	}
	return p
}

// restartDelay : 2 s, 4 s, 8 s… jusqu'à 60 s pour des arrêts rapprochés.
// restartDelay: 2 s, 4 s, 8 s… up to 60 s for close exits.
func restartDelay(failures int) time.Duration {
	d := restartBase
	for i := 1; i < failures && d < restartMax; i++ {
		d *= 2
	}
	return min(d, restartMax)
}

func (p *frpProc) runningLocked() bool { return p.cmd != nil }

// launchLocked démarre le programme (p.mu tenu).
// launchLocked starts the program (p.mu held).
func (p *frpProc) launchLocked(l Lang, inst *Instance) error {
	if !isExecutable(inst.Binary) {
		return errors.New(l.M("programme introuvable : "+inst.Binary, "program not found: "+inst.Binary))
	}
	if !fileExists(inst.Config) {
		return errors.New(l.M("configuration introuvable : "+inst.Config, "configuration not found: "+inst.Config))
	}
	os.MkdirAll(frpLogDir, 0o755)
	out := newConsoleWriter(consoleLog(p.id))
	cmd := frpCommand(inst)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		out.Close()
		return err
	}
	out.note(fmt.Sprintf("[frp-manager] %s %s -c %s (pid %d)", l.M("démarré :", "started:"), inst.Binary, inst.Config, cmd.Process.Pid))
	p.cmd, p.exited = cmd, make(chan struct{})
	started, exited := time.Now(), p.exited
	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		status := "0"
		if err != nil {
			status = err.Error()
		}
		p.cmd, p.lastExit = nil, status
		if time.Since(started) >= stableAfter {
			p.failures = 0
		}
		msg := l.M("[frp-manager] arrêté ("+status+")", "[frp-manager] stopped ("+status+")")
		if p.wanted {
			p.failures++
			delay := restartDelay(p.failures)
			msg += l.M(fmt.Sprintf(", relance dans %s", delay), fmt.Sprintf(", restarting in %s", delay))
			p.retry = time.AfterFunc(delay, func() { p.relaunch(l) })
		}
		out.note(msg)
		out.Close()
		close(exited)
		p.mu.Unlock()
		invalidateCache(false)
	}()
	return nil
}

// relaunch : relance après un arrêt imprévu, si l'instance existe toujours.
// relaunch: restart after an unexpected exit, if the instance still exists.
func (p *frpProc) relaunch(l Lang) {
	inst := standaloneInstances()[p.id]
	func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.retry = nil
		if !p.wanted || p.runningLocked() {
			return
		}
		if inst == nil {
			p.wanted = false
			return
		}
		if err := p.launchLocked(l, inst); err != nil {
			p.failures++
			delay := restartDelay(p.failures)
			appendConsole(p.id, l.M(fmt.Sprintf("[frp-manager] relance impossible (%v), nouvel essai dans %s", err, delay),
				fmt.Sprintf("[frp-manager] restart failed (%v), trying again in %s", err, delay)))
			p.retry = time.AfterFunc(delay, func() { p.relaunch(l) })
		}
	}()
	// Hors du verrou : detectFrp lit l'état sous detectMu puis p.mu
	// Outside the lock: detectFrp reads the state under detectMu then p.mu
	invalidateCache(false)
}

// start → (ok, message). Un programme qui s'arrête aussitôt est signalé (sa
// dernière ligne de sortie), et relancé comme après tout arrêt imprévu.
// start → (ok, message). A program that stops right away is reported (its last
// output line), and restarted like after any unexpected exit.
func (p *frpProc) start(l Lang, inst *Instance) (bool, string) {
	p.mu.Lock()
	if p.runningLocked() {
		p.wanted = true
		p.mu.Unlock()
		return true, l.M(p.id+" tourne déjà", p.id+" is already running")
	}
	if p.retry != nil {
		p.retry.Stop()
		p.retry = nil
	}
	p.failures = 0
	if err := p.launchLocked(l, inst); err != nil {
		p.mu.Unlock()
		return false, err.Error()
	}
	p.wanted = true
	exited := p.exited
	p.mu.Unlock()
	select {
	case <-exited:
		p.mu.Lock()
		status := p.lastExit
		p.mu.Unlock()
		last := lastConsoleLine(p.id)
		return false, strings.TrimSpace(l.M(p.id+" s'est arrêté aussitôt ("+status+") : "+last,
			p.id+" stopped right away ("+status+"): "+last))
	case <-time.After(startObserve):
		return true, l.M(p.id+" démarré", p.id+" started")
	}
}

// stop : SIGTERM, puis SIGKILL si le programme ne s'arrête pas à temps.
// stop: SIGTERM, then SIGKILL if the program does not stop in time.
func (p *frpProc) stop(l Lang) (bool, string) {
	p.mu.Lock()
	p.wanted = false
	if p.retry != nil {
		p.retry.Stop()
		p.retry = nil
	}
	cmd, exited := p.cmd, p.exited
	p.mu.Unlock()
	if cmd == nil {
		return true, l.M(p.id+" est déjà arrêté", p.id+" is already stopped")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		cmd.Process.Kill() // Windows (tests) : pas de SIGTERM / Windows (tests): no SIGTERM
	}
	select {
	case <-exited:
	case <-time.After(stopTimeout):
		cmd.Process.Kill()
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
			return false, l.M(p.id+" ne s'arrête pas", p.id+" does not stop")
		}
	}
	return true, l.M(p.id+" arrêté", p.id+" stopped")
}

// status : état au format de readStatuses (activating : relance en attente).
// status: state in the readStatuses format (activating: pending restart).
func (p *frpProc) status(enabled bool) J {
	p.mu.Lock()
	defer p.mu.Unlock()
	active := "inactive"
	switch {
	case p.runningLocked():
		active = "active"
	case p.wanted:
		active = "activating"
	}
	return J{"active": active, "enabled": enabled, "running": active == "active"}
}

// standaloneAction : systemctl start|stop|restart|reload|enable|disable, version
// superviseur. reload relance le programme (frps ne sait pas relire sa config).
// standaloneAction: systemctl start|stop|restart|reload|enable|disable, the
// supervisor way. reload restarts the program (frps cannot reread its config).
func (l Lang) standaloneAction(id, action string) (bool, string) {
	inst := standaloneInstances()[id]
	if inst == nil {
		return false, l.M("Instance inconnue : "+id, "Unknown instance: "+id)
	}
	p := procFor(id)
	switch action {
	case "start":
		return p.start(l, inst)
	case "stop":
		return p.stop(l)
	case "restart", "reload":
		if ok, msg := p.stop(l); !ok {
			return false, msg
		}
		return p.start(l, inst)
	case "enable", "disable":
		if err := setAutostart(id, action == "enable"); err != nil {
			return false, err.Error()
		}
		if action == "enable" {
			return true, l.M(id+" démarrera avec le conteneur", id+" will start with the container")
		}
		return true, l.M(id+" ne démarrera plus avec le conteneur", id+" will no longer start with the container")
	}
	return false, l.M("Action invalide", "Invalid action")
}

// standaloneStatuses : état des instances supervisées.
// standaloneStatuses: state of the supervised instances.
func standaloneStatuses(found map[string]J) map[string]J {
	auto := autostartIDs()
	out := map[string]J{}
	for iid, inst := range found {
		if inst["source"] != "standalone" {
			continue
		}
		if inst["binary_found"] != true {
			out[iid] = missingStatus()
			continue
		}
		out[iid] = procFor(iid).status(auto[iid])
	}
	return out
}

// standaloneRunning : instances lancées qui utilisent l'un des programmes bins.
// standaloneRunning: running instances that use one of the bins programs.
func standaloneRunning(bins []string) []string {
	var out []string
	for id, inst := range standaloneInstances() {
		for _, b := range bins {
			if inst.Type == b && procFor(id).status(false)["running"] == true {
				out = append(out, id)
			}
		}
	}
	return serversFirst(out)
}

// serversFirst : frps avant frpc (un frpc local se connecte dès son lancement),
// puis ordre alphabétique.
// serversFirst: frps before frpc (a local frpc connects as soon as it starts),
// then alphabetical order.
func serversFirst(ids []string) []string {
	sort.Slice(ids, func(i, j int) bool {
		si, sj := strings.HasPrefix(ids[i], "frps"), strings.HasPrefix(ids[j], "frps")
		if si != sj {
			return si
		}
		return ids[i] < ids[j]
	})
	return ids
}

// standaloneForget : instance supprimée, plus lancée ni démarrée automatiquement.
// standaloneForget: deleted instance, no longer running nor started automatically.
func standaloneForget(id string) {
	procFor(id).stop(systemLang())
	setAutostart(id, false)
}

// ── Lancement et arrêt du conteneur ─────────────────────────────────────────

// standaloneBoot : configs par défaut au tout premier lancement, puis démarrage
// des instances au démarrage automatique.
// standaloneBoot: default configs on the very first launch, then start of the
// instances set to start automatically.
func standaloneBoot() {
	l := systemLang()
	ensureStandaloneConfigs(l)
	detectFrp(true)
	insts := standaloneInstances()
	for _, id := range serversFirst(sortedKeys(autostartIDs())) {
		inst := insts[id]
		if inst == nil {
			continue
		}
		ok, msg := procFor(id).start(l, inst)
		level := "[INFO]"
		if !ok {
			level = "[WARN]"
		}
		log.Printf("%s %s", level, msg)
	}
	invalidateCache(false)
}

// ensureStandaloneConfigs crée frps.toml et frpc.toml au premier lancement
// seulement (une config supprimée depuis le panel ne revient pas). frps écoute
// sur FRPS_BIND_PORT, sinon sur un port tiré au hasard comme avec install.sh, et
// reçoit un jeton tiré au hasard.
// ensureStandaloneConfigs creates frps.toml and frpc.toml on the first launch
// only (a config deleted from the panel does not come back). frps listens on
// FRPS_BIND_PORT, otherwise on a random port as with install.sh, and gets a
// random token.
func ensureStandaloneConfigs(l Lang) {
	st := loadState()
	if truthy(st["standalone_configs"]) {
		return
	}
	if err := os.MkdirAll(frpConfDir, 0o755); err != nil {
		log.Printf("[WARN] %s : %v", frpConfDir, err)
		return
	}
	for _, bin := range frpBinaries {
		if len(findAllConfigs(bin)) > 0 {
			continue
		}
		content := defaultConfigs[bin]
		if bin == "frps" {
			port := frpsBindPort(os.Getenv("FRPS_BIND_PORT"))
			content = strings.Replace(content, "bindPort = 7000", "bindPort = "+strconv.Itoa(port), 1)
			content = strings.Replace(content, `auth.token = "changeme"`, `auth.token = "`+tokenHex(16)+`"`, 1)
		}
		conf := filepath.Join(frpConfDir, bin+".toml")
		if err := os.WriteFile(conf, []byte(content), 0o600); err != nil {
			log.Printf("[WARN] %s : %v", conf, err)
			continue
		}
		log.Printf("[INFO] %s", l.M("Configuration par défaut créée : "+conf, "Default configuration created: "+conf))
	}
	st["standalone_configs"] = true
	if err := saveState(st); err != nil {
		log.Printf("[WARN] %v", err)
	}
}

// frpsBindPort : FRPS_BIND_PORT (numéro, ou « random »), sinon un port entre 10000
// et 32767 : les robots qui balaient Internet visent 7000.
// frpsBindPort: FRPS_BIND_PORT (number, or "random"), otherwise a port between
// 10000 and 32767: bots scanning the Internet target 7000.
func frpsBindPort(env string) int {
	if p, err := strconv.Atoi(strings.TrimSpace(env)); err == nil && p >= 1 && p <= 65535 {
		return p
	}
	n, _ := strconv.ParseUint(tokenHex(2), 16, 32)
	return 10000 + int(n)%22768
}

// handleShutdown : au SIGTERM (docker stop) ou SIGINT, frps et frpc sont arrêtés
// proprement avant la sortie du panel.
// handleShutdown: on SIGTERM (docker stop) or SIGINT, frps and frpc are stopped
// cleanly before the panel exits.
func handleShutdown() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	sig := <-ch
	log.Printf("[INFO] %v : arrêt de frps et frpc", sig)
	stopAllProcs()
	os.Exit(0)
}

func stopAllProcs() {
	procsMu.Lock()
	list := make([]*frpProc, 0, len(procs))
	for _, p := range procs {
		list = append(list, p)
	}
	procsMu.Unlock()
	var wg sync.WaitGroup
	l := systemLang()
	for _, p := range list {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.stop(l)
		}()
	}
	wg.Wait()
}

// ── Sortie des programmes ───────────────────────────────────────────────────
// Fichier par instance, plafonné : au-delà de 5 Mo il devient <fichier>.1 (un
// seul ancien fichier gardé), ce que « tail -F » suit.
// One file per instance, capped: past 5 MB it becomes <file>.1 (a single old
// file kept), which "tail -F" follows.

var consoleMaxBytes int64 = 5 << 20

type consoleWriter struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

func newConsoleWriter(path string) *consoleWriter { return &consoleWriter{path: path} }

func (w *consoleWriter) openLocked() error {
	if w.f != nil {
		return nil
	}
	f, err := os.OpenFile(w.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	st, _ := f.Stat()
	w.f, w.size = f, 0
	if st != nil {
		w.size = st.Size()
	}
	return nil
}

func (w *consoleWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size+int64(len(b)) > consoleMaxBytes && w.f != nil {
		w.f.Close()
		w.f = nil
		os.Rename(w.path, w.path+".1")
	}
	if err := w.openLocked(); err != nil {
		return len(b), nil // log illisible : le programme continue / unwritable log: the program goes on
	}
	n, err := w.f.Write(b)
	w.size += int64(n)
	return n, err
}

// note : ligne du panel, horodatée, dans la sortie de l'instance.
// note: panel line, timestamped, in the instance's output.
func (w *consoleWriter) note(s string) {
	w.Write([]byte(time.Now().Format("2006-01-02 15:04:05") + " " + s + "\n"))
}

func (w *consoleWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

func appendConsole(id, s string) {
	w := newConsoleWriter(consoleLog(id))
	w.note(s)
	w.Close()
}

// tailLines : les n dernières lignes d'un fichier (lu depuis la fin).
// tailLines: the last n lines of a file (read from the end).
func tailLines(path string, n int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	const chunk = 64 << 10
	size := st.Size()
	start := max(0, size-chunk)
	for {
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return "", err
		}
		buf, err := io.ReadAll(io.LimitReader(f, size-start))
		if err != nil {
			return "", err
		}
		text := strings.TrimRight(string(buf), "\n")
		lines := strings.Split(text, "\n")
		if len(lines) > n || start == 0 {
			if start > 0 {
				lines = lines[1:] // première ligne peut-être coupée / first line maybe cut
			}
			if len(lines) > n {
				lines = lines[len(lines)-n:]
			}
			return validUTF8(strings.Join(lines, "\n")), nil
		}
		start = max(0, start-chunk*4)
	}
}

// lastConsoleLine : dernière ligne écrite par le programme (pas par le panel).
// lastConsoleLine: last line written by the program (not by the panel).
func lastConsoleLine(id string) string {
	text, _ := tailLines(consoleLog(id), 20)
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" && !strings.Contains(s, "[frp-manager]") {
			return s
		}
	}
	return ""
}

// standaloneConsole : sortie récente d'une instance (page Logs, source « Console »).
// standaloneConsole: recent output of an instance (Logs page, "Console" source).
func (l Lang) standaloneConsole(id string, n int) string {
	text, err := tailLines(consoleLog(id), n)
	if err != nil {
		return l.M("(aucune sortie : "+id+" n'a pas encore été lancé)", "(no output: "+id+" has not been started yet)")
	}
	return text
}
