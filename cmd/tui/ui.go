package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type state int

const (
	stateConfig state = iota
	stateRunning
	stateResults
)

type crawlerResult struct {
	Language  string
	ReqPerSec float64
	TimeTaken time.Duration
}

type progressMsg struct {
	reqs int64
}

type model struct {
	state        state
	spinner      spinner.Model
	results      []crawlerResult
	table        table.Model
	progressChan chan progressMsg
	currentReqs  int64

	// current benchmark index when running
	currentIdx  int
	currentPass int
	totalPasses int
	maxURLs     int
	targetURL   string
	crawlers    []string

	inputs     []textinput.Model
	focusIndex int
}

type benchmarkCompleteMsg struct {
	result crawlerResult
}

func initialModel() model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	columns := []table.Column{
		{Title: "Language", Width: 15},
		{Title: "Req/s", Width: 15},
		{Title: "Time", Width: 15},
	}
	t := table.New(
		table.WithColumns(columns),
		table.WithFocused(true),
		table.WithHeight(7),
	)

	var inputs []textinput.Model = make([]textinput.Model, 3)
	inputs[0] = textinput.New()
	inputs[0].Placeholder = "http://localhost:8080/page/1"
	inputs[0].Focus()
	inputs[0].CharLimit = 100
	inputs[0].Width = 30
	inputs[0].Prompt = "URL: "

	inputs[1] = textinput.New()
	inputs[1].Placeholder = "10000"
	inputs[1].CharLimit = 10
	inputs[1].Width = 10
	inputs[1].Prompt = "Max URLs: "

	inputs[2] = textinput.New()
	inputs[2].Placeholder = "1"
	inputs[2].CharLimit = 5
	inputs[2].Width = 5
	inputs[2].Prompt = "Passes: "

	return model{
		state:        stateConfig,
		spinner:      s,
		crawlers:     []string{"Go", "TypeScript", "Rust"},
		table:        t,
		progressChan: make(chan progressMsg, 1000), // Buffered to handle rapid prints
		inputs:       inputs,
		focusIndex:   0,
	}
}

func (m model) Init() tea.Cmd {
	return m.spinner.Tick
}

func waitForProgress(c <-chan progressMsg) tea.Cmd {
	return func() tea.Msg {
		return <-c
	}
}

func runRealBenchmark(language string, targetURL string, maxURLs int, progChan chan<- progressMsg) tea.Cmd {
	return func() tea.Msg {
		var cmd *exec.Cmd

		maxFlag := fmt.Sprintf("--max=%d", maxURLs)
		urlFlag := fmt.Sprintf("--url=%s", targetURL)

		switch language {
		case "Go":
			cmd = exec.Command("./bin/crawler-go", urlFlag, maxFlag, "-c=100")
		case "TypeScript":
			cmd = exec.Command("node", "index.js", urlFlag, maxFlag, "-c=100")
			cmd.Dir = "./cmd/crawler-ts"
		case "Rust":
			cmd = exec.Command("../../bin/crawler-rust", urlFlag, maxFlag, "-c=100")
			cmd.Dir = "./cmd/crawler-rust"
		default:
			return benchmarkCompleteMsg{
				result: crawlerResult{Language: language, ReqPerSec: 0, TimeTaken: 0},
			}
		}

		stderr, err := cmd.StderrPipe()
		if err != nil {
			return benchmarkCompleteMsg{
				result: crawlerResult{Language: language + " (Pipe Err)", ReqPerSec: 0, TimeTaken: 0},
			}
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return benchmarkCompleteMsg{
				result: crawlerResult{Language: language + " (Pipe Err)", ReqPerSec: 0, TimeTaken: 0},
			}
		}

		if err := cmd.Start(); err != nil {
			return benchmarkCompleteMsg{
				result: crawlerResult{Language: language + " (Start Err)", ReqPerSec: 0, TimeTaken: 0},
			}
		}

		// Read stderr for progress without blocking stdout
		go func() {
			scanner := bufio.NewScanner(stderr)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "PROGRESS: ") {
					valStr := strings.TrimSpace(strings.TrimPrefix(line, "PROGRESS: "))
					val, err := strconv.ParseInt(valStr, 10, 64)
					if err == nil {
						select {
						case progChan <- progressMsg{reqs: val}:
						default:
							// Drop if too fast
						}
					}
				}
			}
		}()

		out, err := io.ReadAll(stdout)
		if err != nil {
			return benchmarkCompleteMsg{result: crawlerResult{Language: language + " (IO Err)"}}
		}
		cmd.Wait()

		var res BenchmarkResult
		if err := json.Unmarshal(out, &res); err != nil {
			return benchmarkCompleteMsg{
				result: crawlerResult{
					Language:  language + " (Parse Err)",
					ReqPerSec: 0,
					TimeTaken: 0,
				},
			}
		}

		return benchmarkCompleteMsg{
			result: crawlerResult{
				Language:  res.Language,
				ReqPerSec: res.ReqPerSec,
				TimeTaken: time.Duration(res.TimeTakenMs) * time.Millisecond,
			},
		}
	}
}

