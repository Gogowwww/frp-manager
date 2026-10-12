// Catalogue des listes communautaires du pare-feu (branche « blocklists » du
// dépôt GitHub public) : vérification, ajout à la main, et robot qui ajoute les
// listes publiées par issue, sans relecture humaine.
//
//	go run ./tools/blocklists [-dir catalogue] check           vérifie blocklists.json
//	go run ./tools/blocklists [-dir catalogue] add entree.json ajoute ou remplace une liste
//	go run ./tools/blocklists [-dir catalogue] sync            le robot (workflow blocklists.yml)
//
// Les règles sont celles du panel (même règle des adresses publiques :
// internal/netaddr) : le panel écarte de lui-même les entrées refusées, ce
// programme les refuse avant la fusion, pour que le catalogue reste propre.
//
// sync : pour chaque issue ouverte dont le titre commence par « Nouvelle liste »
// ou « Mise à jour de la liste », l'entrée JSON de l'issue est vérifiée ; une
// liste existante ne peut être remplacée que par le compte GitHub qui l'a
// publiée (champ « github »). Acceptée : ajoutée au catalogue, poussée, puis
// l'issue est fermée avec un commentaire ; refusée : l'issue est fermée avec la
// raison. Si le push échoue, les issues acceptées restent ouvertes et sont
// retraitées au passage suivant. Variables : GH_TOKEN (commenter et fermer les
// issues), GH_REPO, GIT_AUTH (en-tête d'authentification pour git push).

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Gogowwww/frp-manager/internal/netaddr"
)

const (
	maxSources = 20000
	maxASNs    = 50
)

var (
	listID   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	asnRe    = regexp.MustCompile(`(?i)^AS\s*(\d{1,10})$`)
	titleRe  = regexp.MustCompile(`(?i)^\s*(Nouvelle liste|Mise à jour de la liste)`)
	blockRe  = regexp.MustCompile("(?s)```(?:json)?\\s*\n(.*?)\n```")
	entryKey = []string{"id", "name", "mode", "description", "author", "updated", "github", "sources"}
)

// ── Objets JSON dans l'ordre de leurs clés (le catalogue est relu par des humains) ──

type field struct {
	Key string
	Val json.RawMessage
}

type object []field

func (o *object) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return errors.New("objet JSON attendu")
	}
	*o = nil
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return err
		}
		o.set(t.(string), v)
	}
	_, err := dec.Token()
	return err
}

func (o object) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, f := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, _ := marshal(f.Key)
		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(f.Val)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func (o object) raw(key string) (json.RawMessage, bool) {
	for _, f := range o {
		if f.Key == key {
			return f.Val, true
		}
	}
	return nil, false
}

// set : remplace la valeur (même place) ou ajoute la clé à la fin.
func (o *object) set(key string, v json.RawMessage) {
	for i := range *o {
		if (*o)[i].Key == key {
			(*o)[i].Val = v
			return
		}
	}
	*o = append(*o, field{key, v})
}

func (o *object) setString(key, s string) { b, _ := marshal(s); o.set(key, b) }

// text : valeur d'un champ en texte, comme str(x or "") en Python.
func (o object) text(key string) string {
	raw, ok := o.raw(key)
	if !ok {
		return ""
	}
	var v any
	json.Unmarshal(raw, &v)
	return pyText(v)
}

