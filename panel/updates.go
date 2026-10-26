// Mises à jour : version et mise à jour du panel, frp (installation, envoi
// manuel d'une archive), accès aux sources.

package main

import (
	"archive/zip"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ── Version du panel ────────────────────────────────────────────────────────
// Gravée à la compilation (-ldflags "-X main.buildVersion=0.0.60") ; vide en
// développement.
var buildVersion = ""

var panelVersion = loadPanelVersion()

// loadPanelVersion : PANEL_DOCKER_VERSION (image Docker), puis la version
// gravée dans le binaire, puis state.json (lancement depuis les sources).
func loadPanelVersion() string {
	if v := strings.TrimSpace(os.Getenv("PANEL_DOCKER_VERSION")); v != "" && v != "unknown" {
		return v
	}
	if buildVersion != "" && buildVersion != "unknown" {
		return buildVersion
	}
	if raw, err := os.ReadFile(frpStateFile); err == nil {
		var s map[string]any
		if json.Unmarshal(raw, &s) == nil {
			if v := str(s, "panel_version", ""); v != "" {
				return v
			}
		}
	}
	return "0.0.0"
}

// ── Cache des réponses de GitHub ────────────────────────────────────────────
// Gardées 10 min (1 min en cas d'échec) : l'interface vérifie les mises à jour à
// chaque ouverture de page, et l'API GitHub n'accepte que 60 appels par heure
// sans compte. Le bouton « Vérifier » passe outre (?fresh=1).

type cacheEntry struct {
	at    time.Time
	value []string
	ttl   time.Duration
}

var (
	githubCacheMu sync.Mutex
	githubCache   = map[string]cacheEntry{}
)

func githubCached(key string, fetch func() []string, fresh bool) []string {
	githubCacheMu.Lock()
	hit, ok := githubCache[key]
	githubCacheMu.Unlock()
	if ok && !fresh && time.Since(hit.at) < hit.ttl {
		return hit.value
	}
	value := fetch()
	ttl := 10 * time.Minute
	if len(value) == 0 || value[0] == "" {
		ttl = time.Minute
	}
	githubCacheMu.Lock()
	githubCache[key] = cacheEntry{time.Now(), value, ttl}
	githubCacheMu.Unlock()
	return value
}

// ── Releases du panel ───────────────────────────────────────────────────────

const (
	panelChecksumAsset = "frp-manager.zip.sha256" // somme SHA-256 de frp-manager.zip
	panelZipMaxBytes   = 100 << 20
	panelZipMaxUnpack  = 300 << 20
	panelZipMaxMembers = 2000
)

// fetchPanelRelease : release cible du panel (JSON de l'API), ou nil. Canal
// stable : la release « latest ». Canal pré-release : la version la plus
// récente parmi toutes les releases publiées, pré-releases comprises.
func fetchPanelRelease(l Lang, includePre bool) (map[string]any, error) {
	if !includePre {
		if data, err := ghAPI(l, panelGitHubAPI, 10*time.Second); err == nil {
			obj, _ := data.(map[string]any)
			return obj, nil
		}
	} else if data, err := ghAPI(l, "repos/"+panelGitHubRepo+"/releases?per_page=30", 10*time.Second); err == nil {
		var best map[string]any
		var bestKey *versionKey
		list, _ := data.([]any)
		for _, item := range list {
			rel, _ := item.(map[string]any)
			if truthy(rel["draft"]) {
				continue
			}
			k := parseVersion(str(rel, "tag_name", ""))
			if k != nil && (bestKey == nil || cmpVersionKey(k, bestKey) > 0) {
				best, bestKey = rel, k
			}
		}
		return best, nil
	}
	// API indisponible (quota) : même choix, d'après le site
	latest, err := ghWebLatestTag(l, panelGitHubRepo, 10*time.Second)
	if err != nil {
		return nil, err
	}
	tag := latest
	if includePre {
		tags, _ := ghWebTags(panelGitHubRepo, 10*time.Second)
		for _, t := range tags {
			if parseVersion(t) != nil && versionNewer(t, tag) {
				tag = t
			}
		}
	}
	return panelWebRelease(tag, latest), nil
}

// panelWebRelease : release reconstituée sans l'API (l'archive publiée par la
// CI porte toujours le nom frp-manager.zip ; plus récente que « latest » :
// pré-release).
func panelWebRelease(tag, latest string) map[string]any {
	var assets []any
	for _, name := range []string{"frp-manager.zip", panelChecksumAsset} {
		assets = append(assets, map[string]any{"name": name,
			"browser_download_url": "https://github.com/" + panelGitHubRepo + "/releases/download/" + tag + "/" + name})
	}
	return map[string]any{"tag_name": tag, "html_url": "https://github.com/" + panelGitHubRepo + "/releases/tag/" + tag,
		"prerelease": versionNewer(tag, latest), "assets": assets}
}

// fetchPanelLatest → [version, adresse de la release] (vides si inconnues).
func fetchPanelLatest(l Lang, includePre bool) []string {
	data, err := fetchPanelRelease(l, includePre)
	if err != nil || data == nil {
		return []string{"", ""}
	}
	url := str(data, "html_url", "https://github.com/"+panelGitHubRepo+"/releases")
	return []string{strings.TrimPrefix(str(data, "tag_name", ""), "v"), url}
}

var (
	releaseStatusMu sync.Mutex
	releaseStatus   = map[string]struct {
		at     time.Time
		status *bool
	}{}
)

func boolPtr(b bool) *bool { return &b }

// githubReleaseIsPrerelease : statut de la release v<version> (nil si inconnu).
// Mis en cache 10 minutes, sauf fresh : une promotion récente est vue tout de suite.
func githubReleaseIsPrerelease(l Lang, version string, fresh bool) *bool {
	releaseStatusMu.Lock()
	cached, ok := releaseStatus[version]
	releaseStatusMu.Unlock()
	if ok && !fresh && time.Since(cached.at) < 10*time.Minute {
		return cached.status
	}
	var status *bool
	data, err := ghAPI(l, "repos/"+panelGitHubRepo+"/releases/tags/v"+version, 8*time.Second)
	switch {
	case err == nil:
		obj, _ := data.(map[string]any)
		status = boolPtr(truthy(obj["prerelease"]))
	case err.Error() == "HTTP 404":
		status = nil // pas de release pour cette version
	default:
		// Statut déduit du site : ce qui suit la release « latest » est en pré-release
		if latest, err := ghWebLatestTag(l, panelGitHubRepo, 8*time.Second); err == nil {
			status = boolPtr(versionNewer(version, latest))
		}
	}
	releaseStatusMu.Lock()
	releaseStatus[version] = struct {
		at     time.Time
		status *bool
	}{time.Now(), status}
	releaseStatusMu.Unlock()
	return status
}

// panelIsPrerelease : pré-release si le numéro l'indique ou si la release GitHub
// de cette version est marquée pré-release.
func panelIsPrerelease(l Lang, fresh bool) bool {
	if isPrereleaseVersion(panelVersion) {
		return true
	}
	s := githubReleaseIsPrerelease(l, panelVersion, fresh)
	return s != nil && *s
}

// ── Journaux des tâches de fond ─────────────────────────────────────────────

type taskLog struct {
	mu      sync.Mutex
	lines   []string
	running bool // tâche en cours (reset → finish)
	success bool // issue de la dernière tâche terminée
}

func (t *taskLog) reset() {
	t.mu.Lock()
	t.lines, t.running, t.success = nil, true, false
	t.mu.Unlock()
}

func (t *taskLog) finish(ok bool) { t.mu.Lock(); t.running, t.success = false, ok; t.mu.Unlock() }

func (t *taskLog) state() (running, success bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.running, t.success
}

func (t *taskLog) add(msg string) {
	t.mu.Lock()
	t.lines = append(t.lines, "["+time.Now().Format("15:04:05")+"] "+msg)
	t.mu.Unlock()
}

func (t *taskLog) snapshot() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := append([]string{}, t.lines...)
	return out
}

