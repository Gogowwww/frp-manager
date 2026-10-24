// Chiffres publics du dépôt GitHub : téléchargements des fichiers de chaque
// release, étoiles, forks, et un historique quotidien.
// Public figures of the GitHub repository: downloads of each release file,
// stars, forks, and a daily history.
//
//	go run ./tools/ghstats [-repo Gogowwww/frp-manager] [-out stats.json] [-history history.json]
//
// Variable : GH_TOKEN (facultative, relève la limite de l'API ; le jeton du
// workflow suffit). Rien de privé n'est lu : ce sont les mêmes chiffres que
// l'API publique de GitHub. Les téléchargements mesurent l'adoption par
// version, pas le nombre de panels actifs.
// Variable: GH_TOKEN (optional, raises the API limit; the workflow token is
// enough). Nothing private is read: these are the same figures as GitHub's
// public API. Downloads measure adoption per version, not the number of active
// panels.

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const maxHistory = 400

// apiRepo et apiRelease : les champs lus dans l'API GitHub.
// apiRepo and apiRelease: the fields read from the GitHub API.
type apiRepo struct {
	Stars      int `json:"stargazers_count"`
	Forks      int `json:"forks_count"`
	Watchers   int `json:"subscribers_count"`
	OpenIssues int `json:"open_issues_count"`
}

type apiRelease struct {
	Tag         string     `json:"tag_name"`
	Prerelease  bool       `json:"prerelease"`
	Draft       bool       `json:"draft"`
	PublishedAt string     `json:"published_at"`
	Assets      []apiAsset `json:"assets"`
}

type apiAsset struct {
	Name      string `json:"name"`
	Downloads int    `json:"download_count"`
}

// Stats : le fichier publié (stats.json).
// Stats: the published file (stats.json).
type Stats struct {
	GeneratedAt    string        `json:"generated_at"`
	Repo           string        `json:"repo"`
	Stars          int           `json:"stars"`
	Forks          int           `json:"forks"`
	Watchers       int           `json:"watchers"`
	OpenIssues     int           `json:"open_issues"`
	TotalDownloads int           `json:"total_downloads"`
	Releases       []ReleaseStat `json:"releases"`
}

// ReleaseStat : les téléchargements d'une release, par type de fichier.
// ReleaseStat: the downloads of one release, per file type.
type ReleaseStat struct {
	Tag        string         `json:"tag"`
	Prerelease bool           `json:"prerelease"`
	Published  string         `json:"published"`
	Downloads  int            `json:"downloads"`
	ByKind     map[string]int `json:"by_kind"`
}

// Snapshot : une ligne de l'historique quotidien (history.json).
// Snapshot: one line of the daily history (history.json).
type Snapshot struct {
	Date      string `json:"date"`
	Stars     int    `json:"stars"`
	Forks     int    `json:"forks"`
	Downloads int    `json:"downloads"`
}

// kind classe un fichier de release : paquet apt, archive, somme ou autre.
// kind sorts a release file: apt package, archive, checksum or other.
func kind(name string) string {
	switch {
	case strings.HasSuffix(name, ".deb"):
		return "deb"
	case strings.HasSuffix(name, ".zip"):
		return "zip"
	case strings.HasSuffix(name, ".sha256"):
		return "sha256"
	}
	return "other"
}

// summarize assemble les chiffres ; les brouillons sont ignorés, les releases
// vont de la plus récente à la plus ancienne.
// summarize builds the figures; drafts are ignored, releases go from newest to
// oldest.
func summarize(repo string, r apiRepo, releases []apiRelease, now time.Time) Stats {
	s := Stats{
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Repo:        repo,
		Stars:       r.Stars,
		Forks:       r.Forks,
		Watchers:    r.Watchers,
		OpenIssues:  r.OpenIssues,
		Releases:    []ReleaseStat{},
	}
	for _, rel := range releases {
		if rel.Draft {
			continue
		}
		rs := ReleaseStat{Tag: rel.Tag, Prerelease: rel.Prerelease, Published: rel.PublishedAt, ByKind: map[string]int{}}
		for _, a := range rel.Assets {
			rs.Downloads += a.Downloads
			rs.ByKind[kind(a.Name)] += a.Downloads
		}
		s.TotalDownloads += rs.Downloads
		s.Releases = append(s.Releases, rs)
	}
	sort.SliceStable(s.Releases, func(i, j int) bool { return s.Releases[i].Published > s.Releases[j].Published })
	return s
}

// addHistory ajoute (ou remplace, pour le même jour) la ligne du jour et ne
// garde que les maxHistory plus récentes.
// addHistory adds (or replaces, for the same day) today's line and only keeps
// the most recent maxHistory ones.
func addHistory(h []Snapshot, s Stats) []Snapshot {
	day := Snapshot{Date: s.GeneratedAt[:10], Stars: s.Stars, Forks: s.Forks, Downloads: s.TotalDownloads}
	if n := len(h); n > 0 && h[n-1].Date == day.Date {
		h[n-1] = day
	} else {
		h = append(h, day)
	}
	if len(h) > maxHistory {
		h = h[len(h)-maxHistory:]
	}
	return h
}

// get lit une page de l'API GitHub et la décode dans v.
// get reads one page of the GitHub API and decodes it into v.
func get(client *http.Client, url, token string, v any) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s : HTTP %d", url, resp.StatusCode)
	}
	return json.Unmarshal(body, v)
}

// fetch interroge l'API (base : https://api.github.com) : le dépôt, puis toutes
// les pages de releases.
// fetch queries the API (base: https://api.github.com): the repository, then
// every page of releases.
func fetch(client *http.Client, base, repo, token string) (apiRepo, []apiRelease, error) {
	var r apiRepo
	if err := get(client, base+"/repos/"+repo, token, &r); err != nil {
		return r, nil, err
	}
	var all []apiRelease
	for page := 1; page <= 20; page++ {
		var batch []apiRelease
		if err := get(client, fmt.Sprintf("%s/repos/%s/releases?per_page=100&page=%d", base, repo, page), token, &batch); err != nil {
			return r, nil, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return r, all, nil
}

func run(repo, out, history string, client *http.Client, base string, now time.Time) error {
	if !strings.Contains(repo, "/") {
		return errors.New("dépôt attendu sous la forme propriétaire/nom")
	}
	r, releases, err := fetch(client, base, repo, os.Getenv("GH_TOKEN"))
	if err != nil {
		return err
	}
	s := summarize(repo, r, releases, now)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, append(data, '\n'), 0o644); err != nil {
		return err
	}
	if history == "" {
		return nil
	}
	var h []Snapshot
	if old, err := os.ReadFile(history); err == nil {
		if err := json.Unmarshal(old, &h); err != nil {
			return fmt.Errorf("%s illisible : %w", history, err)
		}
	}
	data, err = json.MarshalIndent(addHistory(h, s), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(history, append(data, '\n'), 0o644)
}

func main() {
	repo := flag.String("repo", "Gogowwww/frp-manager", "dépôt GitHub")
	out := flag.String("out", "stats.json", "fichier des chiffres")
	history := flag.String("history", "", "fichier de l'historique quotidien (facultatif)")
	flag.Parse()
	client := &http.Client{Timeout: 30 * time.Second}
	if err := run(*repo, *out, *history, client, "https://api.github.com", time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, "ghstats :", err)
		os.Exit(1)
	}
	fmt.Println("ghstats : écrit", *out)
}