func pyText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return ""
	case float64:
		if x == 0 {
			return ""
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// ── Règles d'une liste ──────────────────────────────────────────────────────

// checkList : problèmes de la liste (vide si elle est acceptable).
func checkList(raw json.RawMessage) []string {
	var lst object
	if err := json.Unmarshal(raw, &lst); err != nil {
		return []string{`l'entrée doit être un objet JSON { "id": …, "name": …, "sources": [...] }`}
	}
	id := "?"
	if _, ok := lst.raw("id"); ok {
		id = lst.text("id")
	}
	where := fmt.Sprintf("liste « %s »", id)
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, where+" : "+fmt.Sprintf(format, args...)) }
	if modeRaw, ok := lst.raw("mode"); ok {
		var mode any
		json.Unmarshal(modeRaw, &mode)
		if mode != "block" && mode != "allow" {
			shown := pyText(mode)
			if mode == nil {
				shown = "None"
			}
			add("mode « %s » inconnu (block ou allow)", shown)
		}
	}
	if utf8.RuneCountInString(lst.text("author")) > 40 {
		add("auteur trop long (40 caractères au plus)")
	}
	if !listID.MatchString(lst.text("id")) {
		add("id invalide (minuscules, chiffres, tirets, 40 caractères au plus)")
	}
	if strings.TrimSpace(lst.text("name")) == "" {
		add("nom manquant")
	}
	if utf8.RuneCountInString(lst.text("name")) > 60 || utf8.RuneCountInString(lst.text("description")) > 300 {
		add("nom (60) ou description (300) trop long")
	}
	var sources []any
	if r, ok := lst.raw("sources"); !ok || json.Unmarshal(r, &sources) != nil || len(sources) == 0 {
		return append(errs, where+" : aucune entrée")
	}
	if len(sources) > maxSources {
		add("%d entrées, %d au plus", len(sources), maxSources)
	}
	asns, seen := 0, map[string]bool{}
	for _, item := range sources {
		s, ok := item.(string)
		if !ok {
			s = pyText(item)
		}
		text, _, _ := strings.Cut(s, "#")
		text = strings.TrimSpace(text)
		if seen[text] {
			add("« %s » en double", text)
		}
		seen[text] = true
		if m := asnRe.FindStringSubmatch(text); m != nil {
			asns++
			if n, _ := strconv.ParseUint(m[1], 10, 64); n < 1 || n > 4294967295 {
				add("« %s » n'est pas un numéro d'AS valide", text)
			}
			continue
		}
		p, ok := netaddr.ParseNetwork(text)
		if !ok {
			add("« %s » n'est ni une adresse, ni un réseau, ni un AS", text)
			continue
		}
		minBits := 8
		if p.Addr().Is6() {
			minBits = 16
		}
		if !netaddr.PrefixIsGlobal(p) {
			add("« %s » est privée ou réservée", text)
		} else if p.Bits() < minBits {
			add("« %s » est trop large (/8 au plus, /16 en IPv6)", text)
		}
	}
	if asns > maxASNs {
		add("%d AS, %d au plus", asns, maxASNs)
	}
	return errs
}

// ── Catalogue ───────────────────────────────────────────────────────────────

type catalog struct {
	path  string
	head  object   // champs du fichier, dont "version" ("lists" réécrit à l'enregistrement)
	lists []object // listes, dans l'ordre du fichier
}

func loadCatalog(dir string) (*catalog, error) {
	c := &catalog{path: filepath.Join(dir, "blocklists.json")}
	raw, err := os.ReadFile(c.path)
	if err != nil {
		return nil, err
	}
	bad := errors.New(`blocklists.json : attendu {"version": 1, "lists": [...]}`)
	if err := json.Unmarshal(raw, &c.head); err != nil {
		return nil, bad
	}
	var version any
	if v, ok := c.head.raw("version"); ok {
		json.Unmarshal(v, &version)
	}
	listsRaw, ok := c.head.raw("lists")
	if version != float64(1) || !ok || json.Unmarshal(listsRaw, &c.lists) != nil || c.lists == nil {
		return nil, bad
	}
	return c, nil
}

func (c *catalog) find(id string) object {
	for _, l := range c.lists {
		if l.text("id") == id {
			return l
		}
	}
	return nil
}

// put : ajoute la liste (en remplaçant celle de même id) ; vrai si remplacée.
func (c *catalog) put(entry object) bool {
	id, replaced := entry.text("id"), false
	kept := c.lists[:0]
	for _, l := range c.lists {
		if l.text("id") == id {
			replaced = true
			continue
		}
		kept = append(kept, l)
	}
	c.lists = append(kept, entry)
	return replaced
}