var (
	updateLog      taskLog
	updateMu       sync.Mutex
	panelUpdateLog taskLog
	panelUpdateMu  sync.Mutex
)

// ── Mise à jour du panel ────────────────────────────────────────────────────
// L'archive frp-manager.zip contient le binaire de chaque architecture
// (bin/frp-manager-linux-<arch>) et install.sh : le binaire de cette machine
// remplace celui qui tourne, puis le service redémarre.

var panelBinaryName = "frp-manager-linux-" + map[string]string{"amd64": "amd64", "arm64": "arm64", "arm": "armv7"}[getArch()]

// installedByApt : panel installé par le paquet .deb (apt le met à jour, pas le panel).
func installedByApt() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	exe, _ = filepath.EvalSymlinks(exe)
	return exe == "/usr/bin/frp-manager" && fileExists("/var/lib/dpkg/info/frp-manager.list")
}

// checkPanelZip : erreur si l'archive est suspecte (chemins absolus ou avec
// « .. », liens symboliques, trop d'entrées ou trop grosse décompressée).
func checkPanelZip(l Lang, zr *zip.Reader) error {
	if len(zr.File) > panelZipMaxMembers {
		return errors.New(l.M("archive refusée : trop d'entrées", "archive rejected: too many entries"))
	}
	var total uint64
	driveRe := regexp.MustCompile(`^[A-Za-z]:`)
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		dotdot := false
		for _, p := range strings.Split(name, "/") {
			if p == ".." {
				dotdot = true
			}
		}
		if strings.HasPrefix(name, "/") || driveRe.MatchString(name) || dotdot {
			return errors.New(l.M("archive refusée : chemin suspect « "+f.Name+" »", "archive rejected: suspicious path “"+f.Name+"”"))
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return errors.New(l.M("archive refusée : lien symbolique « "+f.Name+" »", "archive rejected: symbolic link “"+f.Name+"”"))
		}
		total += f.UncompressedSize64
		if total > panelZipMaxUnpack {
			return errors.New(l.M("archive refusée : trop volumineuse une fois décompressée", "archive rejected: too large once decompressed"))
		}
	}
	return nil
}

