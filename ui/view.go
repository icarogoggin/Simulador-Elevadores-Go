package ui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"elevatorsim/domain"
)

// dois conjuntos de glifo, e não é firula: terminal do Windows sem fonte decente
// transforma ▲ em caixinha e desalinha a coluna INTEIRA — o poço vira um serrote.
// -ascii é a saída de emergência.
// corner/hbar/tee e a borda da caixa entram aqui pelo mesmo motivo dos outros: são
// desenho de caixa, que é justamente o que vira tofu de largura errada e cisalha a
// coluna inteira. Deixar a caixa unicode no modo -ascii seria entregar meia solução.
// λ e τ ficam nos dois modos de propósito — não são desenho de caixa, não mexem em
// alinhamento nenhum, e são o nome da coisa.
type glyphs struct {
	up, down, idle, doors, wall, shaft, bar string
	corner, hbar, tee, to                   string
	border                                  lipgloss.Border
}

var uniGlyphs = glyphs{
	up: "▲", down: "▼", idle: "■", doors: "◧", wall: "│", shaft: "  ·  ", bar: "█",
	corner: "└", hbar: "─", tee: "┴", to: "→",
	border: lipgloss.RoundedBorder(),
}

var asciiGlyphs = glyphs{
	up: "^", down: "v", idle: "=", doors: "D", wall: "|", shaft: "  .  ", bar: "#",
	corner: "+", hbar: "-", tee: "+", to: "->",
	border: lipgloss.Border{
		Top: "-", Bottom: "-", Left: "|", Right: "|",
		TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+",
	},
}

func (g glyphs) arrow(d domain.Direction) string {
	switch d {
	case domain.Up:
		return g.up
	case domain.Down:
		return g.down
	default:
		return g.idle
	}
}

var (
	movingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	doorsStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	idleStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	fullStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	shaftStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	headStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("81")).Bold(true)
	theoStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("63"))
	obsStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
)

func (m Model) box() lipgloss.Style {
	return lipgloss.NewStyle().Border(m.g.border).Padding(0, 1)
}

// barra de fila: largura fixa, satura com '+' e o número vai do lado. O número
// sozinho não dá a sensação de fila; a barra dá, e o número tira a ambiguidade
// quando ela satura.
const maxBar = 18

const sparkW = domain.ProfileBins

var sparkRunes = []rune(" ▁▂▃▄▅▆▇█")

// Terminal apertado: aviso em vez de tela mutilada em silêncio. E as duas dimensões,
// não só a largura — quem corta o excedente é o renderer do bubbletea, e ele corta a
// altura pelo TOPO (o header, o relógio, o "[q] sair"), o que aparece pro usuário como
// se o poço tivesse perdido o andar de cima. Sintoma de off-by-one pra um bug que é de
// clipping: caro de diagnosticar, e a culpa é de não ter olhado m.h.
//
// A exigência sai de MEDIR o render que a gente acabou de montar, em vez de const
// cravada. Já teve uma cravada aqui (64) que era só a largura da sparkline e ignorava o
// header — mentia "precisa de 64" enquanto o layout pedia ~104. Número cravado
// envelhece na primeira vez que alguém mexe no box de MÉTRICAS ou no NumCars; o header
// ainda por cima cresce com os dígitos do seed. Medir não tem como discordar do desenho.
func (m Model) View() string {
	s := m.render()
	needW, needH := lipgloss.Width(s), lipgloss.Height(s)
	if (m.w > 0 && m.w < needW) || (m.h > 0 && m.h < needH) {
		return fmt.Sprintf("terminal apertado: %dx%d, precisa de %dx%d.\n[q] sair\n",
			m.w, m.h, needW, needH)
	}
	return s
}

func (m Model) render() string {
	var b strings.Builder
	b.WriteString(m.header())
	b.WriteString("\n\n")
	b.WriteString(m.shaft())
	b.WriteString("\n")
	b.WriteString(m.sparklines())
	b.WriteString("\n")
	b.WriteString(m.bottom())
	return b.String()
}

