// Textes du site (website/content.json) : interface en anglais et en français,
// pages de documentation, contenu de la page d'accueil.

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type feature struct {
	Img    string   `json:"img"`
	Alt    string   `json:"alt"`
	Title  string   `json:"title"`
	Text   string   `json:"text"`
	Points []string `json:"points"`
}

// step : texte d'une étape et commande (vide : pas de bloc de code).
type step struct {
	Text string
	Cmd  string
}

func (s *step) UnmarshalJSON(b []byte) error {
	var raw []*string
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if len(raw) > 0 && raw[0] != nil {
		s.Text = *raw[0]
	}
	if len(raw) > 1 && raw[1] != nil {
		s.Cmd = *raw[1]
	}
	return nil
}

type homeContent struct {
	Title         string      `json:"title"`
	Desc          string      `json:"desc"`
	H1            string      `json:"h1"`
	Lead          string      `json:"lead"`
	CtaDemo       string      `json:"cta_demo"`
	CtaInstall    string      `json:"cta_install"`
	Meta          string      `json:"meta"`
	RouteCaption  string      `json:"route_caption"`
	Route         [][3]string `json:"route"`
	RouteLinks    []string    `json:"route_links"`
	ShotAlt       string      `json:"shot_alt"`
	FeaturesTitle string      `json:"features_title"`
	FeaturesLead  string      `json:"features_lead"`
	Rows          []feature   `json:"rows"`
	More          [][3]string `json:"more"`
	DemoTitle     string      `json:"demo_title"`
	DemoLead      string      `json:"demo_lead"`
	DemoLaunch    string      `json:"demo_launch"`
	DemoFull      string      `json:"demo_full"`
	StartTitle    string      `json:"start_title"`
	StartLead     string      `json:"start_lead"`
	Tabs          []startTab  `json:"tabs"`
	StepsCommon   []step      `json:"steps_common"`
	StartMore     string      `json:"start_more"`
	WhyTitle      string      `json:"why_title"`
	WhyLead       string      `json:"why_lead"`
	WhyHead       [3]string   `json:"why_head"`
	WhyRows       [][3]string `json:"why_rows"`
	WhyNote       string      `json:"why_note"`
	SecTitle      string      `json:"sec_title"`
	SecLead       string      `json:"sec_lead"`
	SecPoints     [][2]string `json:"sec_points"`
	SecDocker     string      `json:"sec_docker"`
	SecMore       string      `json:"sec_more"`
	FaqTitle      string      `json:"faq_title"`
	Faq           [][2]string `json:"faq"`
	FinalTitle    string      `json:"final_title"`
	FinalText     string      `json:"final_text"`
}

// startTab : un onglet de « Démarrer » (Docker, apt, script…).
type startTab struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Steps []step `json:"steps"`
}

type content struct {
	Docs     [][2]any                  `json:"docs"`
	DocFiles map[string][2]string      `json:"doc_files"`
	T        map[string]map[string]any `json:"t"`
	Home     map[string]homeContent    `json:"home"`
}

type docPage struct {
	slug    string
	sources map[string]string // langue → fichier du dépôt
}

var (
	langs   = []string{"en", "fr"}
	texts   content
	docList []docPage
)

func loadContent(root string) error {
	raw, err := os.ReadFile(filepath.Join(root, "website", "content.json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &texts); err != nil {
		return err
	}
	docList = nil
	for _, d := range texts.Docs {
		srcs := map[string]string{}
		for k, v := range d[1].(map[string]any) {
			srcs[k] = v.(string)
		}
		docList = append(docList, docPage{d[0].(string), srcs})
	}
	return nil
}

// t : texte de l'interface du site.
func t(lang, key string) string { s, _ := texts.T[lang][key].(string); return s }

func docName(lang, slug string) string {
	m, _ := texts.T[lang]["doc_names"].(map[string]any)
	s, _ := m[slug].(string)
	return s
}

func docDesc(lang, slug string) string {
	m, _ := texts.T[lang]["doc_desc"].(map[string]any)
	s, _ := m[slug].(string)
	return s
}
