// Package tui holds the interactive prompts (Bubble Tea) used by the CLI.
package tui

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
)

// ErrCancelled is returned when the user presses Esc or Ctrl+C.
var ErrCancelled = errors.New("cancelled")

// Interactive reports whether prompts can be shown.
func Interactive() bool {
	return term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd()) && os.Getenv("CI") == ""
}

// Option is one choice in a list.
type Option struct {
	Label string
	Hint  string
	Value string
}

// --- Select -------------------------------------------------------------------

type selectModel struct {
	title    string
	help     string
	options  []Option
	cursor   int
	selected map[int]bool
	multi    bool
	done     bool
	err      string
}

func (m selectModel) Init() tea.Cmd { return nil }

func (m selectModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := message.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "ctrl+c", "esc":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.options)-1 {
			m.cursor++
		}
	case "space":
		if m.multi {
			m.selected[m.cursor] = !m.selected[m.cursor]
		}
	case "enter":
		if m.multi && len(m.chosen()) == 0 {
			m.err = "Select at least one option (space)."
			return m, nil
		}
		m.done = true
		return m, tea.Quit
	}
	return m, nil
}

func (m selectModel) chosen() []int {
	var indexes []int
	for index := range m.options {
		if m.selected[index] {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func (m selectModel) View() tea.View {
	var b strings.Builder
	b.WriteString(m.title + "\n")
	if m.help != "" {
		b.WriteString(dim(m.help) + "\n")
	}
	b.WriteString("\n")
	for index, option := range m.options {
		cursor := "  "
		if index == m.cursor {
			cursor = "> "
		}
		box := ""
		if m.multi {
			box = "[ ] "
			if m.selected[index] {
				box = "[x] "
			}
		}
		line := cursor + box + option.Label
		if option.Hint != "" {
			line += "  " + dim(option.Hint)
		}
		b.WriteString(line + "\n")
	}
	keys := "↑/↓ move · Enter confirm · Esc cancel"
	if m.multi {
		keys = "↑/↓ move · Space toggle · Enter confirm · Esc cancel"
	}
	b.WriteString("\n" + dim(keys))
	if m.err != "" {
		b.WriteString("\n" + m.err)
	}
	return tea.NewView(b.String())
}

// Select asks for one option and returns its Value.
func Select(title, help string, options []Option, initial string) (string, error) {
	if len(options) == 0 {
		return "", fmt.Errorf("%s: no options", title)
	}
	model := selectModel{title: title, help: help, options: options, selected: map[int]bool{}}
	for index, option := range options {
		if option.Value == initial {
			model.cursor = index
		}
	}
	result, err := tea.NewProgram(model).Run()
	if err != nil {
		return "", err
	}
	final := result.(selectModel)
	if !final.done {
		return "", ErrCancelled
	}
	return final.options[final.cursor].Value, nil
}

// MultiSelect asks for one or more options and returns their Values.
func MultiSelect(title, help string, options []Option, initial []string) ([]string, error) {
	model := selectModel{title: title, help: help, options: options, selected: map[int]bool{}, multi: true}
	for index, option := range options {
		for _, value := range initial {
			if option.Value == value {
				model.selected[index] = true
			}
		}
	}
	result, err := tea.NewProgram(model).Run()
	if err != nil {
		return nil, err
	}
	final := result.(selectModel)
	if !final.done {
		return nil, ErrCancelled
	}
	var values []string
	for _, index := range final.chosen() {
		values = append(values, final.options[index].Value)
	}
	return values, nil
}

// --- Input --------------------------------------------------------------------

type inputModel struct {
	title    string
	help     string
	input    textinput.Model
	validate func(string) error
	value    string
	err      string
	done     bool
}

func (m inputModel) Init() tea.Cmd { return textinput.Blink }

func (m inputModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := message.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "enter":
			value := m.input.Value()
			if m.validate != nil {
				if err := m.validate(value); err != nil {
					m.err = err.Error()
					return m, nil
				}
			}
			m.value = value
			m.done = true
			return m, tea.Quit
		}
	}
	var command tea.Cmd
	m.input, command = m.input.Update(message)
	return m, command
}

func (m inputModel) View() tea.View {
	if m.done {
		return tea.NewView("")
	}
	view := m.title + "\n"
	if m.help != "" {
		view += dim(m.help) + "\n"
	}
	view += "\n" + m.input.View() + "\n\n" + dim("Enter confirm · Esc cancel")
	if m.err != "" {
		view += "\n" + m.err
	}
	return tea.NewView(view)
}

// InputOptions configures Input.
type InputOptions struct {
	Help        string
	Placeholder string
	Initial     string
	Secret      bool
	Validate    func(string) error
}

// Input asks for a line of text. Secret masks the typed value.
func Input(title string, options InputOptions) (string, error) {
	input := textinput.New()
	input.Placeholder = options.Placeholder
	input.SetValue(options.Initial)
	input.SetWidth(64)
	input.CharLimit = 4096
	if options.Secret {
		input.EchoMode = textinput.EchoPassword
	}
	input.Focus()
	result, err := tea.NewProgram(inputModel{title: title, help: options.Help, input: input, validate: options.Validate}).Run()
	if err != nil {
		return "", err
	}
	final := result.(inputModel)
	if !final.done {
		return "", ErrCancelled
	}
	return final.value, nil
}

// Required rejects blank values.
func Required(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("A value is required.")
	}
	return nil
}

// Confirm asks a yes/no question.
func Confirm(question string, defaultYes bool) (bool, error) {
	initial := "no"
	if defaultYes {
		initial = "yes"
	}
	answer, err := Select(question, "", []Option{{Label: "Yes", Value: "yes"}, {Label: "No", Value: "no"}}, initial)
	if err != nil {
		return false, err
	}
	return answer == "yes", nil
}

// TypeToConfirm requires typing word exactly (used before store releases).
func TypeToConfirm(question, word string) (bool, error) {
	_, err := Input(question, InputOptions{
		Help: fmt.Sprintf("Type %q to continue.", word),
		Validate: func(value string) error {
			if strings.TrimSpace(value) != word {
				return fmt.Errorf("Type %q exactly, or press Esc to cancel.", word)
			}
			return nil
		},
	})
	if errors.Is(err, ErrCancelled) {
		return false, nil
	}
	return err == nil, err
}

func dim(text string) string {
	return "\x1b[2m" + text + "\x1b[0m"
}
