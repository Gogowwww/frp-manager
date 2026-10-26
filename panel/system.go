// Système : état persistant, validation des noms, commandes (sur l'hôte quand le
// panel tourne dans Docker), API Docker, journaux en direct, détection des
// instances frps / frpc.

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

// ── Chemins ─────────────────────────────────────────────────────────────────

var (
	frpBinDir    = "/usr/local/bin"
	frpConfDir   = "/etc/frp"
	frpLogDir    = "/var/log/frp"
	frpStateFile = "/var/lib/frp-manager/state.json"

	binarySearchPaths = []string{"/usr/local/bin", "/usr/bin", "/usr/sbin", "/opt/frp", "/opt/frp/bin", "/root/frp", "/srv/frp"}
	configSearchPaths = []string{"/etc/frp", "/usr/local/etc/frp", "/opt/frp", "/root/frp"}

	// Détecter si le panel tourne dans un conteneur Docker
	inDocker = fileExists("/.dockerenv") || os.Getenv("DOCKER_MODE") == "true"
	// Mode autonome : frps et frpc de l'image, lancés par le panel (standalone.go)
	// Standalone mode: the image's frps and frpc, run by the panel (standalone.go)
	standalone = standaloneMode(os.Getenv("FRP_MANAGER_MODE"), inDocker, fileExists, pid1Name())
	// nsenter vers l'hôte : seulement dans un vrai conteneur, en mode hôte
	// nsenter to the host: only in a real container, in host mode
	dockerEnv    = fileExists("/.dockerenv") && !standalone
	dockerSocket = "/var/run/docker.sock"
)

func init() {
	if standalone {
		// Volume /data, puis le dossier persistant (mise à jour depuis le panel)
		// avant les programmes de l'image
		// /data volume, then the persistent folder (update from the panel)
		// before the image's programs
		data := os.Getenv("FRP_MANAGER_DATA")
		if data == "" {
			data = "/data"
		}
		linkDataDirs(data)
		frpBinDir = frpPersistDir
		binarySearchPaths = []string{frpPersistDir, frpEmbeddedDir}
		return
	}
	// Support Docker : /host/usr/local/bin est le /usr/local/bin de l'hôte monté
	// via docker-compose. S'il existe, les binaires frp y sont lus ET écrits.
	if fileExists("/host/usr/local/bin") {
		frpBinDir = "/host/usr/local/bin"
		binarySearchPaths = append([]string{frpBinDir}, binarySearchPaths...)
	}
}

const defaultFrpsConfig = "bindAddr = \"0.0.0.0\"\nbindPort = 7000\n\nauth.method = \"token\"\nauth.token = \"changeme\"\n\nlog.to = \"/var/log/frp/frps.log\"\nlog.level = \"info\"\nlog.maxDays = 3\n"
const defaultFrpcConfig = "serverAddr = \"\"\nserverPort = 7000\n\nauth.method = \"token\"\nauth.token = \"changeme\"\n\nlog.to = \"/var/log/frp/frpc.log\"\nlog.level = \"info\"\nlog.maxDays = 3\n"

var defaultConfigs = map[string]string{"frps": defaultFrpsConfig, "frpc": defaultFrpcConfig}

// ── État (/var/lib/frp-manager/state.json) ──────────────────────────────────

func loadState() map[string]any {
	if raw, err := os.ReadFile(frpStateFile); err == nil {
		var s map[string]any
		if json.Unmarshal(raw, &s) == nil && s != nil {
			return s
		}
	}
	return map[string]any{"installed_version": nil, "last_update_check": nil, "last_update_result": nil}
}

func saveState(s map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(frpStateFile), 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(frpStateFile, data, 0o644)
}

// mirrorsAllowed : miroirs tiers autorisés ? (Réglages ; FRP_MANAGER_NO_MIRRORS=1 les interdit)
func mirrorsAllowed() bool {
	switch os.Getenv("FRP_MANAGER_NO_MIRRORS") {
	case "1", "true", "yes":
		return false
	}
	v, ok := cfg()["download_mirrors"]
	return !ok || v != false
}

