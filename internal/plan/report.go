package plan

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

type Status string

const (
	StatusUpdate    Status = "update"
	StatusUnchanged Status = "unchanged"
	StatusSkipped   Status = "skipped"
	StatusError     Status = "error"
)

type Entry struct {
	FilePath string
	Line     int
	Display  string
	Status   Status
	NewRef   string
	Reason   string
}

type Counts struct {
	Updates    int
	Unchanged  int
	Skipped    int
	Errors     int
	TotalFiles int
}

type Report struct {
	Entries []Entry
	Counts  Counts
}

func (r *Report) Add(entry Entry) {
	r.Entries = append(r.Entries, entry)
	switch entry.Status {
	case StatusUpdate:
		r.Counts.Updates++
	case StatusUnchanged:
		r.Counts.Unchanged++
	case StatusSkipped:
		r.Counts.Skipped++
	case StatusError:
		r.Counts.Errors++
	}
}

func Render(report Report) string {
	if len(report.Entries) == 0 {
		return "No action references found.\n"
	}

	grouped := map[string][]Entry{}
	for _, entry := range report.Entries {
		grouped[entry.FilePath] = append(grouped[entry.FilePath], entry)
	}

	files := make([]string, 0, len(grouped))
	for file := range grouped {
		files = append(files, file)
	}
	sort.Strings(files)

	var b strings.Builder
	for _, file := range files {
		b.WriteString(filepath.ToSlash(file))
		b.WriteString(":\n")
		entries := grouped[file]
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].Line != entries[j].Line {
				return entries[i].Line < entries[j].Line
			}
			return entries[i].Display < entries[j].Display
		})
		for _, entry := range entries {
			b.WriteString("  ")
			switch entry.Status {
			case StatusUpdate:
				b.WriteString(fmt.Sprintf("%s -> @%s (%s)\n", entry.Display, entry.NewRef, entry.Reason))
			case StatusUnchanged:
				b.WriteString(fmt.Sprintf("%s (unchanged: %s)\n", entry.Display, entry.Reason))
			case StatusSkipped:
				b.WriteString(fmt.Sprintf("%s (skipped: %s)\n", entry.Display, entry.Reason))
			case StatusError:
				b.WriteString(fmt.Sprintf("%s (verification failed: %s)\n", entry.Display, entry.Reason))
			}
		}
	}

	b.WriteString(fmt.Sprintf(
		"Summary: %d updates, %d unchanged, %d skipped, %d verification errors\n",
		report.Counts.Updates,
		report.Counts.Unchanged,
		report.Counts.Skipped,
		report.Counts.Errors,
	))
	return b.String()
}