// Ensure we have a matching type for the JSON payload
type BenchmarkResult struct {
	Language    string  `json:"language"`
	Requests    int64   `json:"requests"`
	TimeTakenMs int64   `json:"time_taken_ms"`
	ReqPerSec   float64 `json:"req_per_sec"`
	BytesRead   int64   `json:"bytes_read"`
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "shift+tab":
			if m.state == stateConfig {
				m.focusIndex--
				if m.focusIndex < 0 {
					m.focusIndex = len(m.inputs) - 1
				}
				for i := 0; i < len(m.inputs); i++ {
					if i == m.focusIndex {
						m.inputs[i].Focus()
					} else {
						m.inputs[i].Blur()
					}
				}
				return m, nil
			}
		case "down", "tab":
			if m.state == stateConfig {
				m.focusIndex++
				if m.focusIndex >= len(m.inputs) {
					m.focusIndex = 0
				}
				for i := 0; i < len(m.inputs); i++ {
					if i == m.focusIndex {
						m.inputs[i].Focus()
					} else {
						m.inputs[i].Blur()
					}
				}
				return m, nil
			}
		case "enter":
			if m.state == stateConfig {
				targetURL := m.inputs[0].Value()
				if targetURL == "" {
					targetURL = "http://localhost:8080/page/1"
				}
				maxURLs, err := strconv.Atoi(m.inputs[1].Value())
				if err != nil || maxURLs <= 0 {
					maxURLs = 10000
				}
				passes, err := strconv.Atoi(m.inputs[2].Value())
				if err != nil || passes <= 0 {
					passes = 1
				}

				m.targetURL = targetURL
				m.maxURLs = maxURLs
				m.totalPasses = passes
				m.state = stateRunning
				m.currentIdx = 0
				m.currentPass = 1
				m.currentReqs = 0
				m.results = nil // clear for new run
				return m, tea.Batch(
					runRealBenchmark(m.crawlers[m.currentIdx], m.targetURL, m.maxURLs, m.progressChan),
					waitForProgress(m.progressChan),
				)
			} else if m.state == stateResults {
				m.state = stateConfig
				return m, nil
			}
		case "r", "R":
			if m.state == stateResults {
				m.state = stateConfig
				return m, nil
			}
		}

	case progressMsg:
		m.currentReqs = msg.reqs
		return m, waitForProgress(m.progressChan)

	case benchmarkCompleteMsg:
		m.results = append(m.results, msg.result)
		m.currentIdx++
		m.currentReqs = 0
		if m.currentIdx < len(m.crawlers) {
			return m, runRealBenchmark(m.crawlers[m.currentIdx], m.targetURL, m.maxURLs, m.progressChan)
		} else {
			m.currentPass++
			if m.currentPass <= m.totalPasses {
				m.currentIdx = 0
				return m, runRealBenchmark(m.crawlers[m.currentIdx], m.targetURL, m.maxURLs, m.progressChan)
			} else {
				m.state = stateResults
				m.updateTableData()
				return m, nil
			}
		}

	case spinner.TickMsg:
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)
	}

	if m.state == stateConfig {
		for i := range m.inputs {
			m.inputs[i], cmd = m.inputs[i].Update(msg)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}

	return m, tea.Batch(cmds...)
}

func (m *model) updateTableData() {
	rows := []table.Row{}

	// Aggregate results if passes > 1
	agg := make(map[string]struct {
		reqPerSec float64
		timeTaken time.Duration
		count     int
	})

	// Maintain order of languages
	for _, cr := range m.crawlers {
		agg[cr] = struct {
			reqPerSec float64
			timeTaken time.Duration
			count     int
		}{}
	}

	for _, r := range m.results {
		baseLang := r.Language
		// handle errors which might change the language field
		for _, cr := range m.crawlers {
			if strings.HasPrefix(r.Language, cr) {
				baseLang = cr
				break
			}
		}

		val := agg[baseLang]
		val.reqPerSec += r.ReqPerSec
		val.timeTaken += r.TimeTaken
		val.count++
		agg[baseLang] = val
	}

	for _, cr := range m.crawlers {
		val := agg[cr]
		if val.count > 0 {
			avgReq := val.reqPerSec / float64(val.count)
			avgTime := time.Duration(int64(val.timeTaken) / int64(val.count))
			rows = append(rows, table.Row{
				cr,
				fmt.Sprintf("%.2f", avgReq),
				avgTime.String(),
			})
		}
	}

	m.table.SetRows(rows)
}

func (m model) progressBar(current, total int) string {
	width := 40
	if current > total {
		current = total
	}
	if current < 0 {
		current = 0
	}
	percent := float64(current) / float64(total)
	filled := int(percent * float64(width))
	if filled > width {
		filled = width
	}
	empty := width - filled

	bar := lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Render(strings.Repeat("█", filled))
	bar += lipgloss.NewStyle().Foreground(lipgloss.Color("237")).Render(strings.Repeat("█", empty))
	return bar
}

func (m model) View() string {
	var s string

	header := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("205")).
		Render("🕷️  Open-Crawl Benchmark Harness\n")

	s += header + "\n"

	switch m.state {
	case stateConfig:
		s += "Configure Benchmark and Press [ENTER] to start.\n\n"

		for i := range m.inputs {
			s += m.inputs[i].View()
			if i < len(m.inputs)-1 {
				s += "\n"
			}
		}
		s += "\n\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("Crawlers to run: Go, TypeScript, Rust\nConcurrency: 100")
		s += "\nUse Tab/Shift+Tab or Up/Down to switch inputs."
	case stateRunning:
		bar := m.progressBar(int(m.currentReqs), m.maxURLs)
		s += fmt.Sprintf("%s Running %s crawler (Pass %d/%d)...\nTarget: %s\n\n%s %d/%d requests",
			m.spinner.View(), m.crawlers[m.currentIdx], m.currentPass, m.totalPasses, m.targetURL, bar, m.currentReqs, m.maxURLs)
	case stateResults:
		s += "✅ Benchmarks Complete!\n\n"
		s += m.table.View()
		s += "\n\nPress 'r' or 'Enter' to restart."
	}

	s += "\n\nPress 'q' or 'ctrl+c' to quit.\n"

	return lipgloss.NewStyle().Margin(1, 2).Render(s)
}