// save : listes triées par id, indentation de 2 espaces, caractères non ASCII tels quels.
func (c *catalog) save() error {
	sort.SliceStable(c.lists, func(i, j int) bool { return c.lists[i].text("id") < c.lists[j].text("id") })
	lists, err := marshal(c.lists)
	if err != nil {
		return err
	}
	c.head.set("lists", lists)
	compact, err := marshal(c.head)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", "  "); err != nil {
		return err
	}
	out.WriteByte('\n')
	return os.WriteFile(c.path, out.Bytes(), 0o644)
}

func today() string { return time.Now().Format("2006-01-02") }

// ── Commandes ───────────────────────────────────────────────────────────────

func cmdCheck(dir string) error {
	c, err := loadCatalog(dir)
	if err != nil {
		return err
	}
	var problems []string
	count := map[string]int{}
	for _, l := range c.lists {
		b, _ := marshal(l)
		problems = append(problems, checkList(b)...)
		count[l.text("id")]++
	}
	var dup []string
	for id, n := range count {
		if n > 1 {
			dup = append(dup, id)
		}
	}
	sort.Strings(dup)
	for _, id := range dup {
		problems = append(problems, fmt.Sprintf("id « %s » utilisé plusieurs fois", id))
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "\n"))
	}
	fmt.Printf("%d liste(s), tout est en ordre\n", len(c.lists))
	return nil
}

func cmdAdd(dir, file string) error {
	c, err := loadCatalog(dir)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if problems := checkList(raw); len(problems) > 0 {
		return errors.New(strings.Join(problems, "\n"))
	}
	var entry object
	json.Unmarshal(raw, &entry)
	entry.setString("updated", today())
	verb := "Ajoutée"
	if c.put(entry) {
		verb = "Remplacée"
	}
	if err := c.save(); err != nil {
		return err
	}
	fmt.Printf("%s : %s\n", verb, entry.text("id"))
	return nil
}

// ── Robot : issues GitHub → catalogue ───────────────────────────────────────