// extractZipFile écrit l'entrée name (chemins normalisés en /) dans dst.
func extractZipFile(zr *zip.Reader, suffix, dst string, mode os.FileMode) (bool, error) {
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if name != suffix && !strings.HasSuffix(name, "/"+suffix) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return true, err
		}
		tmp := dst + ".frpm-new"
		w, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
		if err != nil {
			rc.Close()
			return true, err
		}
		_, err = io.Copy(w, io.LimitReader(rc, panelZipMaxUnpack))
		rc.Close()
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(tmp)
			return true, err
		}
		return true, os.Rename(tmp, dst)
	}
	return false, nil
}

func runPanelUpdate(l Lang, prerelease bool) {
	logf := panelUpdateLog.add
	logf(l.M("[INFO] Récupération des infos de release…", "[INFO] Fetching release information…"))
	data, err := fetchPanelRelease(l, prerelease)
	if err != nil {
		logf(l.M("[ERROR] GitHub inaccessible : "+err.Error(), "[ERROR] GitHub unreachable: "+err.Error()))
		return
	}
	if data == nil {
		logf(l.M("[ERROR] Aucune release trouvée.", "[ERROR] No release found."))
		return
	}
	tag := str(data, "tag_name", "")
	if !versionNewer(tag, panelVersion) {
		logf(l.M(fmt.Sprintf("[ERROR] %s n'est pas plus récente que v%s : rien à faire.", tag, panelVersion),
			fmt.Sprintf("[ERROR] %s is not newer than v%s: nothing to do.", tag, panelVersion)))
		return
	}
	zipURL, sumURL := "", ""
	for _, item := range listv(data, "assets") {
		a, _ := item.(map[string]any)
		name := str(a, "name", "")
		if strings.HasSuffix(name, ".zip") && strings.Contains(name, "frp-manager") && zipURL == "" {
			zipURL = str(a, "browser_download_url", "")
		} else if name == panelChecksumAsset {
			sumURL = str(a, "browser_download_url", "")
		}
	}
	if zipURL == "" {
		logf(l.M("[ERROR] Aucun asset .zip trouvé dans la release.", "[ERROR] No .zip asset found in the release."))
		return
	}
	// Le binaire remplace celui qui tourne : jamais sans somme de contrôle
	expected := ""
	if sumURL != "" {
		if resp, err := httpGet(sumURL, 30*time.Second, nil); err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if m := regexp.MustCompile(`^\s*([0-9a-fA-F]{64})\b`).FindStringSubmatch(string(body)); resp.StatusCode < 400 && m != nil {
				expected = strings.ToLower(m[1])
			}
		}
	}
	if expected == "" {
		logf(l.M("[ERROR] Pas de somme SHA-256 publiée pour cette release : mise à jour annulée",
			"[ERROR] No SHA-256 published for this release: update cancelled"))
		return
	}
	logf(l.M("[INFO] Téléchargement de "+tag+"…", "[INFO] Downloading "+tag+"…"))
	tmp, digest, err := downloadToTemp(l, zipURL, ".zip", panelZipMaxBytes)
	if err != nil {
		logf(l.M("[ERROR] Téléchargement échoué : "+err.Error(), "[ERROR] Download failed: "+err.Error()))
		return
	}
	defer os.Remove(tmp)
	if subtle.ConstantTimeCompare([]byte(digest), []byte(expected)) != 1 {
		logf(l.M("[ERROR] Somme SHA-256 différente de celle publiée : mise à jour annulée",
			"[ERROR] SHA-256 differs from the published one: update cancelled"))
		return
	}
	logf(l.M("[OK] Somme SHA-256 vérifiée", "[OK] SHA-256 verified"))
	logf(l.M("[INFO] Extraction…", "[INFO] Extracting…"))
	zr, err := zip.OpenReader(tmp)
	if err != nil {
		logf(l.M("[ERROR] Extraction : "+err.Error(), "[ERROR] Extraction: "+err.Error()))
		return
	}
	defer zr.Close()
	if err := checkPanelZip(l, &zr.Reader); err != nil {
		logf(l.M("[ERROR] Extraction : "+err.Error(), "[ERROR] Extraction: "+err.Error()))
		return
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		logf("[ERROR] " + err.Error())
		return
	}
	found, err := extractZipFile(&zr.Reader, "bin/"+panelBinaryName, exe, 0o755)
	if !found {
		logf(l.M("[ERROR] Absent du zip : bin/"+panelBinaryName, "[ERROR] Missing from the zip: bin/"+panelBinaryName))
		return
	}
	if err != nil {
		logf(l.M("[ERROR] Extraction : "+err.Error(), "[ERROR] Extraction: "+err.Error()))
		return
	}
	logf(l.M("[INFO] Mis à jour : "+exe, "[INFO] Updated: "+exe))
	installSh := filepath.Join(filepath.Dir(exe), "install.sh")
	if ok, err := extractZipFile(&zr.Reader, "install.sh", installSh, 0o755); ok && err == nil {
		logf(l.M("[INFO] Mis à jour : install.sh", "[INFO] Updated: install.sh"))
	}
	st := loadState()
	st["panel_version"] = strings.TrimPrefix(tag, "v")
	if err := saveState(st); err != nil {
		logf(l.M("[WARN] Impossible de sauvegarder la version : "+err.Error(), "[WARN] Could not save the version: "+err.Error()))
	}
	logf(l.M("[OK] Panel "+tag+" installé. Redémarrage dans 2s…", "[OK] Panel "+tag+" installed. Restarting in 2 s…"))
	go func() {
		time.Sleep(2 * time.Second)
		logf(l.M("[INFO] Redémarrage de frp-manager…", "[INFO] Restarting frp-manager…"))
		runCmd([]string{"systemctl", "--no-block", "restart", "frp-manager"})
	}()
}

