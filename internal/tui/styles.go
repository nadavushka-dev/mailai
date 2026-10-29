package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Palette and frame helpers, shared with Enigma so the two tools look like
// siblings (copied from enigma/internal/tui/styles.go, MIT).
var (
	colAccent = lipgloss.AdaptiveColor{Light: "#5A56E0", Dark: "#9D99FF"}
	colDim    = lipgloss.AdaptiveColor{Light: "#9B9B9B", Dark: "#6C6C6C"}
	colOK     = lipgloss.AdaptiveColor{Light: "#1F8A4C", Dark: "#4FD18B"}
	colWarn   = lipgloss.AdaptiveColor{Light: "#B26A00", Dark: "#F2B04A"}
	colBad    = lipgloss.AdaptiveColor{Light: "#C0392B", Dark: "#FF6B6B"}
	colSuper  = lipgloss.AdaptiveColor{Light: "#8250DF", Dark: "#C297FF"}

	styleDim      = lipgloss.NewStyle().Foreground(colDim)
	styleOK       = lipgloss.NewStyle().Foreground(colOK)
	styleWarn     = lipgloss.NewStyle().Foreground(colWarn)
	styleBad      = lipgloss.NewStyle().Foreground(colBad)
	styleAccent   = lipgloss.NewStyle().Foreground(colAccent)
	styleSuper    = lipgloss.NewStyle().Foreground(colSuper)
	styleBold     = lipgloss.NewStyle().Bold(true)
	styleSelected = lipgloss.NewStyle().Reverse(true)
	styleTab      = lipgloss.NewStyle().Padding(0, 1)
	styleTabOn    = lipgloss.NewStyle().Padding(0, 1).Reverse(true).Bold(true)
	styleBorder   = lipgloss.NewStyle().Foreground(colDim)
	styleFocused  = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
)

// fit cuts or pads s to exactly width cells.
func fit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = lipgloss.NewStyle().MaxWidth(width).Render(s)
	if pad := width - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// box draws a titled frame of exactly width x height around body lines.
func box(title, body string, width, height int, focused bool) string {
	if width < 4 || height < 2 {
		return ""
	}
	border := styleBorder
	if focused {
		border = styleFocused
	}
	inner := width - 2
	t := " " + title + " "
	if lipgloss.Width(t) > inner {
		t = fit(t, inner)
	}
	top := border.Render("┌"+t+strings.Repeat("─", max(0, inner-lipgloss.Width(t)))) + border.Render("┐")
	lines := strings.Split(body, "\n")
	out := []string{top}
	for i := 0; i < height-2; i++ {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		out = append(out, border.Render("│")+fit(l, inner)+border.Render("│"))
	}
	out = append(out, border.Render("└"+strings.Repeat("─", inner)+"┘"))
	return strings.Join(out, "\n")
}

// padBetween puts left and right at the edges of width.
func padBetween(left, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return left + " " + right
	}
	return left + strings.Repeat(" ", gap) + right
}

// center places s in the middle of a width x height area.
func center(s string, width, height int) string {
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, s)
}
