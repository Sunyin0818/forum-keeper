package ptsite

import (
	"embed"
	"fmt"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed presets.yaml
var presetFS embed.FS

// Preset is a reusable set of sign-in/message rules keyed by domain. A site
// config only needs to override the fields that differ.
type Preset struct {
	Name string `yaml:"name"`
	// Title is the friendly label used in cards/logs when the site config
	// does not set a name. Name remains the stable preset identifier.
	Title    string         `yaml:"title,omitempty"`
	Hosts    []string       `yaml:"hosts"`
	Signin   SigninConfig   `yaml:"signin,omitempty"`
	Messages MessagesConfig `yaml:"messages,omitempty"`
}

type presetFile struct {
	Presets []Preset `yaml:"presets"`
}

var (
	presetsOnce sync.Once
	presets     []Preset
	presetsErr  error
)

func loadPresets() ([]Preset, error) {
	presetsOnce.Do(func() {
		raw, err := presetFS.ReadFile("presets.yaml")
		if err != nil {
			presetsErr = fmt.Errorf("ptsite: read presets: %w", err)
			return
		}
		var file presetFile
		dec := yaml.NewDecoder(strings.NewReader(string(raw)))
		dec.KnownFields(true)
		if err := dec.Decode(&file); err != nil {
			presetsErr = fmt.Errorf("ptsite: parse presets: %w", err)
			return
		}
		seen := make(map[string]string)
		for i := range file.Presets {
			p := &file.Presets[i]
			p.Name = strings.ToLower(strings.TrimSpace(p.Name))
			if p.Name == "" {
				presetsErr = fmt.Errorf("ptsite: preset %d has no name", i)
				return
			}
			for j := range p.Hosts {
				p.Hosts[j] = normalizeHost(p.Hosts[j])
			}
			file.Presets[i] = *p
			for _, host := range p.Hosts {
				if host == "" {
					continue
				}
				if other, ok := seen[host]; ok {
					presetsErr = fmt.Errorf("ptsite: preset host %q is declared by %q and %q", host, other, p.Name)
					return
				}
				seen[host] = p.Name
			}
		}
		presets = file.Presets
	})
	return presets, presetsErr
}

// PresetNames returns the known preset names, sorted.
func PresetNames() []string {
	ps, err := loadPresets()
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name)
	}
	sort.Strings(out)
	return out
}

func applyPreset(s *SiteConfig) error {
	presets, err := loadPresets()
	if err != nil {
		return err
	}
	wanted := strings.ToLower(strings.TrimSpace(s.Preset))
	if wanted == "none" {
		return nil
	}

	var chosen *Preset
	if wanted != "" {
		for i := range presets {
			if presets[i].Name == wanted {
				chosen = &presets[i]
				break
			}
		}
		if chosen == nil {
			return fmt.Errorf("preset %q is unknown (known: %s)", s.Preset, strings.Join(PresetNames(), ", "))
		}
	} else {
		host := normalizeHost(s.host)
		for i := range presets {
			for _, h := range presets[i].Hosts {
				if h == host {
					chosen = &presets[i]
					break
				}
			}
			if chosen != nil {
				break
			}
		}
	}
	if chosen == nil {
		return nil
	}
	s.Preset = chosen.Name
	if chosen.Title != "" && (s.Name == "" || s.Name == s.host) {
		s.Name = chosen.Title
	}
	mergeSignin(&s.Signin, chosen.Signin)
	mergeMessages(&s.Messages, chosen.Messages)
	return nil
}

func normalizeHost(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "www.")
	return value
}

func mergeSignin(dst *SigninConfig, src SigninConfig) {
	mergeString(&dst.Mode, src.Mode)
	mergeString(&dst.Path, src.Path)
	mergeString(&dst.Method, src.Method)
	dst.Data = mergeStringMap(dst.Data, src.Data)
	mergeString(&dst.SubmitText, src.SubmitText)
	mergeString(&dst.Success, src.Success)
	mergeString(&dst.Already, src.Already)
	mergeString(&dst.LoginRequired, src.LoginRequired)
	if len(dst.AbsentKeywords) == 0 {
		dst.AbsentKeywords = append([]string(nil), src.AbsentKeywords...)
	}
	mergeString(&dst.VerifyPath, src.VerifyPath)
	mergeTwoStep(&dst.TwoStep, src.TwoStep)
	mergeString(&dst.Bonus, src.Bonus)
}

func mergeMessages(dst *MessagesConfig, src MessagesConfig) {
	mergeString(&dst.Engine, src.Engine)
	mergeString(&dst.Mode, src.Mode)
	mergeString(&dst.InboxPath, src.InboxPath)
	mergeString(&dst.SystemPath, src.SystemPath)
	if dst.MaxPages == 0 {
		dst.MaxPages = src.MaxPages
	}
	if dst.Selectors == nil {
		dst.Selectors = src.Selectors
	}
}

func mergeTwoStep(dst **TwoStepConfig, src *TwoStepConfig) {
	if src == nil {
		return
	}
	if *dst == nil {
		copied := *src
		copied.Data = mergeStringMap(nil, src.Data)
		copied.Tokens = mergeStringMap(nil, src.Tokens)
		copied.Headers = mergeStringMap(nil, src.Headers)
		*dst = &copied
		return
	}
	mergeString(&(*dst).Path, src.Path)
	mergeString(&(*dst).Method, src.Method)
	(*dst).Data = mergeStringMap((*dst).Data, src.Data)
	(*dst).Tokens = mergeStringMap((*dst).Tokens, src.Tokens)
	(*dst).Headers = mergeStringMap((*dst).Headers, src.Headers)
}

func mergeString(dst *string, src string) {
	if strings.TrimSpace(*dst) == "" {
		*dst = src
	}
}

func mergeStringMap(dst, src map[string]string) map[string]string {
	if len(src) == 0 {
		return dst
	}
	if len(dst) == 0 {
		dst = make(map[string]string, len(src))
	}
	for k, v := range src {
		if _, ok := dst[k]; !ok {
			dst[k] = v
		}
	}
	return dst
}