type issue struct {
	Number      int             `json:"number"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	PullRequest json.RawMessage `json:"pull_request"`
	User        struct {
		Login string `json:"login"`
	} `json:"user"`
}

type robot struct {
	api, repo, token, dir string
	client                *http.Client
}

func (r *robot) github(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, r.api+"/repos/"+r.repo+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+r.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "frp-manager-blocklists")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("GitHub %s %s : HTTP %d", method, path, resp.StatusCode)
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (r *robot) close(is issue, text string, accepted bool) error {
	n := strconv.Itoa(is.Number)
	if err := r.github("POST", "/issues/"+n+"/comments", map[string]string{"body": text}, nil); err != nil {
		return err
	}
	reason := "not_planned"
	if accepted {
		reason = "completed"
	}
	return r.github("PATCH", "/issues/"+n, map[string]string{"state": "closed", "state_reason": reason}, nil)
}

func (r *robot) git(args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = r.dir, os.Stdout, os.Stderr
	return cmd
}

// entryOf : entrée propre de l'issue, ou la raison du refus.
func entryOf(is issue) (object, string) {
	m := blockRe.FindStringSubmatch(is.Body)
	if m == nil {
		return nil, "aucun bloc ```json``` trouvé dans l'issue."
	}
	raw := []byte(m[1])
	var probe any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Sprintf("le JSON est illisible (%v). Avez-vous bien collé l'entrée copiée depuis le panel ?", err)
	}
	if problems := checkList(raw); len(problems) > 0 {
		for i, p := range problems {
			problems[i] = "- " + p
		}
		return nil, strings.Join(problems, "\n")
	}
	var full, entry object
	json.Unmarshal(raw, &full)
	for _, k := range entryKey {
		if v, ok := full.raw(k); ok {
			entry = append(entry, field{k, v})
		}
	}
	if _, ok := entry.raw("mode"); !ok {
		entry.setString("mode", "block")
	}
	entry.setString("github", is.User.Login)
	entry.setString("updated", today())
	return entry, ""
}

type accepted struct {
	is      issue
	entry   object
	updated bool
}

func cmdSync(dir string) error {
	token := os.Getenv("GH_TOKEN")
	if token == "" {
		return errors.New("variable GH_TOKEN manquante")
	}
	repo := os.Getenv("GH_REPO")
	if repo == "" {
		repo = "Gogowwww/frp-manager"
	}
	api := os.Getenv("GITHUB_API") // tests
	if api == "" {
		api = "https://api.github.com"
	}
	r := &robot{api: api, repo: repo, token: token, dir: dir, client: &http.Client{Timeout: 30 * time.Second}}
	var all []issue
	if err := r.github("GET", "/issues?state=open&per_page=100", nil, &all); err != nil {
		return err
	}
	var issues []issue
	for _, is := range all {
		if len(is.PullRequest) == 0 && titleRe.MatchString(is.Title) {
			issues = append(issues, is)
		}
	}
	if len(issues) == 0 {
		return nil
	}
	sort.Slice(issues, func(i, j int) bool { return issues[i].Number < issues[j].Number })
	c, err := loadCatalog(dir)
	if err != nil {
		return err
	}
	var done []accepted
	for _, is := range issues {
		entry, why := entryOf(is)
		var old object
		if entry != nil {
			id := entry.text("id")
			old = c.find(id)
			if old != nil && !strings.EqualFold(old.text("github"), entry.text("github")) {
				owner := old.text("github")
				if owner == "" {
					owner = "?"
				}
				entry, why = nil, fmt.Sprintf("la liste « %s » appartient à @%s. "+
					"Seul son auteur peut la mettre à jour : choisissez un autre nom.", id, owner)
			}
		}
		if entry == nil {
			fmt.Printf("#%d refusée : %s\n", is.Number, why)
			msg := "❌ Liste refusée :\n\n" + why + "\n\nCorrigez puis publiez-la à nouveau depuis le panel."
			if err := r.close(is, msg, false); err != nil {
				return err
			}
			continue
		}
		c.put(entry)
		if err := c.save(); err != nil {
			return err
		}
		if err := r.git("add", "blocklists.json").Run(); err != nil {
			return err
		}
		if r.git("diff", "--cached", "--quiet").Run() != nil {
			verb := "Ajout"
			if old != nil {
				verb = "Mise à jour"
			}
			msg := fmt.Sprintf("%s : %s (#%d)", verb, entry.text("name"), is.Number)
			if err := r.git("commit", "-q", "-m", msg).Run(); err != nil {
				return err
			}
		}
		done = append(done, accepted{is, entry, old != nil})
		fmt.Printf("#%d acceptée : %s\n", is.Number, entry.text("id"))
	}
	if len(done) == 0 {
		return nil
	}
	push := r.git("-c", "http.https://github.com/.extraheader="+os.Getenv("GIT_AUTH"), "push", "-q", "origin", "HEAD:blocklists")
	if err := push.Run(); err != nil {
		return fmt.Errorf("git push : %w", err)
	}
	for _, a := range done {
		kind := "de blocage"
		if a.entry.text("mode") == "allow" {
			kind = "d'autorisation"
		}
		what := "ajoutée au catalogue"
		if a.updated {
			what = "mise à jour"
		}
		var sources []any
		if s, ok := a.entry.raw("sources"); ok {
			json.Unmarshal(s, &sources)
		}
		msg := fmt.Sprintf("✅ Liste %s « %s » %s (%d entrées). Les panels la voient sous quelques minutes : "+
			"Pare-feu → Listes communautaires → Actualiser.", kind, a.entry.text("name"), what, len(sources))
		if err := r.close(a.is, msg, true); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	dir := flag.String("dir", ".", "dossier du catalogue (clone de la branche blocklists)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage : go run ./tools/blocklists [-dir catalogue] check | add entree.json | sync")
	}
	flag.Parse()
	args := flag.Args()
	var err error
	switch {
	case len(args) == 1 && args[0] == "check":
		err = cmdCheck(*dir)
	case len(args) == 2 && args[0] == "add":
		err = cmdAdd(*dir, args[1])
	case len(args) == 1 && args[0] == "sync":
		if err = cmdSync(*dir); err != nil {
			err = fmt.Errorf("Échec : %w", err)
		}
	default:
		flag.Usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
