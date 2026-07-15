package ui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"elevatorsim/simulation"
)

// Config é o que a tela precisa saber sobre o run pra escrever o cabeçalho. Não tem
// nada de motor aqui — é tudo o que veio da linha de comando.
type Config struct {
	Seed  uint64
	Speed float64
	Day   time.Duration
	ASCII bool
}

// Model segura o ctx num campo. Normalmente é cheiro, mas em bubbletea é O padrão: o
// Update é síncrono e não recebe ctx, então quem precisa dele nos Cmd guarda aqui.
type Model struct {
	ctx    context.Context
	frames <-chan Frame
	prof   simulation.Profile // valor imutável: desenhar a curva não é falar com o motor
	cfg    Config
	frame  Frame
	w, h   int
	g      glyphs
}

func NewModel(ctx context.Context, frames <-chan Frame, prof simulation.Profile, cfg Config) Model {
	g := uniGlyphs
	if cfg.ASCII {
		g = asciiGlyphs
	}
	return Model{ctx: ctx, frames: frames, prof: prof, cfg: cfg, g: g}
}

type frameMsg Frame
type engineDoneMsg struct{}

// waitFrame é o ÚNICO ponto do programa em que a UI toca num canal do motor — e é
// sempre um recv guardado por ctx. Nada de "for f := range frames": os canais desse
// projeto não fecham (multi-sender, disciplina do assemble.go), então range aqui seria
// bloqueio eterno e goroutine vazada no shutdown, garantido.
func waitFrame(ctx context.Context, ch <-chan Frame) tea.Cmd {
	return func() tea.Msg {
		select {
		case f := <-ch:
			return frameMsg(f)
		case <-ctx.Done():
			return engineDoneMsg{}
		}
	}
}

func (m Model) Init() tea.Cmd { return waitFrame(m.ctx, m.frames) }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			// terminal em raw mode: o ctrl+c chega aqui como TECLA, não como sinal. Por
			// isso ele mora no Update e não só no NotifyContext do main.
			return m, tea.Quit
		}
	case frameMsg:
		m.frame = Frame(msg)
		// re-arma na hora. Como o cmd só volta a existir depois que o anterior foi
		// consumido, tem sempre EXATAMENTE UM recv em voo: não acumula goroutine e não
		// tem como frame chegar fora de ordem.
		return m, waitFrame(m.ctx, m.frames)
	case engineDoneMsg:
		return m, tea.Quit // ctx caiu: sai em vez de ficar pendurado
	}
	return m, nil
}
