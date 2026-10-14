// Package render turns ledger state into terminal output. It holds no
// business logic: the cli package decides what to show, this package decides
// how it looks.
package render

import (
	"strconv"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
)

// BoardRow is one ledger entry as shown on the board.
type BoardRow struct {
	ID               string
	Age              string
	DependentCommits int
	TouchedFiles     int
	BlastRadius      int
	Status           string
}

// Board table styling. The dim border keeps the grid quiet so the numbers
// carry the weight; status colors follow ledger semantics — amber while the
// debt is open, green once paid, cyan when refinanced, red when defaulted.
var (
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("8"))

	borderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	statusStyles = map[string]lipgloss.Style{
		"open":       lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		"paid":       lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		"refinanced": lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		"defaulted":  lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
	}
)

// Board renders the ledger table: one row per entry with its id, age and the
// three interest columns (dependent commits, touched files, blast radius),
// plus the entry status. Entries are shown in the order given — the cli
// passes oldest debt first.
func Board(rows []BoardRow) string {
	if len(rows) == 0 {
		return ""
	}

	t := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(borderStyle).
		Headers("ENTRY", "AGE", "DEPENDENT COMMITS", "TOUCHED FILES", "BLAST RADIUS", "STATUS").
		StyleFunc(boardStyle)

	for _, row := range rows {
		status := row.Status
		if style, ok := statusStyles[status]; ok {
			status = style.Render(status)
		}
		t.Row(row.ID, row.Age,
			strconv.Itoa(row.DependentCommits),
			strconv.Itoa(row.TouchedFiles),
			strconv.Itoa(row.BlastRadius),
			status)
	}
	return t.Render()
}

// boardStyle aligns the interest columns to the right so magnitudes compare
// at a glance, and pads every cell away from the border.
func boardStyle(row, col int) lipgloss.Style {
	style := lipgloss.NewStyle().Padding(0, 1)
	if row == table.HeaderRow {
		return headerStyle.Padding(0, 1)
	}
	switch col {
	case 2, 3, 4:
		return style.Align(lipgloss.Right)
	default:
		return style
	}
}