func (m Model) header() string {
	f := m.frame
	acc := 0.0
	if f.Gen.Candidates > 0 {
		acc = 100 * float64(f.Gen.Generated) / float64(f.Gen.Candidates)
	}
	day := 1 + int(f.Gen.SimSeconds/math.Max(f.Gen.DaySeconds, 1))
	left := headStyle.Render(" ELEVATORSIM") + dimStyle.Render(fmt.Sprintf(
		"   t=%s  ·  %gx  ·  seed %d  ·  dia %d  τ=%.2f  λ=%.2f/s (λ*=%.2f)  aceit. %.1f%%",
		clock(f.Elapsed), m.cfg.Speed, m.cfg.Seed, day, f.Gen.Phase, f.Gen.Lambda, f.Gen.LambdaMax, acc))
	return left + dimStyle.Render("   [q] sair")
}

func clock(d time.Duration) string {
	s := int(d.Seconds())
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}

// célula do carro: 5 colunas fixas, [glifo + lotação]. Cor conta o estado sem precisar
// de legenda, e a lotação é o dígito ali do lado pra não ter que caçar na linha de
// baixo qual carro é qual.
func (m Model) carCell(c CarView) string {
	var glyph string
	var st lipgloss.Style
	switch c.State {
	case domain.StateDoorsOpen:
		glyph, st = m.g.doors, doorsStyle
	case domain.StateMoving:
		glyph, st = m.g.arrow(c.Dir), movingStyle
	default:
		glyph, st = m.g.idle, idleStyle
	}
	if c.Load >= domain.Capacity {
		st = fullStyle // lotado grita, porque é o que explica o ETA feio
	}
	return st.Render(fmt.Sprintf("[%s %d]", glyph, c.Load))
}

// poço com o 10 EM CIMA e o 1 embaixo — é como prédio é.
func (m Model) shaft() string {
	var b strings.Builder
	wall := shaftStyle.Render(m.g.wall)
	empty := shaftStyle.Render(m.g.shaft)

	for fl := domain.MaxFloor; fl >= domain.MinFloor; fl-- {
		b.WriteString(fmt.Sprintf("   %2d ", fl))
		b.WriteString(wall)
		for i := range m.frame.Cars {
			cell := empty
			if m.frame.Seen[i] && m.frame.Cars[i].Floor == fl {
				cell = m.carCell(m.frame.Cars[i])
			}
			b.WriteString(cell)
			b.WriteString(wall)
		}
		b.WriteString("  ")
		b.WriteString(m.waitBar(int(m.frame.Waiting[fl])))
		b.WriteString("\n")
	}
	seg := strings.Repeat(m.g.hbar, 5)
	b.WriteString("      " + shaftStyle.Render(
		m.g.corner+strings.Repeat(seg+m.g.tee, domain.NumCars-1)+seg+m.g.corner) + "\n")
	b.WriteString(dimStyle.Render(fmt.Sprintf(
		"       %s = porta aberta   %s = fila no andar   lotação = o dígito na cabine (cap. %d)",
		m.g.doors, m.g.bar, domain.Capacity)) + "\n")
	return b.String()
}

// preenche até maxBar com espaço pra a coluna do número não dançar.
func (m Model) waitBar(n int) string {
	w := n
	sat := ""
	if w > maxBar {
		w, sat = maxBar, "+"
	}
	st := idleStyle
	switch {
	case n >= 12:
		st = fullStyle
	case n >= 5:
		st = doorsStyle
	case n > 0:
		st = movingStyle
	}
	bar := st.Render(strings.Repeat(m.g.bar, w) + sat)
	pad := maxBar + 1 - lipgloss.Width(bar) // lipgloss.Width, nunca len: len conta os bytes do ANSI
	if pad < 0 {
		pad = 0
	}
	return bar + strings.Repeat(" ", pad) + fmt.Sprintf(" %3d", n)
}

