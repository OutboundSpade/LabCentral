// Package instructor defines private template instruction and guest programs.
// It has no hosted policy, database, identity or provider dependencies.
package instructor

import (
	"crypto/sha256"
	"crypto/subtle"
	lc "github.com/OutboundSpade/LabCentral/v2"
	"strings"
	"time"
	"unicode"
)

type Visibility struct {
	Audience       string   `json:"audience" yaml:"audience"`
	HiddenMachines []string `json:"hiddenMachines,omitempty" yaml:"hiddenMachines,omitempty"`
}
type Practice struct {
	DesktopMachine string `json:"desktopMachine" yaml:"desktopMachine"`
}
type Step struct {
	Machine        string   `json:"machine" yaml:"machine"`
	Playbook       string   `json:"playbook" yaml:"playbook"`
	Files          []string `json:"files,omitempty" yaml:"files,omitempty"`
	Assets         []string `json:"assets,omitempty" yaml:"assets,omitempty"`
	GuestHosts     []string `json:"guestHosts,omitempty" yaml:"guestHosts,omitempty"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty" yaml:"timeoutSeconds,omitempty"`
}

func (s Step) Timeout() time.Duration {
	if s.TimeoutSeconds == 0 {
		return 90 * time.Second
	}
	return time.Duration(s.TimeoutSeconds) * time.Second
}

// Deadline includes transport/lock/termination allowance for every step.
func Deadline(steps []Step) time.Duration {
	var d time.Duration
	for _, s := range steps {
		d += s.Timeout() + 15*time.Second
	}
	return d
}

type Check struct {
	ID         string        `json:"id" yaml:"id"`
	Name       string        `json:"name" yaml:"name"`
	Points     int           `json:"points" yaml:"points"`
	Steps      []Step        `json:"steps" yaml:"steps"`
	Extensions lc.Extensions `json:"extensions,omitempty" yaml:"extensions,omitempty"`
}
type Break struct {
	ID          string        `json:"id" yaml:"id"`
	Name        string        `json:"name" yaml:"name"`
	Description string        `json:"description,omitempty" yaml:"description,omitempty"`
	Walkthrough string        `json:"walkthrough,omitempty" yaml:"walkthrough,omitempty"`
	Steps       []Step        `json:"steps" yaml:"steps"`
	Extensions  lc.Extensions `json:"extensions,omitempty" yaml:"extensions,omitempty"`
}
type Answer struct {
	Accepted      []string `json:"accepted" yaml:"accepted"`
	CaseSensitive bool     `json:"caseSensitive" yaml:"caseSensitive"`
}

func (a Answer) Matches(value string) bool {
	normalize := func(s string) string {
		s = strings.TrimSpace(s)
		if !a.CaseSensitive {
			var folded strings.Builder
			for _, r := range s {
				best := r
				for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
					if next < best {
						best = next
					}
				}
				folded.WriteRune(best)
			}
			s = folded.String()
		}
		return s
	}
	got := sha256.Sum256([]byte(normalize(value)))
	matched := 0
	for _, value := range a.Accepted {
		want := sha256.Sum256([]byte(normalize(value)))
		matched |= subtle.ConstantTimeCompare(got[:], want[:])
	}
	return matched == 1
}

type Completion struct {
	Answer     *Answer `json:"answer,omitempty" yaml:"answer,omitempty"`
	Check      string  `json:"check,omitempty" yaml:"check,omitempty"`
	Instructor *bool   `json:"instructor,omitempty" yaml:"instructor,omitempty"`
}
type Stage struct {
	ID           string        `json:"id" yaml:"id"`
	Title        string        `json:"title" yaml:"title"`
	Instructions string        `json:"instructions" yaml:"instructions"`
	Prompt       string        `json:"prompt,omitempty" yaml:"prompt,omitempty"`
	Solution     string        `json:"solution,omitempty" yaml:"solution,omitempty"`
	Break        string        `json:"break,omitempty" yaml:"break,omitempty"`
	Completion   Completion    `json:"completion" yaml:"completion"`
	Extensions   lc.Extensions `json:"extensions,omitempty" yaml:"extensions,omitempty"`
}

func (s Stage) Kind() string {
	if s.Completion.Answer != nil {
		return "answer"
	}
	if s.Completion.Check != "" {
		return "action"
	}
	return "instructor"
}

type Library struct {
	Category         string   `json:"category" yaml:"category"`
	Difficulty       string   `json:"difficulty" yaml:"difficulty"`
	EstimatedMinutes int      `json:"estimatedMinutes" yaml:"estimatedMinutes"`
	Tags             []string `json:"tags,omitempty" yaml:"tags,omitempty"`
}
type Scenario struct {
	ID               string    `json:"id" yaml:"id"`
	Title            string    `json:"title" yaml:"title"`
	Description      string    `json:"description" yaml:"description"`
	Difficulty       string    `json:"difficulty" yaml:"difficulty"`
	EstimatedMinutes int       `json:"estimatedMinutes" yaml:"estimatedMinutes"`
	Variants         []Variant `json:"variants" yaml:"variants"`
}
type Variant struct {
	ID         string   `json:"id" yaml:"id"`
	Compatible bool     `json:"compatible" yaml:"compatible"`
	Breaks     []string `json:"breaks" yaml:"breaks"`
	Stages     []string `json:"stages" yaml:"stages"`
}
type Bundle struct {
	Version    int           `json:"version" yaml:"version"`
	Visibility Visibility    `json:"visibility" yaml:"visibility"`
	Practice   *Practice     `json:"practice,omitempty" yaml:"practice,omitempty"`
	Library    *Library      `json:"library,omitempty" yaml:"library,omitempty"`
	Notes      string        `json:"notes,omitempty" yaml:"notes,omitempty"`
	Extensions lc.Extensions `json:"extensions,omitempty" yaml:"extensions,omitempty"`
	Reset      []Step        `json:"reset,omitempty" yaml:"reset,omitempty"`
	Checks     []Check       `json:"checks,omitempty" yaml:"checks,omitempty"`
	Breaks     []Break       `json:"breaks,omitempty" yaml:"breaks,omitempty"`
	Stages     []Stage       `json:"stages,omitempty" yaml:"stages,omitempty"`
	Scenarios  []Scenario    `json:"scenarios,omitempty" yaml:"scenarios,omitempty"`
	Files      []lc.File     `json:"files" yaml:"-"`
	raw        []byte
}

func (*Bundle) StrictYAMLShape() {}
