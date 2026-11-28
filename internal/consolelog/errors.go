package consolelog

import (
	"fmt"
	"sort"

	"github.com/vinaycharlie01/jenkins-mcp-go/internal/loganalysis"
)

const (
	maxSample  = 400
	maxStages  = 10
	contextLen = 3
)

// ErrorGroup is a repeated error (same category and normalized text).
type ErrorGroup struct {
	Category  string   `json:"category"`
	Pattern   string   `json:"pattern"`
	Count     int      `json:"count"`
	Stages    []string `json:"stages,omitempty"`
	FirstLine int      `json:"first_line"`
	LastLine  int      `json:"last_line"`
	Sample    string   `json:"sample"`
	Command   string   `json:"command,omitempty"`
}

// Cause is the most likely root cause of a failure.
type Cause struct {
	Category string   `json:"category"`
	Stage    string   `json:"stage,omitempty"`
	Line     int      `json:"line"`
	Text     string   `json:"text"`
	Command  string   `json:"command,omitempty"`
	Reason   string   `json:"reason"`
	Before   []string `json:"context_before,omitempty"`
	After    []string `json:"context_after,omitempty"`
}

// ErrorSummary is the error analysis of a console log.
type ErrorSummary struct {
	Categories  map[string]int `json:"categories"`
	ErrorLines  int            `json:"error_lines"`
	FailedStage string         `json:"failed_stage,omitempty"`
	ExitCode    string         `json:"exit_code,omitempty"`
	LikelyCause *Cause         `json:"likely_cause,omitempty"`
	Groups      []ErrorGroup   `json:"groups"`
}

func addUnique(list []string, v string, limit int) []string {
	if v == "" || len(list) >= limit {
		return list
	}
	for _, x := range list {
		if x == v {
			return list
		}
	}

	return append(list, v)
}

// AnalyzeErrors classifies error lines, groups them (most specific category
// first) and picks the likely root cause in the failing stage.
func AnalyzeErrors(p Parsed) ErrorSummary {
	s := ErrorSummary{Categories: map[string]int{}, Groups: []ErrorGroup{}}
	groups := map[string]*ErrorGroup{}
	type hit struct {
		i   int
		cat string
	}
	var hits []hit
	for i, l := range p.Lines {
		cat := l.Category // classified once in Parse
		if cat == "" {
			continue
		}
		hits = append(hits, hit{i, cat})
		s.ErrorLines++
		s.Categories[cat]++
		pat := loganalysis.Redact(loganalysis.Normalize(l.Text))
		k := cat + "\x00" + pat
		g := groups[k]
		if g == nil {
			g = &ErrorGroup{Category: cat, Pattern: pat, FirstLine: l.No, Sample: loganalysis.Clip(l.Text, maxSample), Command: loganalysis.Clip(l.Command, 300)}
			groups[k] = g
		}
		g.Count++
		g.LastLine = l.No
		g.Stages = addUnique(g.Stages, l.Stage, maxStages)
	}
	for _, g := range groups {
		s.Groups = append(s.Groups, *g)
	}
	sort.Slice(s.Groups, func(i, j int) bool {
		a, b := s.Groups[i], s.Groups[j]
		if Rank(a.Category) != Rank(b.Category) {
			return Rank(a.Category) < Rank(b.Category)
		}

		return a.FirstLine < b.FirstLine
	})

	// Failing stage: the stage whose script exited non-zero, else the last
	// stage with error lines.
	for _, st := range p.Stages {
		if st.ExitCode != "" && st.ExitCode != "0" {
			s.FailedStage, s.ExitCode = st.Name, st.ExitCode
		}
	}
	if s.FailedStage == "" {
		for _, st := range p.Stages {
			if st.ErrorLines > 0 {
				s.FailedStage = st.Name
			}
		}
	}

	// Likely cause: most specific category within the failing stage
	// (whole log if no stage); earliest line wins ties.
	best := -1
	for _, h := range hits {
		if s.FailedStage != "" && p.Lines[h.i].Stage != s.FailedStage {
			continue
		}
		if best < 0 || Rank(h.cat) < Rank(hits[best].cat) {
			best = indexOf(hits, h.i, func(x hit) int { return x.i })
		}
	}
	if best >= 0 {
		h := hits[best]
		l := p.Lines[h.i]
		c := &Cause{Category: h.cat, Stage: l.Stage, Line: l.No, Text: loganalysis.Clip(l.Text, maxSample), Command: loganalysis.Clip(l.Command, 300)}
		where := "the log"
		if s.FailedStage != "" {
			where = fmt.Sprintf("failing stage %q", s.FailedStage)
		}
		c.Reason = fmt.Sprintf("most specific error (%s) in %s", h.cat, where)
		if s.ExitCode != "" {
			c.Reason += fmt.Sprintf(", which exited with code %s", s.ExitCode)
		}
		for j := h.i - 1; j >= 0 && j >= h.i-contextLen; j-- {
			if p.Lines[j].Stage == l.Stage {
				c.Before = append([]string{loganalysis.Clip(p.Lines[j].Text, 300)}, c.Before...)
			}
		}
		for j := h.i + 1; j < len(p.Lines) && j <= h.i+contextLen; j++ {
			if p.Lines[j].Stage == l.Stage {
				c.After = append(c.After, loganalysis.Clip(p.Lines[j].Text, 300))
			}
		}
		s.LikelyCause = c
	}

	return s
}

func indexOf[T any](xs []T, want int, key func(T) int) int {
	for i, x := range xs {
		if key(x) == want {
			return i
		}
	}

	return -1
}
