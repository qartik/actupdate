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

type RenderOptions struct {
	Color bool
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

func Render(report Report, options RenderOptions) string {
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
		b.WriteString(style(options.Color, colorBoldBlue, filepath.ToSlash(file)))
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
				b.WriteString(fmt.Sprintf(
					"%s %s %s %s\n",
					entry.Display,
					style(options.Color, colorBoldGreen, "->"),
					style(options.Color, colorBoldGreen, "@"+entry.NewRef),
					style(options.Color, colorDim, "("+entry.Reason+")"),
				))
			case StatusUnchanged:
				b.WriteString(fmt.Sprintf("%s %s\n", entry.Display, style(options.Color, colorDim, "(unchanged: "+entry.Reason+")")))
			case StatusSkipped:
				b.WriteString(fmt.Sprintf("%s %s\n", entry.Display, style(options.Color, colorYellow, "(skipped: "+entry.Reason+")")))
			case StatusError:
				b.WriteString(fmt.Sprintf("%s %s\n", entry.Display, style(options.Color, colorBoldRed, "(verification failed: "+entry.Reason+")")))
			}
		}
	}

	b.WriteString(style(options.Color, colorBold, "Summary: "))
	b.WriteString(fmt.Sprintf(
		"%s, %s, %s, %s\n",
		style(options.Color, colorBoldGreen, fmt.Sprintf("%d updates", report.Counts.Updates)),
		style(options.Color, colorDim, fmt.Sprintf("%d unchanged", report.Counts.Unchanged)),
		style(options.Color, colorYellow, fmt.Sprintf("%d skipped", report.Counts.Skipped)),
		style(options.Color, colorBoldRed, fmt.Sprintf("%d verification errors", report.Counts.Errors)),
	))
	return b.String()
}

const (
	colorReset     = "\x1b[0m"
	colorBold      = "\x1b[1m"
	colorDim       = "\x1b[2m"
	colorYellow    = "\x1b[33m"
	colorBoldBlue  = "\x1b[1;34m"
	colorBoldGreen = "\x1b[1;32m"
	colorBoldRed   = "\x1b[1;31m"
)

func style(enabled bool, code string, value string) string {
	if !enabled {
		return value
	}
	return code + value + colorReset
}
