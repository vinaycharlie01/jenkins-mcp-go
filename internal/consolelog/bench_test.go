package consolelog

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vinaycharlie01/jenkins-mcp-go/internal/loganalysis"
)

func bigConsole() string {
	var b strings.Builder
	b.WriteString("[Pipeline] { (Build)\n")
	for i := 0; i < 45000; i++ {
		fmt.Fprintf(&b, "[2026-03-01T10:00:00.%03dZ] > Task :module%d:compileJava UP-TO-DATE\n", i%1000, i%400)
	}
	b.WriteString("[Pipeline] }\n")

	return b.String()
}

func BenchmarkParse(b *testing.B) {
	s := bigConsole()
	for b.Loop() {
		Parse(s)
	}
}

func BenchmarkAnalyzeErrors(b *testing.B) {
	p := Parse(bigConsole())
	for b.Loop() {
		AnalyzeErrors(p)
	}
}

func BenchmarkLogAnalysisParse(b *testing.B) {
	p := Parse(bigConsole())
	raw := make([]loganalysis.Line, len(p.Lines))
	for i, l := range p.Lines {
		raw[i] = loganalysis.Line{No: l.No, Source: l.Stage, Timestamp: l.Time, Content: l.Text}
	}
	for b.Loop() {
		loganalysis.Parse(raw)
	}
}

func BenchmarkPatterns(b *testing.B) {
	p := Parse(bigConsole())
	raw := make([]loganalysis.Line, len(p.Lines))
	for i, l := range p.Lines {
		raw[i] = loganalysis.Line{No: l.No, Source: l.Stage, Timestamp: l.Time, Content: l.Text}
	}
	es := loganalysis.Parse(raw)
	for b.Loop() {
		loganalysis.AnalyzePatterns(es)
	}
}
