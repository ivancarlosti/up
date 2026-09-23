package services

import (
	"encoding/csv"
	"strings"

	"github.com/ivancarlosti/up/internal/models"
)

// maxBulkRows caps a single bulk request: it is a paste of a spreadsheet, not a
// data import pipeline.
const maxBulkRows = 500

// BulkRow is one parsed line of the bulk text.
type BulkRow struct {
	Line     int    `json:"line"`
	Name     string `json:"name"`
	Target   string `json:"target"`
	Type     string `json:"type,omitempty"`
	Keyword  string `json:"keyword,omitempty"`
	Tags     string `json:"tags,omitempty"`
	Interval string `json:"interval_seconds,omitempty"`
	Error    string `json:"error,omitempty"`
}

// BulkRowResult is the outcome of one row.
type BulkRowResult struct {
	Line      int    `json:"line"`
	Name      string `json:"name"`
	Status    string `json:"status"` // dry_run | created | duplicate | invalid | failed
	MonitorID uint   `json:"monitor_id,omitempty"`
	Error     string `json:"error,omitempty"`
}

// BulkReport is the answer of a bulk request.
type BulkReport struct {
	Parsed  int             `json:"parsed"`
	Created int             `json:"created"`
	DryRun  int             `json:"dry_run"`
	Skipped int             `json:"skipped"`
	Failed  int             `json:"failed"`
	Rows    []BulkRowResult `json:"rows"`
}

// BulkOptions are the inputs of a bulk creation.
type BulkOptions struct {
	Text       string
	TemplateID uint
	// GroupIDs adds the new monitors to these groups (on top of the ones the
	// template already carries).
	GroupIDs []uint
	// Active overrides the template default; nil keeps it.
	Active *bool
	DryRun bool
}

// ParseBulkText turns pasted text into rows.
//
// The format is a spreadsheet paste: one monitor per line, the name first and the
// target second, then optional columns (type, keyword, tags, interval). The
// delimiter is detected among comma, semicolon and tab, quoted cells are
// supported, blank lines and `#` comments are skipped and a header row such as
// `name,url` is recognised and ignored.
func ParseBulkText(text string) []BulkRow {
	rows := []BulkRow{}
	for _, record := range bulkRecords(text) {
		row := BulkRow{Line: record.line}
		cells := record.cells
		if len(cells) > 0 {
			row.Name = strings.TrimSpace(cells[0])
		}
		if len(cells) > 1 {
			row.Target = strings.TrimSpace(cells[1])
		}
		if len(cells) > 2 {
			row.Type = strings.ToLower(strings.TrimSpace(cells[2]))
		}
		if len(cells) > 3 {
			row.Keyword = strings.TrimSpace(cells[3])
		}
		if len(cells) > 4 {
			row.Tags = strings.TrimSpace(cells[4])
		}
		if len(cells) > 5 {
			row.Interval = strings.TrimSpace(cells[5])
		}
		switch {
		case row.Name == "":
			row.Error = "name is required"
		case row.Target == "":
			row.Error = "the target (second column) is required"
		case row.Type != "" && !models.MonitorType(row.Type).Valid():
			row.Error = "unknown type " + row.Type
		}
		rows = append(rows, row)
	}
	return rows
}

// bulkRecord is one parsed line with its original number.
type bulkRecord struct {
	line  int
	cells []string
}

// bulkRecords tokenises the text, skipping the header, the comments and the
// blank lines.
//
// The line number is computed from the bytes the reader consumed, not from the
// record index: csv.Reader hides the blank lines, so the index would point at the
// wrong line of the textarea the operator pasted into.
func bulkRecords(text string) []bulkRecord {
	delimiter := detectDelimiter(text)
	reader := csv.NewReader(strings.NewReader(text))
	reader.Comma = delimiter
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true

	out := []bulkRecord{}
	line, consumed := 0, 0
	for {
		cells, err := reader.Read()
		if offset := int(reader.InputOffset()); offset > consumed {
			line += strings.Count(text[consumed:offset], "\n")
			consumed = offset
		}
		if err != nil {
			// io.EOF or a malformed record: whatever was parsed so far is what
			// the preview shows.
			break
		}
		if isBlankRecord(cells) || isBulkHeader(cells) {
			continue
		}
		if first := strings.TrimSpace(cells[0]); strings.HasPrefix(first, "#") {
			continue
		}
		// A row with an empty name is NOT dropped here: ParseBulkText reports it
		// so the operator sees which line to fix.
		out = append(out, bulkRecord{line: line, cells: cells})
	}
	return out
}

// isBlankRecord reports whether every cell is empty (a blank or whitespace line).
func isBlankRecord(cells []string) bool {
	if len(cells) == 0 {
		return true
	}
	for _, cell := range cells {
		if strings.TrimSpace(cell) != "" {
			return false
		}
	}
	return true
}

// isBulkHeader recognises the `name,url` style header.
//
// Both cells must look like a column name: a monitor literally called "Name"
// pointing at an URL must not be swallowed by the header detection.
func isBulkHeader(cells []string) bool {
	if len(cells) < 2 {
		return false
	}
	first := strings.ToLower(strings.TrimSpace(cells[0]))
	second := strings.ToLower(strings.TrimSpace(cells[1]))
	nameLabels := map[string]bool{"name": true, "nome": true, "nombre": true}
	targetLabels := map[string]bool{"url": true, "target": true, "host": true, "hostname": true, "alvo": true, "objetivo": true, "destino": true}
	return nameLabels[first] && targetLabels[second]
}

// detectDelimiter picks the most frequent separator of the first data line.
func detectDelimiter(text string) rune {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		counts := map[rune]int{',': strings.Count(trimmed, ","), ';': strings.Count(trimmed, ";"), '\t': strings.Count(trimmed, "\t")}
		best, bestCount := ',', 0
		for _, candidate := range []rune{',', ';', '\t'} {
			if counts[candidate] > bestCount {
				best, bestCount = candidate, counts[candidate]
			}
		}
		return best
	}
	return ','
}
