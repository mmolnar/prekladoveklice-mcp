package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/mmolnar/prekladoveklice-mcp/internal/model"
)

func newTable(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
}

// printResults prints one compact line per entry:
//
//	#10015  aberration → zrůda  [Bytosti; TVOR] — note
func printResults(w io.Writer, r *model.SearchResult) {
	if r.Total == 0 {
		fmt.Fprintln(w, "No matches.")
		return
	}
	for _, e := range r.Entries {
		fmt.Fprintln(w, entryLine(e))
	}
	shown := r.Offset + len(r.Entries)
	if shown < r.Total {
		fmt.Fprintf(w, "-- %d-%d of %d matches (use --limit/--offset for more)\n", r.Offset+1, shown, r.Total)
	} else {
		fmt.Fprintf(w, "-- %d matches\n", r.Total)
	}
}

func entryLine(e model.Entry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#%d  %s → %s  [%s", e.ID, e.Original, e.Translation, e.Section)
	for _, t := range e.Tags {
		b.WriteString("; " + t.Name)
	}
	b.WriteString("]")
	if e.Deprecated {
		if e.ReplacedBy != 0 {
			fmt.Fprintf(&b, " (DEPRECATED, current: #%d)", e.ReplacedBy)
		} else {
			b.WriteString(" (DEPRECATED)")
		}
	}
	if len(e.Notes) > 0 {
		b.WriteString(" — " + strings.Join(e.Notes, "; "))
	}
	return b.String()
}

func printEntry(w io.Writer, e *model.Entry) {
	fmt.Fprintf(w, "id:          %d\noriginal:    %s\ntranslation: %s\nsection:     %s (%d)\n",
		e.ID, e.Original, e.Translation, e.Section, e.SectionID)
	if e.Deprecated {
		fmt.Fprintln(w, "deprecated:  yes")
	}
	if e.ReplacedBy != 0 {
		fmt.Fprintf(w, "replaced by: #%d\n", e.ReplacedBy)
	}
	if len(e.Tags) > 0 {
		names := make([]string, len(e.Tags))
		for i, t := range e.Tags {
			names[i] = fmt.Sprintf("%s (%s %d)", t.Name, t.Type, t.ID)
		}
		fmt.Fprintf(w, "tags:        %s\n", strings.Join(names, ", "))
	}
	for _, n := range e.Notes {
		fmt.Fprintf(w, "note:        %s\n", n)
	}
	for _, d := range e.DeprecatedTranslations {
		fmt.Fprintf(w, "deprecated translation: #%d %s → %s\n", d.ID, d.Original, d.Translation)
	}
}