// ── Validation des noms ─────────────────────────────────────────────────────
// Tout nom qui finit dans une commande (systemctl, journalctl) ou dans une
// adresse de l'API Docker passe par ces listes blanches. Les commandes sont
// toujours des listes d'arguments, jamais une chaîne passée à un shell.

var (
	serviceNameRe   = regexp.MustCompile(`^frp[sc][A-Za-z0-9_.@-]{0,60}$`)
	containerNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	instanceIDRe    = regexp.MustCompile(`^(?:docker_)?[A-Za-z0-9][A-Za-z0-9_.@-]{0,127}$`)
)

func validServiceName(s string) bool   { return serviceNameRe.MatchString(s) }
func validContainerName(s string) bool { return containerNameRe.MatchString(s) }
func validInstanceID(s string) bool    { return instanceIDRe.MatchString(s) }

// ── Commandes ───────────────────────────────────────────────────────────────

var nsenterPrefix = []string{"nsenter", "-t", "1", "-m", "-u", "-i", "-n", "-p", "--"}

// execCmd lance une commande → (réussie, sortie, erreurs). Remplaçable (tests).
var execCmd = runProcess

func runProcess(args []string, timeout time.Duration, input *string) (bool, string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if input != nil {
		cmd.Stdin = strings.NewReader(*input)
	}
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return false, "", "timeout"
	}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return false, "", err.Error()
		}
	}
	return err == nil, strings.TrimSpace(out.String()), strings.TrimSpace(errb.String())
}

// runCmd : dans Docker, nsenter atteint le systemd/journalctl/ufw de l'hôte.
func runCmd(args []string, timeout ...time.Duration) (bool, string, string) {
	t := 15 * time.Second
	if len(timeout) > 0 {
		t = timeout[0]
	}
	if dockerEnv && len(args) > 0 {
		switch args[0] {
		case "systemctl", "journalctl", "ufw", "iptables", "ip6tables":
			args = append(append([]string(nil), nsenterPrefix...), args...)
		}
	}
	return execCmd(args, t, nil)
}

// runHost : commande sur l'HÔTE (en Docker : nsenter dans les namespaces de PID 1).
func runHost(args []string, timeout time.Duration, input *string) (bool, string, string) {
	if dockerEnv {
		args = append(append([]string(nil), nsenterPrefix...), args...)
	}
	return execCmd(args, timeout, input)
}

// hostReadFile : fichier de l'hôte (ok=false si absent ou illisible).
func hostReadFile(p string) (string, bool) {
	if !dockerEnv {
		b, err := os.ReadFile(p)
		return string(b), err == nil
	}
	ok, out, _ := runHost([]string{"cat", p}, 15*time.Second, nil)
	return out, ok
}

func hostWriteFile(p, content string) bool {
	if !dockerEnv {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return false
		}
		return os.WriteFile(p, []byte(content), 0o644) == nil
	}
	ok, _, _ := runHost([]string{"tee", "--", p}, 15*time.Second, &content)
	return ok
}

func hostRemoveFile(p string) bool {
	if !dockerEnv {
		err := os.Remove(p)
		return err == nil || os.IsNotExist(err)
	}
	ok, _, _ := runHost([]string{"rm", "-f", p}, 15*time.Second, nil)
	return ok
}

func getArch() string {
	switch runtime.GOARCH {
	case "arm64":
		return "arm64"
	case "arm":
		return "arm"
	}
	return "amd64"
}

func (l Lang) serviceAction(name, action string) (bool, string) {
	if !validServiceName(name) {
		return false, l.M(fmt.Sprintf("Nom de service refusé : %q", name), fmt.Sprintf("Service name rejected: %q", name))
	}
	if standalone {
		return l.standaloneAction(name, action)
	}
	ok, out, errs := runCmd([]string{"systemctl", action, name})
	if errs != "" {
		return ok, errs
	}
	return ok, out
}

