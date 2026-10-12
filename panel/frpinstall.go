// GitHub (API et site en repli), versions, téléchargement vérifié et
// installation des binaires frp.

package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	panelGitHubRepo = "Gogowwww/frp-manager"
	panelGitHubAPI  = "https://api.github.com/repos/" + panelGitHubRepo + "/releases/latest"

	frpReleaseDownload = "https://github.com/fatedier/frp/releases/download/{tag}/{filename}"
	frpChecksumsFile   = "frp_sha256_checksums.txt" // joint à chaque release de fatedier/frp
	downloadMaxBytes   = 100 << 20                  // archive frp : ~15 Mo
	archiveMaxMembers  = 1000
)

// Taille décompressée maximale d'une archive frp (bombe de décompression)
var archiveMaxUnpacked int64 = 300 << 20

var (
	versionSources = [][2]string{
		{"github", "https://api.github.com/repos/fatedier/frp/releases/latest"},
		// Le site lui-même (redirection de /releases/latest) : pas soumis au quota de l'API
		{"github.com", "https://github.com/fatedier/frp/releases/latest"},
	}
	// Miroirs TIERS, essayés seulement si github.com échoue et si download_mirrors
	// est vrai (Réglages) : ils voient passer et pourraient modifier le fichier, d'où
	// la vérification par la somme SHA-256 publiée avec chaque release de frp.
	thirdPartyMirrors = []string{
		"https://mirror.ghproxy.com/" + frpReleaseDownload,
		"https://ghfast.top/" + frpReleaseDownload,
		"https://gh-proxy.com/" + frpReleaseDownload,
	}
	frpBinaries = []string{"frps", "frpc"}
)

func buildDownloadMirrors(tag, filename string) []string {
	tpls := []string{frpReleaseDownload}
	if mirrorsAllowed() {
		tpls = append(tpls, thirdPartyMirrors...)
	}
	out := make([]string, len(tpls))
	for i, t := range tpls {
		out[i] = strings.NewReplacer("{tag}", tag, "{filename}", filename).Replace(t)
	}
	return out
}

func isThirdParty(url string) bool { return !strings.HasPrefix(url, "https://github.com/") }

func urlHost(url string) string {
	parts := strings.SplitN(url, "/", 4)
	if len(parts) >= 3 {
		return parts[2]
	}
	return url
}

// ── GitHub : l'API, et le site en repli ─────────────────────────────────────
// Sans compte, l'API n'accepte que 60 appels par heure et par adresse IP. Une
// fois ce quota épuisé, on ne la sollicite plus avant sa remise à zéro et on lit
// les mêmes informations sur le site : redirection de /releases/latest et flux
// releases.atom.

var (
	ghMu           sync.Mutex
	ghBlockedUntil float64
)

func httpGet(url string, timeout time.Duration, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "frp-manager")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return (&http.Client{Timeout: timeout}).Do(req)
}

// ghAPI : GET sur l'API GitHub (chemin ou URL complète) → JSON.
func ghAPI(l Lang, path string, timeout time.Duration) (any, error) {
	ghMu.Lock()
	blocked := nowFunc() < ghBlockedUntil
	ghMu.Unlock()
	if blocked {
		return nil, errors.New(l.M("quota de l'API GitHub épuisé, repli sur github.com", "GitHub API quota exhausted, falling back to github.com"))
	}
	url := path
	if !strings.HasPrefix(path, "http") {
		url = "https://api.github.com/" + path
	}
	resp, err := httpGet(url, timeout, map[string]string{"Accept": "application/vnd.github.v3+json"})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if (resp.StatusCode == 403 || resp.StatusCode == 429) && resp.Header.Get("X-RateLimit-Remaining") == "0" {
		ghMu.Lock()
		if reset, err := strconv.ParseFloat(resp.Header.Get("X-RateLimit-Reset"), 64); err == nil {
			ghBlockedUntil = reset
		} else {
			ghBlockedUntil = nowFunc() + 3600
		}
		ghMu.Unlock()
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var data any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&data); err != nil {
		return nil, err
	}
	return data, nil
}

var latestTagRe = regexp.MustCompile(`/releases/tag/([^/?#]+)$`)