func init() {
	// Version actuelle du panel et mise à jour disponible. Canal stable : releases
	// uniquement. Pré-release en installation classique : pré-releases et releases
	// plus récentes. Pré-release sous Docker : rien (la mise à jour passe par l'image).
	route("GET /api/panel/version", "api_panel_version", perm(P("updates.view"), func(c *Ctx) {
		fresh := c.Query("fresh") == "1"
		pre := panelIsPrerelease(c.Lang, fresh)
		res := githubCached(fmt.Sprintf("panel:%v", pre), func() []string { return fetchPanelLatest(c.Lang, pre) }, fresh)
		latest, url := res[0], res[1]
		c.JSON(J{
			"ok": true, "current": panelVersion, "latest": nullable(latest), "release_url": nullable(url),
			"update_available": latest != "" && !(pre && inDocker) && versionNewer(latest, panelVersion),
			"prerelease":       pre, "repo": panelGitHubRepo, "repo_configured": true, "in_docker": inDocker,
		})
	}))

	route("POST /api/panel/update", "api_panel_update", perm(P("updates.panel"), func(c *Ctx) {
		pre := panelIsPrerelease(c.Lang, false)
		if inDocker {
			msg := c.M("Sous Docker, le panel se met à jour avec l'image du conteneur (docker pull).",
				"Under Docker, the panel is updated with the container image (docker pull).")
			if pre {
				msg = c.M("Pré-release sous Docker : mettez à jour l'image du conteneur.", "Pre-release under Docker: update the container image.")
			}
			c.JSON(J{"ok": false, "msg": msg})
			return
		}
		if installedByApt() {
			c.JSON(J{"ok": false, "msg": c.M("Panel installé par apt : mettez-le à jour avec « apt update && apt upgrade ».",
				"Panel installed with apt: update it with “apt update && apt upgrade”.")})
			return
		}
		if !panelUpdateMu.TryLock() {
			c.JSON(J{"ok": false, "msg": c.M("Mise à jour du panel déjà en cours.", "A panel update is already running.")})
			return
		}
		panelUpdateLog.reset()
		l := c.Lang
		go func() {
			defer panelUpdateMu.Unlock()
			runPanelUpdate(l, pre)
		}()
		c.JSON(J{"ok": true})
	}))

	route("GET /api/panel/update/log", "api_panel_update_log", perm(P("updates.view"), func(c *Ctx) {
		c.JSON(J{"ok": true, "lines": panelUpdateLog.snapshot()})
	}))

	route("GET /api/connectivity", "api_connectivity", perm(P("updates.view"), func(c *Ctx) {
		results := J{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, s := range versionSources {
			wg.Add(1)
			go func(name, url string) {
				defer wg.Done()
				tag, err := fetchVersionSource(c.Lang, url)
				var r J
				if err != nil {
					r = J{"ok": false, "error": short(err.Error(), 120)}
				} else {
					if tag == "" {
						tag = "?"
					}
					r = J{"ok": true, "version": tag}
				}
				mu.Lock()
				results[name] = r
				mu.Unlock()
			}(s[0], s[1])
		}
		wg.Wait()
		c.JSON(J{"ok": true, "sources": results})
	}))

	route("GET /api/update/check", "api_update_check", perm(P("updates.view"), func(c *Ctx) {
		res := githubCached("frp", func() []string {
			v, t, s := fetchLatestVersion(c.Lang)
			return []string{v, t, s}
		}, c.Query("fresh") == "1")
		version, tag, source := res[0], res[1], res[2]
		if version == "" {
			c.JSON(J{"ok": false, "msg": c.M("Toutes les sources inaccessibles.", "All sources unreachable.")})
			return
		}
		st := loadState()
		installed := st["installed_version"]
		if standalone {
			// Programmes de l'image ou du dossier persistant, sans state.json
			// Programs of the image or of the persistent folder, without state.json
			installed = frpInstalledVersion()
		}
		st["last_update_check"] = time.Now().Format("2006-01-02T15:04:05.000000")
		saveState(st)
		available := true
		if truthy(installed) {
			available = pyStr(installed) != version
		}
		c.JSON(J{"ok": true, "latest": version, "tag": tag, "installed": installed, "source": source, "update_available": available})
	}))

	route("POST /api/update/install", "api_update_install", perm(P("updates.install"), func(c *Ctx) {
		if !updateMu.TryLock() {
			c.JSON(J{"ok": false, "msg": c.M("Mise à jour déjà en cours", "An update is already running")})
			return
		}
		updateLog.reset()
		l := c.Lang
		go func() {
			defer updateMu.Unlock()
			version, tag, source := fetchLatestVersion(l)
			if version == "" {
				updateLog.add(l.M("[ERROR] Toutes les sources inaccessibles. Utilisez l'upload manuel.", "[ERROR] All sources unreachable. Use the manual upload."))
				updateLog.finish(false)
				return
			}
			updateLog.add(fmt.Sprintf("[INFO] Version: %s (%s)", tag, source))
			updateLog.finish(downloadAndInstall(l, version, tag, nil))
		}()
		c.JSON(J{"ok": true})
	}))

	route("POST /api/update/upload", "api_update_upload", perm(P("updates.install"), func(c *Ctx) {
		if !updateMu.TryLock() {
			c.JSON(J{"ok": false, "msg": c.M("Mise à jour déjà en cours", "An update is already running")})
			return
		}
		updateLog.reset()
		file, _, err := c.R.FormFile("file")
		if err != nil {
			updateMu.Unlock()
			c.JSON(J{"ok": false, "msg": c.M("Aucun fichier reçu", "No file received")})
			return
		}
		defer file.Close()
		version := strings.TrimPrefix(strings.TrimSpace(c.R.FormValue("version")), "v")
		if !regexp.MustCompile(`^[0-9A-Za-z._-]{1,32}$`).MatchString(version) {
			version = "manual"
		}
		tmp, err := os.CreateTemp("", "frpm-*.tar.gz")
		if err == nil {
			_, err = io.Copy(tmp, file)
			tmp.Close()
		}
		if err != nil {
			updateMu.Unlock()
			if tmp != nil {
				os.Remove(tmp.Name())
			}
			c.JSON(J{"ok": false, "msg": err.Error()})
			return
		}
		l := c.Lang
		go func() {
			defer updateMu.Unlock()
			defer os.Remove(tmp.Name())
			updateLog.finish(installFromArchive(l, tmp.Name(), version, nil, updateLog.add))
		}()
		c.JSON(J{"ok": true})
	}))

	route("GET /api/update/log", "api_update_log", perm(P("updates.view"), func(c *Ctx) {
		running, success := updateLog.state()
		c.JSON(J{"ok": true, "lines": updateLog.snapshot(), "running": running, "success": success})
	}))
}

// downloadAndInstall : archive de la release tag téléchargée, vérifiée puis
// installée (programmes only, tous si nil), journal dans updateLog.
func downloadAndInstall(l Lang, version, tag string, only []string) bool {
	tmp, _ := downloadArchive(l, version, tag, updateLog.add)
	if tmp == "" {
		updateLog.add(l.M("[ERROR] Tous les miroirs ont échoué. Utilisez l'upload manuel.", "[ERROR] All mirrors failed. Use the manual upload."))
		return false
	}
	defer os.Remove(tmp)
	return installFromArchive(l, tmp, version, only, updateLog.add)
}

// reinstallVersion : (version, tag) de la version installée, vides si elle est
// inconnue ou ne vient pas d'une release (archive envoyée à la main).
func reinstallVersion(installed string) (string, string) {
	tag := "v" + strings.TrimPrefix(strings.TrimSpace(installed), "v")
	if !frpTagRe.MatchString(tag) {
		return "", ""
	}
	return strings.TrimPrefix(tag, "v"), tag
}