func isExecutable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}

// ── Socket Docker ───────────────────────────────────────────────────────────

func dockerClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", dockerSocket)
			},
		},
	}
}

// dockerAPI : requête REST vers le socket Docker → (statut HTTP, données JSON ou texte).
func dockerAPI(method, path string, body any, timeout ...time.Duration) (int, any) {
	if !fileExists(dockerSocket) {
		return 0, nil
	}
	t := 10 * time.Second
	if len(timeout) > 0 {
		t = timeout[0]
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, "http://localhost/v1.41"+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := dockerClient(t).Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var data any
	if json.Unmarshal(raw, &data) != nil {
		data = strings.ToValidUTF8(string(raw), "�")
	}
	return resp.StatusCode, data
}

// isDockerFrame : début d'une trame du flux multiplexé Docker (conteneur SANS
// tty) : type 0/1/2 puis 3 octets nuls. Avec tty, le texte arrive brut.
func isDockerFrame(buf []byte) bool {
	return len(buf) >= 4 && buf[0] <= 2 && buf[1] == 0 && buf[2] == 0 && buf[3] == 0
}

// dockerDemux : texte complet de buf → (texte, reste non consommé).
func dockerDemux(buf []byte, tty bool) (string, []byte) {
	if tty {
		cut := bytes.LastIndexByte(buf, '\n') + 1 // ne pas couper un caractère UTF-8
		return strings.ToValidUTF8(string(buf[:cut]), "�"), buf[cut:]
	}
	var out strings.Builder
	// Trame : type[1] + remplissage[3] + taille[4] (gros-boutiste) + contenu
	for len(buf) >= 8 {
		size := int(binary.BigEndian.Uint32(buf[4:8]))
		if len(buf) < 8+size {
			break
		}
		out.WriteString(strings.ToValidUTF8(string(buf[8:8+size]), "�"))
		buf = buf[8+size:]
	}
	return out.String(), buf
}

// dockerLogsRaw : dernières lignes de logs d'un conteneur, décodées.
func dockerLogsRaw(container string, tail int) string {
	if !fileExists(dockerSocket) {
		return ""
	}
	resp, err := dockerClient(15 * time.Second).Get(fmt.Sprintf("http://localhost/v1.41/containers/%s/logs?stdout=1&stderr=1&tail=%d", container, tail))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	raw, _ := io.ReadAll(resp.Body)
	tty := len(raw) > 0 && !isDockerFrame(raw)
	text, rest := dockerDemux(raw, tty)
	if tty {
		return text + strings.ToValidUTF8(string(rest), "�")
	}
	return text
}

// resolveContainer : nom du conteneur, ou son id court s'il n'est pas trouvé sous ce nom.
func resolveContainer(container string) string {
	if st, _ := dockerAPI("GET", "/containers/"+container+"/json", nil); st == 200 {
		return container
	}
	if st, data := dockerAPI("GET", "/containers/json?all=true", nil); st == 200 {
		if list, ok := data.([]any); ok {
			for _, item := range list {
				c, _ := item.(map[string]any)
				for _, n := range stringList(c["Names"]) {
					if strings.TrimLeft(n, "/") == container {
						id := str(c, "Id", container)
						if len(id) > 12 {
							id = id[:12]
						}
						return id
					}
				}
			}
		}
	}
	return container
}

// ── Journaux en direct ──────────────────────────────────────────────────────
// Journal systemd (journalctl -f), fichier de log (tail -F) ou logs Docker en
// follow, ligne par ligne. Annuler le contexte arrête le processus lu ou coupe
// la connexion Docker, ce qui ferme le canal.

func liveLogLines(ctx context.Context, l Lang, inst *Instance, source string, history int) <-chan string {
	ch := make(chan string, 64)
	go func() {
		defer close(ch)
		send := func(s string) bool {
			select {
			case ch <- s:
				return true
			case <-ctx.Done():
				return false
			}
		}
		switch {
		case inst.Source == "docker":
			dockerFollow(ctx, l, resolveContainer(inst.ContainerName), history, send)
		case source == "file":
			if inst.Log == "" {
				send(l.M("[frp-manager] aucun fichier de log connu pour cette instance", "[frp-manager] no known log file for this instance"))
				return
			}
			// -F : suit le fichier même après une rotation des logs
			commandLines(ctx, []string{"tail", fmt.Sprintf("-n%d", history), "-F", inst.Log}, send)
		case inst.Source == "standalone":
			// Sortie du programme lancé par le panel, à la place du journal systemd
			// Output of the program run by the panel, instead of the systemd journal
			commandLines(ctx, []string{"tail", fmt.Sprintf("-n%d", history), "-F", consoleLog(inst.ID)}, send)
		default:
			if !validServiceName(inst.Service) {
				send(l.M("[frp-manager] nom de service refusé", "[frp-manager] service name rejected"))
				return
			}
			commandLines(ctx, []string{"journalctl", "-u", inst.Service, "-f", fmt.Sprintf("-n%d", history),
				"--no-pager", "-o", "short-iso"}, send)
		}
	}()
	return ch
}

func commandLines(ctx context.Context, args []string, send func(string) bool) {
	if dockerEnv {
		// Comme runCmd : journal et fichiers de log sont ceux de l'hôte
		args = append(append([]string(nil), nsenterPrefix...), args...)
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 3 * time.Second
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		pw.Close()
		return
	}
	go func() { cmd.Wait(); pw.Close() }()
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if !send(strings.ToValidUTF8(sc.Text(), "�")) {
			break
		}
	}
	pr.Close()
}