// ghWebLatestTag : tag de la release « latest » d'un dépôt, lu sur le site.
func ghWebLatestTag(l Lang, repo string, timeout time.Duration) (string, error) {
	req, _ := http.NewRequest(http.MethodHead, "https://github.com/"+repo+"/releases/latest", nil)
	req.Header.Set("User-Agent", "frp-manager")
	client := &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if m := latestTagRe.FindStringSubmatch(resp.Header.Get("Location")); m != nil {
		return m[1], nil
	}
	return "", errors.New(l.M(fmt.Sprintf("github.com : pas de release « latest » pour %s (HTTP %d)", repo, resp.StatusCode),
		fmt.Sprintf("github.com: no \"latest\" release for %s (HTTP %d)", repo, resp.StatusCode)))
}

var atomTagRe = regexp.MustCompile(`/releases/tag/([^"'<>\s]+)`)

// ghWebTags : tags des releases publiées (pré-releases comprises), du flux releases.atom.
func ghWebTags(repo string, timeout time.Duration) ([]string, error) {
	resp, err := httpGet("https://github.com/"+repo+"/releases.atom", timeout, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var tags []string
	seen := map[string]bool{}
	for _, m := range atomTagRe.FindAllStringSubmatch(string(body), -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			tags = append(tags, m[1])
		}
	}
	return tags, nil
}

var (
	webLatestRe = regexp.MustCompile(`^https://github\.com/([^/]+/[^/]+)/releases/latest$`)
	frpTagRe    = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
)

func fetchVersionSource(l Lang, url string) (string, error) {
	if m := webLatestRe.FindStringSubmatch(url); m != nil {
		return ghWebLatestTag(l, m[1], 12*time.Second)
	}
	data, err := ghAPI(l, url, 12*time.Second)
	if err != nil {
		return "", err
	}
	obj, _ := data.(map[string]any)
	if t := str(obj, "tag_name", ""); t != "" {
		return t, nil
	}
	return str(obj, "tag", ""), nil
}

// fetchLatestVersion → (version, tag, source) ; version vide si introuvable.
func fetchLatestVersion(l Lang) (string, string, string) {
	for _, s := range versionSources {
		tag, err := fetchVersionSource(l, s[1])
		// Le tag finit dans des adresses de téléchargement : format strict
		if err == nil && frpTagRe.MatchString(tag) {
			return strings.TrimPrefix(tag, "v"), tag, s[0]
		}
	}
	return "", "", l.M("toutes les sources inaccessibles", "all sources unreachable")
}

// ── Versions (semver simplifié) ─────────────────────────────────────────────

var versionRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.]+))?$`)

type versionKey struct {
	core    [3]int
	release bool // une pré-release passe avant la release du même numéro
	pre     []string
}

func parseVersion(v string) *versionKey {
	m := versionRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return nil
	}
	k := &versionKey{}
	for i := 0; i < 3; i++ {
		k.core[i], _ = strconv.Atoi(m[i+1])
	}
	if m[4] == "" {
		k.release = true
	} else {
		k.pre = strings.Split(m[4], ".")
	}
	return k
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// cmpVersionKey : <0, 0, >0 (identifiants numériques avant les textuels, comme semver).
func cmpVersionKey(a, b *versionKey) int {
	for i := 0; i < 3; i++ {
		if a.core[i] != b.core[i] {
			if a.core[i] < b.core[i] {
				return -1
			}
			return 1
		}
	}
	if a.release != b.release {
		if a.release {
			return 1
		}
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		pa, pb := a.pre[i], b.pre[i]
		da, db := isDigits(pa), isDigits(pb)
		switch {
		case da && db:
			na, _ := strconv.Atoi(pa)
			nb, _ := strconv.Atoi(pb)
			if na != nb {
				if na < nb {
					return -1
				}
				return 1
			}
		case da != db:
			if da {
				return -1
			}
			return 1
		default:
			if c := strings.Compare(pa, pb); c != 0 {
				return c
			}
		}
	}
	return len(a.pre) - len(b.pre)
}

// isPrereleaseVersion : tout ce qui n'est pas une release X.Y.Z.
func isPrereleaseVersion(v string) bool {
	k := parseVersion(v)
	return k == nil || !k.release
}

func versionNewer(candidate, current string) bool {
	a, b := parseVersion(candidate), parseVersion(current)
	return a != nil && b != nil && cmpVersionKey(a, b) > 0
}

// ── Téléchargement vérifié ──────────────────────────────────────────────────

var checksumLineRe = regexp.MustCompile(`^\s*([0-9a-fA-F]{64})\s+\*?(\S+)\s*$`)

// parseChecksums : fichier au format sha256sum → {nom: empreinte}.
func parseChecksums(text string) map[string]string {
	sums := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		if m := checksumLineRe.FindStringSubmatch(strings.TrimRight(line, "\r")); m != nil {
			sums[m[2]] = strings.ToLower(m[1])
		}
	}
	return sums
}

// fetchFrpChecksums : sommes SHA-256 de la release → (sommes, viennent de
// github.com). Un miroir tiers seulement s'ils sont autorisés (vérification
// alors affaiblie : le même intermédiaire pourrait fournir archive et somme).
func fetchFrpChecksums(l Lang, tag string, logf func(string)) (map[string]string, bool) {
	for _, url := range buildDownloadMirrors(tag, frpChecksumsFile) {
		resp, err := httpGet(url, 30*time.Second, nil)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1_000_000))
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			continue
		}
		if sums := parseChecksums(string(body)); len(sums) > 0 {
			if isThirdParty(url) {
				h := urlHost(url)
				logf(l.M("[WARN] Sommes de contrôle obtenues via le miroir tiers "+h+" : vérification moins fiable",
					"[WARN] Checksums obtained through the third-party mirror "+h+": weaker verification"))
			}
			return sums, !isThirdParty(url)
		}
	}
	return map[string]string{}, false
}

// downloadToTemp : fichier temporaire → (chemin, sha256). Taille plafonnée.
func downloadToTemp(l Lang, url, suffix string, maxBytes int64) (string, string, error) {
	resp, err := httpGet(url, 120*time.Second, nil)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp("", "frpm-*"+suffix)
	if err != nil {
		return "", "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, maxBytes+1))
	tmp.Close()
	if err == nil && n > maxBytes {
		err = errors.New(l.M(fmt.Sprintf("fichier plus gros que %d Mo", maxBytes>>20), fmt.Sprintf("file larger than %d MB", maxBytes>>20)))
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", "", err
	}
	return tmp.Name(), hex.EncodeToString(h.Sum(nil)), nil
}

// downloadArchive : archive frp téléchargée ET vérifiée (SHA-256) → (chemin, nom).
func downloadArchive(l Lang, version, tag string, logf func(string)) (string, string) {
	filename := fmt.Sprintf("frp_%s_linux_%s.tar.gz", version, getArch())
	sums, trusted := fetchFrpChecksums(l, tag, logf)
	expected := sums[filename]
	if expected == "" {
		logf(l.M("[WARN] Pas de somme de contrôle publiée pour "+filename+" : intégrité non vérifiable",
			"[WARN] No published checksum for "+filename+": integrity cannot be verified"))
	}
	if !mirrorsAllowed() {
		logf(l.M("[INFO] Miroirs tiers désactivés : github.com uniquement", "[INFO] Third-party mirrors disabled: github.com only"))
	}
	for _, url := range buildDownloadMirrors(tag, filename) {
		source, third := urlHost(url), isThirdParty(url)
		if third && expected == "" {
			logf(l.M("[WARN] "+source+" ignoré : un miroir tiers n'est utilisé qu'avec une somme de contrôle",
				"[WARN] "+source+" skipped: a third-party mirror is only used with a checksum"))
			continue
		}
		msg := l.M("[INFO] Tentative : "+source+" …", "[INFO] Trying "+source+"…")
		if third {
			msg += l.M(" (miroir tiers)", " (third-party mirror)")
		}
		logf(msg)
		path, digest, err := downloadToTemp(l, url, ".tar.gz", downloadMaxBytes)
		if err != nil {
			logf("[WARN] " + source + " : " + err.Error())
			continue
		}
		if expected != "" && subtle.ConstantTimeCompare([]byte(digest), []byte(expected)) != 1 {
			os.Remove(path)
			logf(l.M("[ERROR] "+source+" : somme SHA-256 différente de celle publiée, fichier rejeté",
				"[ERROR] "+source+": SHA-256 differs from the published one, file rejected"))
			continue
		}
		if expected != "" {
			msg := l.M("[OK] Téléchargé depuis "+source+", SHA-256 vérifiée", "[OK] Downloaded from "+source+", SHA-256 verified")
			if !trusted {
				msg += l.M(" (somme obtenue via un miroir tiers)", " (checksum obtained through a third-party mirror)")
			}
			logf(msg)
		} else {
			logf(l.M("[OK] Téléchargé depuis "+source+" (non vérifié)", "[OK] Downloaded from "+source+" (not verified)"))
		}
		return path, filename
	}
	return "", filename
}

func stopRunningFrpServices() []string {
	_, out, _ := runCmd([]string{"systemctl", "list-units", "--type=service", "--state=active", "--no-pager", "--plain", "--no-legend"})
	var running []string
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}
		unit := strings.Trim(parts[0], "●▶ ")
		if !strings.HasSuffix(unit, ".service") {
			continue
		}
		_, prop, _ := runCmd([]string{"systemctl", "show", unit, "--property=ExecStart", "--value"})
		if strings.Contains(prop, "/frps") || strings.Contains(prop, "/frpc") {
			running = append(running, strings.TrimSuffix(unit, ".service"))
		}
	}
	for _, svc := range running {
		runCmd([]string{"systemctl", "stop", svc})
	}
	return running
}

// posixParts : composants d'un chemin d'archive (sans « . » ni vides).
func posixParts(name string) []string {
	var parts []string
	for _, p := range strings.Split(name, "/") {
		if p != "" && p != "." {
			parts = append(parts, p)
		}
	}
	return parts
}

// extractFrpBinaries extrait frps/frpc d'une archive .tar.gz dans dest : chemins
// absolus ou avec « .. », liens et fichiers spéciaux refusés, taille
// décompressée plafonnée (bombe de décompression). → {nom: chemin extrait}.
func extractFrpBinaries(l Lang, archive, dest string) (map[string]string, error) {
	f, err := os.Open(archive)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	found := map[string]string{}
	var total int64
	count := 0
	isBin := func(b string) bool { return b == "frps" || b == "frpc" }
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		count++
		if count > archiveMaxMembers {
			return nil, errors.New(l.M("archive refusée : trop d'entrées", "archive rejected: too many entries"))
		}
		parts := posixParts(hdr.Name)
		hasDotDot := false
		for _, p := range parts {
			if p == ".." {
				hasDotDot = true
			}
		}
		if strings.HasPrefix(hdr.Name, "/") || strings.HasPrefix(hdr.Name, "\\") || hasDotDot || strings.Contains(hdr.Name, "\\") {
			return nil, errors.New(l.M("archive refusée : chemin suspect « "+hdr.Name+" »", "archive rejected: suspicious path “"+hdr.Name+"”"))
		}
		base := ""
		if len(parts) > 0 {
			base = parts[len(parts)-1]
		}
		if hdr.Typeflag == tar.TypeSymlink || hdr.Typeflag == tar.TypeLink {
			if isBin(base) {
				return nil, errors.New(l.M("archive refusée : "+hdr.Name+" est un lien", "archive rejected: "+hdr.Name+" is a link"))
			}
			continue
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA && hdr.Typeflag != tar.TypeCont {
			continue // dossiers, périphériques, FIFO : ignorés
		}
		total += hdr.Size
		if total > archiveMaxUnpacked {
			return nil, errors.New(l.M("archive refusée : trop volumineuse une fois décompressée", "archive rejected: too large once decompressed"))
		}
		if !isBin(base) || len(parts) > 2 || found[base] != "" {
			continue
		}
		out := filepath.Join(dest, base)
		w, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			return nil, err
		}
		n, err := io.Copy(w, io.LimitReader(tr, hdr.Size+1))
		w.Close()
		if err != nil {
			return nil, err
		}
		if n > hdr.Size {
			return nil, errors.New(l.M("archive refusée : taille incohérente", "archive rejected: inconsistent size"))
		}
		head := make([]byte, 4)
		if r, err := os.Open(out); err == nil {
			io.ReadFull(r, head)
			r.Close()
		}
		if string(head) != "\x7fELF" {
			return nil, errors.New(l.M("archive refusée : "+hdr.Name+" n'est pas un exécutable Linux",
				"archive rejected: "+hdr.Name+" is not a Linux executable"))
		}
		found[base] = out
	}
	return found, nil
}

// installFile copie src vers dst par un fichier voisin renommé : jamais de
// binaire à moitié écrit, et pas d'erreur « text file busy ».
func installFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".frpm-new"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	os.Chmod(tmp, mode)
	return os.Rename(tmp, dst)
}

func installFromArchive(l Lang, archive, version string, logf func(string)) bool {
	logf(l.M("[INFO] Arrêt des services frp …", "[INFO] Stopping frp services…"))
	running := stopRunningFrpServices()
	if len(running) > 0 {
		list := strings.Join(running, ", ")
		logf(l.M("[INFO] Stoppés : "+list, "[INFO] Stopped: "+list))
	}
	defer func() {
		if len(running) > 0 {
			list := strings.Join(running, ", ")
			logf(l.M("[INFO] Redémarrage : "+list+" …", "[INFO] Restarting: "+list+"…"))
			for _, svc := range running {
				ok, _, e := runCmd([]string{"systemctl", "start", svc})
				if ok {
					logf("[OK] " + svc)
				} else {
					logf("[WARN] " + svc + " : " + e)
				}
			}
		}
	}()
	logf(l.M("[INFO] Extraction …", "[INFO] Extracting…"))
	tmpdir, err := os.MkdirTemp("", "frpm-extract-")
	if err != nil {
		logf("[ERROR] " + err.Error())
		return false
	}
	defer os.RemoveAll(tmpdir)
	extracted, err := extractFrpBinaries(l, archive, tmpdir)
	if err != nil {
		logf("[ERROR] " + err.Error())
		return false
	}
	var installed []string
	for _, b := range frpBinaries {
		src := extracted[b]
		if src == "" {
			continue
		}
		dst := filepath.Join(frpBinDir, b)
		if err := installFile(src, dst, 0o755); err != nil {
			logf("[ERROR] " + err.Error())
			return false
		}
		logf("[INFO] " + b + " → " + dst)
		installed = append(installed, b)
	}
	if len(installed) == 0 {
		logf(l.M("[ERROR] Aucun binaire trouvé dans l'archive.", "[ERROR] No binary found in the archive."))
		return false
	}
	os.MkdirAll(frpConfDir, 0o755)
	os.MkdirAll(frpLogDir, 0o755)
	// On ne crée PAS de configs par défaut : l'utilisateur les gère lui-même
	st := loadState()
	st["installed_version"] = version
	st["last_update_check"] = time.Now().Format("2006-01-02T15:04:05.000000")
	st["last_update_result"] = "Installed " + version
	if err := saveState(st); err != nil {
		logf("[WARN] " + err.Error())
	}
	invalidateCache(true)
	logf(l.M("[OK] frp "+version+" installé.", "[OK] frp "+version+" installed."))
	webhookEmit("update.frp_installed", [2]string{"frp " + version + " installé", "frp " + version + " installed"},
		[2]string{"frp " + version + " a été installé depuis le panel.", "frp " + version + " was installed from the panel."},
		version, J{"version": version})
	return true
}

// installFrpCLI : frp-manager --install-frp, installe la dernière version de frp
// si frps/frpc manquent (utilisé par install.sh). → code de sortie.
func installFrpCLI() int {
	l := systemLang()
	if fileExists(filepath.Join(frpBinDir, "frps")) && fileExists(filepath.Join(frpBinDir, "frpc")) {
		fmt.Println(l.M("frps et frpc sont déjà installés.", "frps and frpc are already installed."))
		return 0
	}
	version, tag, _ := fetchLatestVersion(l)
	if version == "" {
		fmt.Println(l.M("[ERROR] Dernière version de frp introuvable (réseau ?).", "[ERROR] Could not find the latest frp version (network?)."))
		return 1
	}
	logf := func(s string) { fmt.Println(s) }
	tmp, _ := downloadArchive(l, version, tag, logf)
	if tmp == "" {
		return 1
	}
	defer os.Remove(tmp)
	if installFromArchive(l, tmp, version, logf) {
		return 0
	}
	return 1
}