// o par teórico-vs-observado é a peça de portfólio: a teórica sai de prof.Density (função
// pura), a observada do Gen.Hist (nível cumulativo). Dá pra VER o NHPP convergindo pra
// própria intensidade em tempo real.
func (m Model) sparklines() string {
	theo := make([]float64, sparkW)
	for i := range theo {
		theo[i] = m.prof.Density((float64(i) + 0.5) / sparkW)
	}
	obs := make([]float64, sparkW)
	for i, v := range m.frame.Gen.Hist {
		obs[i] = float64(v)
	}

	cursor := strings.Repeat(" ", sparkW)
	if m.frame.Gen.Candidates > 0 {
		p := int(m.frame.Gen.Phase * sparkW)
		if p >= 0 && p < sparkW {
			cursor = strings.Repeat(" ", p) + m.g.up + strings.Repeat(" ", sparkW-p-1)
		}
	}

	w := m.g.wall
	return fmt.Sprintf(" λ(τ) teórico %s%s%s\n observado    %s%s%s\n               %s\n",
		w, theoStyle.Render(spark(theo, m.cfg.ASCII)), w,
		w, obsStyle.Render(spark(obs, m.cfg.ASCII)), w,
		dimStyle.Render(cursor))
}

func spark(v []float64, ascii bool) string {
	var mx float64
	for _, x := range v {
		mx = math.Max(mx, x)
	}
	var b strings.Builder
	for _, x := range v {
		if mx <= 0 {
			b.WriteByte(' ')
			continue
		}
		i := int(math.Round(x / mx * float64(len(sparkRunes)-1)))
		if ascii {
			// sem as runas de bloco: três níveis já contam a história da curva.
			switch {
			case i >= 6:
				b.WriteByte('#')
			case i >= 3:
				b.WriteByte('+')
			case i >= 1:
				b.WriteByte('.')
			default:
				b.WriteByte(' ')
			}
			continue
		}
		b.WriteRune(sparkRunes[i])
	}
	return b.String()
}

func (m Model) bottom() string {
	f := m.frame
	p95 := "—"
	if f.P95Wait > 0 {
		p95 = "<= " + round(f.P95Wait)
	}
	metrics := m.box().Render(strings.Join([]string{
		headStyle.Render("MÉTRICAS"),
		fmt.Sprintf("gerados   %4d/%-4d   média   %8s", f.Gen.Generated, f.Gen.Target, round(f.MeanWait)),
		fmt.Sprintf("atendidos %4d        p95     %8s", f.Served, p95),
		fmt.Sprintf("esperando %4d        máx     %8s", f.WaitingTotal, round(f.MaxWait)),
		fmt.Sprintf("a bordo   %4d        vazão   %7.2f/s", f.Onboard, f.Throughput),
		fmt.Sprintf("em voo    %4d", f.InFlight()),
	}, "\n"))

	rows := []string{headStyle.Render("ÚLTIMOS EMBARQUES")}
	for i := 0; i < recentN; i++ {
		if f.RecentN-1-i < 0 {
			break
		}
		s := f.Recent[(f.RecentN-1-i)%recentN]
		rows = append(rows, dimStyle.Render(fmt.Sprintf("#%04d C%d andar %2d %s %s",
			s.PassengerID, s.CarID, s.PickupFloor, m.g.to, round(s.Wait)))) // Wait já vem em tempo de simulação
	}
	recent := lipgloss.NewStyle().Padding(1, 0, 0, 2).Render(strings.Join(rows, "\n"))

	return lipgloss.JoinHorizontal(lipgloss.Top, metrics, recent) + "\n"
}

func round(d time.Duration) string {
	if d == 0 {
		return "—"
	}
	return d.Round(10 * time.Millisecond).String()
}
