package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/mikolajwichrowski/baitai/internal/office"
)

const (
	cViolet = "#8B5CF6"
	cCyan   = "#22D3EE"
	cAmber  = "#F59E0B"
	cGreen  = "#10B981"
	cRed    = "#EF4444"
	cMuted  = "#6B7280"
	cFaint  = "#374151"
	cInk    = "#0B0B10"
	cWhite  = "#F9FAFB"
)

var (
	fg      = func(c string) lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(c)) }
	logoSt  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(cWhite)).Background(lipgloss.Color(cViolet)).Padding(0, 1)
	pillSt  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(cInk)).Padding(0, 1)
	dimSt   = fg(cMuted)
	tabSt   = fg(cMuted).Padding(0, 2)
	tabOnSt = fg(cWhite).Bold(true).Padding(0, 2)
	chipSt  = fg(cMuted).Padding(0, 1)
	inputSt = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(cViolet)).Padding(0, 1)
	cardSt  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(cFaint)).Padding(1, 3)
	modalSt = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(cViolet)).Padding(1, 2)
)

// kindColor is the pill colour of an entry; agents use their own colour.
func kindColor(e *office.Entry) string {
	switch e.Kind {
	case office.KindYou:
		return cCyan
	case office.KindBoss:
		return cViolet
	case office.KindNote:
		return cAmber
	case office.KindScoreUp:
		return cGreen
	case office.KindScoreDown, office.KindError:
		return cRed
	case office.KindThinking:
		return cFaint
	}
	if e.Col != "" {
		return e.Col
	}
	return cViolet
}

func bar(v, n int, col string) string {
	return fg(col).Render(strings.Repeat("▰", v)) + fg(cFaint).Render(strings.Repeat("▱", n-v))
}

func scoreColor(s int) string {
	switch {
	case s <= 3:
		return cRed
	case s <= 6:
		return cAmber
	}
	return cGreen
}