func dockerFollow(ctx context.Context, l Lang, container string, history int, send func(string) bool) {
	if !fileExists(dockerSocket) {
		send(l.M("[frp-manager] socket Docker indisponible", "[frp-manager] Docker socket unavailable"))
		return
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://localhost/v1.41/containers/%s/logs?stdout=1&stderr=1&follow=1&tail=%d", container, history), nil)
	resp, err := dockerClient(0).Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		send(l.M(fmt.Sprintf("[frp-manager] logs Docker indisponibles (HTTP %d)", resp.StatusCode),
			fmt.Sprintf("[frp-manager] Docker logs unavailable (HTTP %d)", resp.StatusCode)))
		return
	}
	var buf []byte
	var tty *bool
	pending := ""
	chunk := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			if tty == nil {
				if len(buf) < 4 {
					continue
				}
				t := !isDockerFrame(buf)
				tty = &t
			}
			var text string
			text, buf = dockerDemux(buf, *tty)
			pending += text
			parts := strings.Split(pending, "\n")
			pending = parts[len(parts)-1]
			for _, line := range parts[:len(parts)-1] {
				if !send(strings.TrimRight(line, "\r")) {
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// ── Détection multi-instances ───────────────────────────────────────────────
// Deux étages, pour ne pas lancer une douzaine de processus toutes les 2 s :
//   - la DÉCOUVERTE (services systemd, programmes, configs, conteneurs) change
//     rarement : refaite toutes les 30 s, ou aussitôt après une action qui la
//     modifie (installation, suppression, configuration enregistrée) ;
//   - l'ÉTAT (en marche, démarrage automatique) : un seul « systemctl show »
//     pour toutes les instances et une seule liste Docker, partagés 1,5 s entre
//     tous les appelants (onglets ouverts, WebSocket, requêtes).

type Instance struct {
	ID            string
	Type          string // frps | frpc
	Source        string // systemd | docker
	ContainerName string
	NetworkMode   string
	Image         string
	Binary        string
	Version       string // "" : inconnue
	Config        string // "" : aucune
	Service       string
	Log           string
}

const (
	discoveryTTL = 30 * time.Second
	statusTTL    = 1500 * time.Millisecond
)

var (
	detectMu        sync.Mutex
	instancesMu     sync.RWMutex
	instancesMap    = map[string]*Instance{}
	detectCache     = map[string]J{} // résultat de la découverte, sans l'état
	detectCacheTime time.Time
	statusCache     = map[string]J{}
	statusTime      time.Time
	versionCacheMu  sync.Mutex
	versionCache    = map[string]versionEntry{} // binaire → (mtime, sortie de --version)
)

type versionEntry struct {
	mtime time.Time
	ver   string
}

func stoppedStatus() J { return J{"active": "inactive", "enabled": false, "running": false} }
func missingStatus() J { return J{"active": "not-installed", "enabled": false, "running": false} }

// instancesSnapshot : instances découvertes (copie de la table).
func instancesSnapshot() map[string]*Instance {
	instancesMu.RLock()
	defer instancesMu.RUnlock()
	out := make(map[string]*Instance, len(instancesMap))
	for k, v := range instancesMap {
		out[k] = v
	}
	return out
}

func getInstance(iid string) *Instance {
	instancesMu.RLock()
	defer instancesMu.RUnlock()
	return instancesMap[iid]
}

func findBinary(name string) string {
	if standalone {
		return standaloneBinary(name)
	}
	for _, d := range binarySearchPaths {
		if c := filepath.Join(d, name); isExecutable(c) {
			return c
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	if ok, out, _ := runCmd([]string{"pgrep", "-a", name}); ok && out != "" {
		for _, line := range strings.Split(out, "\n") {
			if parts := strings.Fields(line); len(parts) >= 2 && isExecutable(parts[1]) {
				return parts[1]
			}
		}
	}
	return ""
}

// readVersion : « frps --version », relu seulement si le binaire a changé.
func readVersion(bin string) string {
	st, err := os.Stat(bin)
	if err != nil {
		return ""
	}
	versionCacheMu.Lock()
	cached, ok := versionCache[bin]
	versionCacheMu.Unlock()
	if ok && cached.mtime.Equal(st.ModTime()) {
		return cached.ver
	}
	ver := ""
	if ok, out, _ := runCmd([]string{bin, "--version"}); ok {
		ver = strings.TrimSpace(out)
	}
	versionCacheMu.Lock()
	versionCache[bin] = versionEntry{st.ModTime(), ver}
	versionCacheMu.Unlock()
	return ver
}

// systemctlShow : « systemctl show » de plusieurs unités en un seul appel.
func systemctlShow(units, props []string) map[string]map[string]string {
	result := map[string]map[string]string{}
	if len(units) == 0 {
		return result
	}
	args := []string{"systemctl", "show", "--no-pager", "-p", "Id"}
	for _, p := range props {
		args = append(args, "-p", p)
	}
	_, out, _ := runCmd(append(args, units...))
	block := map[string]string{}
	for _, line := range append(strings.Split(out, "\n"), "") {
		if strings.TrimSpace(line) == "" {
			if block["Id"] != "" {
				result[block["Id"]] = block
			}
			block = map[string]string{}
			continue
		}
		k, v, _ := strings.Cut(line, "=")
		block[k] = v
	}
	return result
}

var (
	// Noms d'unités frps/frpc : les anciens (frpc, frpc2) et ceux à suffixe
	// (frpc-maison, créés par « Nouvelle instance »), sans les modèles « @ ».
	// frps/frpc unit names: the old ones (frpc, frpc2) and suffixed ones
	// (frpc-maison, created by "New instance"), without "@" templates.
	unitNameRe       = regexp.MustCompile(`^frp[sc][A-Za-z0-9_.-]{0,60}\.service$`)
	legacyUnitNameRe = regexp.MustCompile(`^frp[sc]\d*\.service$`)
	execPathRe       = regexp.MustCompile(`path=([^\s;]+)`)
	execConfigRe     = regexp.MustCompile(`-c\s+([^\s;]+)`)
	serverAddrRe     = regexp.MustCompile(`(?m)^\s*(?:serverAddr|server_addr)\s*[=:]\s*["']?([^"'\s#]+)`)
)

type unitCfg struct{ name, config string }

// findSystemdUnits : services frps*/frpc* dont la commande lance vraiment frps/frpc.
func findSystemdUnits() map[string][]unitCfg {
	_, out, _ := runCmd([]string{"systemctl", "list-unit-files", "--type=service", "--no-pager", "--plain", "--no-legend"})
	var candidates []string
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) > 0 && unitNameRe.MatchString(f[0]) {
			candidates = append(candidates, f[0])
		}
	}
	execs := systemctlShow(candidates, []string{"ExecStart"})
	found := map[string][]unitCfg{"frps": nil, "frpc": nil}
	for _, unit := range candidates {
		bin := unit[:4]
		prop := execs[unit]["ExecStart"]
		if !strings.Contains(prop, "/"+bin) && !strings.Contains(prop, " "+bin) {
			continue
		}
		// Nom à suffixe : le programme lancé doit s'appeler exactement frps ou
		// frpc (frps-exporter.service lance frps_exporter, pas frps)
		// Suffixed name: the program run must be named exactly frps or frpc
		// (frps-exporter.service runs frps_exporter, not frps)
		if !legacyUnitNameRe.MatchString(unit) {
			m := execPathRe.FindStringSubmatch(prop)
			if m == nil || path.Base(m[1]) != bin {
				continue
			}
		}
		conf := ""
		if m := execConfigRe.FindStringSubmatch(prop); m != nil {
			conf = m[1]
		}
		found[bin] = append(found[bin], unitCfg{strings.TrimSuffix(unit, ".service"), conf})
	}
	return found
}

func findAllConfigs(binType string) []string {
	var found []string
	seen := map[string]bool{}
	for _, d := range configSearchPaths {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			continue
		}
		for _, ext := range []string{".toml", ".ini", ".yaml", ".yml"} {
			matches, _ := filepath.Glob(filepath.Join(d, binType+"*"+ext))
			sort.Strings(matches)
			for _, p := range matches {
				if !seen[p] {
					seen[p] = true
					found = append(found, p)
				}
			}
		}
	}
	return found
}

// frpcConfigured : un serveur est-il renseigné ? install.sh crée partout un
// frpc.toml avec serverAddr = "" : sans ça, un frpc jamais utilisé compterait.
func frpcConfigured(p string) bool {
	if p == "" {
		return false
	}
	b, err := os.ReadFile(p)
	return err == nil && serverAddrRe.Match(b)
}

func stem(p string) string {
	b := filepath.Base(p)
	return strings.TrimSuffix(b, filepath.Ext(b))
}

func detectDockerContainers() map[string]*Instance {
	out := map[string]*Instance{}
	if !fileExists(dockerSocket) {
		return out
	}
	st, data := dockerAPI("GET", "/containers/json?all=true", nil)
	list, ok := data.([]any)
	if st != 200 || !ok {
		return out
	}
	for _, item := range list {
		c, _ := item.(map[string]any)
		names := stringList(c["Names"])
		name := ""
		if len(names) > 0 {
			name = strings.TrimLeft(names[0], "/")
		} else {
			name = str(c, "Id", "")
			if len(name) > 12 {
				name = name[:12]
			}
		}
		if !validContainerName(name) {
			continue
		}
		image := str(c, "Image", "")
		ln, li := strings.ToLower(name), strings.ToLower(image)
		// Ignorer frp-manager lui-même
		if strings.Contains(ln, "frp-manager") || strings.Contains(li, "frp-manager") {
			continue
		}
		binType := ""
		for _, bt := range []string{"frps", "frpc"} {
			if strings.Contains(ln, bt) || strings.Contains(li, bt) {
				binType = bt
				break
			}
		}
		if binType == "" {
			continue
		}
		iid := "docker_" + name
		out[iid] = &Instance{ID: iid, Type: binType, Source: "docker", ContainerName: name,
			NetworkMode: str(mapv(c, "HostConfig"), "NetworkMode", ""), Image: image,
			Binary: "/docker/" + name, Service: name}
	}
	return out
}

func buildInstances() map[string]*Instance {
	instances := map[string]*Instance{}
	if standalone {
		// Pas de systemd : une instance par config, lancée par le panel
		// No systemd: one instance per config, run by the panel
		instances = standaloneInstances()
		for iid, inst := range detectDockerContainers() {
			if _, ok := instances[iid]; !ok {
				instances[iid] = inst
			}
		}
		return instances
	}
	allUnits := findSystemdUnits()
	for _, binType := range []string{"frps", "frpc"} {
		bin := findBinary(binType)
		version := ""
		if bin != "" {
			version = readVersion(bin)
		}
		units := allUnits[binType]
		configs := findAllConfigs(binType)
		if len(units) > 0 {
			for _, u := range units {
				iid := u.name
				conf := ""
				if u.config != "" && fileExists(u.config) {
					conf = u.config
				}
				if conf == "" {
					suffix := strings.Trim(strings.TrimPrefix(u.name, binType), "-_")
					for _, c := range configs {
						if suffix != "" && strings.Contains(stem(c), suffix) {
							conf = c
							break
						}
					}
					if conf == "" && len(configs) > 0 {
						conf = configs[0]
					}
				}
				if conf == "" {
					conf = filepath.Join(frpConfDir, iid+".toml")
				}
				b := bin
				if b == "" {
					b = filepath.Join(frpBinDir, binType)
				}
				// Journal : le log.to de la config s'il est donné (une config déposée à
				// la main peut garder celui d'une autre instance)
				// Log: the config's log.to when set (a config dropped by hand may keep
				// another instance's one)
				instances[iid] = &Instance{ID: iid, Type: binType, Source: "systemd", Binary: b, Version: version,
					Config: conf, Service: iid, Log: frpLogFile(conf, iid)}
			}
		} else if bin != "" {
			// Programme sans service systemd : une instance par config existante,
			// sauf celles dont le service a été supprimé depuis le tableau de bord.
			dismissed := map[string]bool{}
			for _, d := range stringList(cfg()["dismissed_configs"]) {
				dismissed[d] = true
			}
			i := 0
			for _, conf := range configs {
				if dismissed[conf] {
					continue
				}
				iid := binType
				if i > 0 {
					iid = fmt.Sprintf("%s%d", binType, i+1)
				}
				i++
				instances[iid] = &Instance{ID: iid, Type: binType, Source: "systemd", Binary: bin, Version: version,
					Config: conf, Service: iid, Log: filepath.Join(frpLogDir, iid+".log")}
			}
		}
	}
	// Ajouter les conteneurs Docker (sans doublon avec les instances systemd)
	for iid, inst := range detectDockerContainers() {
		if _, ok := instances[iid]; !ok {
			instances[iid] = inst
		}
	}
	return instances
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// discover : découverte complète → (instances, description sans état).
func discover() (map[string]*Instance, map[string]J) {
	instances := buildInstances()
	result := map[string]J{}
	for iid, inst := range instances {
		if inst.Source == "docker" {
			result[iid] = J{
				"id": iid, "type": inst.Type, "source": "docker", "container_name": inst.ContainerName,
				"network_mode": inst.NetworkMode, "image": inst.Image,
				"binary_path": "docker:" + inst.ContainerName, "binary_found": true,
				"version": nil, "config_path": nil, "config_exists": false,
				"service": inst.Service, "log_path": nil, "configured": true,
			}
			continue
		}
		configured := fileExists(inst.Config)
		if inst.Type == "frpc" {
			configured = frpcConfigured(inst.Config)
		}
		result[iid] = J{
			"id": iid, "type": inst.Type, "source": inst.Source,
			"binary_path": inst.Binary, "binary_found": isExecutable(inst.Binary),
			"version": nullable(inst.Version), "config_path": nullable(inst.Config),
			"config_exists": inst.Config != "" && fileExists(inst.Config),
			"service":       inst.Service, "log_path": inst.Log, "configured": configured,
		}
	}
	return instances, result
}

// readStatuses : état de toutes les instances (un « systemctl show », une liste Docker).
func readStatuses(found map[string]J) map[string]J {
	var units []string
	hasDocker := false
	for _, i := range found {
		if i["source"] == "systemd" && i["binary_found"] == true {
			units = append(units, i["service"].(string)+".service")
		}
		if i["source"] == "docker" {
			hasDocker = true
		}
	}
	sort.Strings(units)
	shown := systemctlShow(units, []string{"ActiveState", "UnitFileState"})
	containers := map[string]bool{}
	if hasDocker {
		if st, data := dockerAPI("GET", "/containers/json?all=true", nil); st == 200 {
			if list, ok := data.([]any); ok {
				for _, item := range list {
					c, _ := item.(map[string]any)
					running := strings.ToLower(str(c, "State", "")) == "running"
					for _, n := range stringList(c["Names"]) {
						containers[strings.TrimLeft(n, "/")] = running
					}
				}
			}
		}
	}
	statuses := standaloneStatuses(found)
	for iid, inst := range found {
		switch {
		case inst["source"] == "standalone":
			// Déjà lu auprès du superviseur / Already read from the supervisor
		case inst["source"] == "docker":
			running := containers[inst["container_name"].(string)]
			active := "inactive"
			if running {
				active = "active"
			}
			statuses[iid] = J{"active": active, "enabled": false, "running": running}
		case inst["binary_found"] != true:
			statuses[iid] = missingStatus()
		default:
			props := shown[inst["service"].(string)+".service"]
			active := props["ActiveState"]
			statuses[iid] = J{"active": active, "enabled": props["UnitFileState"] == "enabled", "running": active == "active"}
		}
	}
	return statuses
}

// Découverte et lecture de l'état : remplaçables (tests).
var (
	discoverFn     = discover
	readStatusesFn = readStatuses
)

// detectFrp : instances avec leur état (découverte et état mis en cache).
func detectFrp(force bool) map[string]J {
	detectMu.Lock()
	defer detectMu.Unlock()
	now := time.Now()
	if force || detectCacheTime.IsZero() || now.Sub(detectCacheTime) >= discoveryTTL {
		insts, cache := discoverFn()
		instancesMu.Lock()
		instancesMap = insts
		instancesMu.Unlock()
		detectCache = cache
		detectCacheTime = now
		statusTime = time.Time{}
	}
	if now.Sub(statusTime) >= statusTTL {
		statusCache = readStatusesFn(detectCache)
		statusTime = time.Now()
	}
	out := make(map[string]J, len(detectCache))
	for iid, inst := range detectCache {
		e := make(J, len(inst)+1)
		for k, v := range inst {
			e[k] = v
		}
		if st, ok := statusCache[iid]; ok {
			e["status"] = st
		} else {
			e["status"] = stoppedStatus()
		}
		out[iid] = e
	}
	return out
}

// Réveil des WebSocket /ws/status : l'état est renvoyé tout de suite après une action
var (
	statusChangedMu sync.Mutex
	statusChanged   = make(chan struct{})
)

func statusChangedChan() <-chan struct{} {
	statusChangedMu.Lock()
	defer statusChangedMu.Unlock()
	return statusChanged
}

// invalidateCache : état relu au prochain appel ; discovery refait aussi la
// découverte (installation, suppression, configuration modifiée).
func invalidateCache(discovery bool) {
	detectMu.Lock()
	statusTime = time.Time{}
	if discovery {
		detectCacheTime = time.Time{}
	}
	detectMu.Unlock()
	statusChangedMu.Lock()
	close(statusChanged)
	statusChanged = make(chan struct{})
	statusChangedMu.Unlock()
}

// validUTF8 : texte lisible (octets invalides remplacés).
func validUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "�")
}
