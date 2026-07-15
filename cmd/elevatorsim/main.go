// Command elevatorsim sobe o prédio inteiro: gerador de tráfego, core (controller +
// 3 cabines) e a tela. Dois modos, uma verdade só: o TUI e o headless consomem
// exatamente os mesmos Frames do mesmo Pump, então não tem como os dois discordarem
// sobre a média.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"elevatorsim/domain"
	"elevatorsim/simulation"
	"elevatorsim/ui"
)

func main() { os.Exit(run()) }

func run() int {
	var (
		headless   = flag.Bool("headless", false, "sem TUI: imprime progresso e resumo. É o modo que roda em pipe/CI.")
		passengers = flag.Int("passengers", 1000, "quantos passageiros gerar (EXATO — o laço para na N-ésima chegada)")
		seed       = flag.Uint64("seed", 1, "semente; mesma semente = mesmo processo de chegada")
		speed      = flag.Float64("speed", 1, "dilatação do tempo: escala a física E as chegadas juntas, então a carga ρ não muda")
		day        = flag.Duration("day", 20*time.Minute, "duração de um dia virtual, ANTES do -speed")
		duration   = flag.Duration("duration", 0, "teto de relógio de parede; 0 = sem teto")
		progress   = flag.Duration("progress", 2*time.Second, "headless: intervalo do relatório (> 0; não tem 'desligado' aqui)")
		fps        = flag.Int("fps", 20, "taxa de desenho")
		ascii      = flag.Bool("ascii", false, "só ASCII, pra terminal que não desenha caixa/seta direito")
	)
	flag.Parse()

	// -progress entra aqui junto com o resto porque ele também vira NewTicker lá no
	// runHeadless, e NewTicker com intervalo <= 0 é pânico, não erro. Tentador ler
	// "-progress 0" como "sem relatório" por causa do "0 = sem teto" do -duration, mas
	// são coisas diferentes: o -duration desliga um TETO, e relatório nenhum num run
	// headless é só um pipe mudo. Melhor recusar do que fingir.
	if *speed <= 0 || *day <= 0 || *passengers <= 0 || *fps <= 0 || *progress <= 0 {
		fmt.Fprintln(os.Stderr, "speed, day, passengers, fps e progress têm que ser > 0")
		return 2
	}

	// o pump não usa -fps, usa o intervalo DERIVADO dele — e divisão inteira arredonda
	// pra zero quando fps passa de 1e9. Aí o pânico do NewTicker nasce lá dentro da
	// goroutine do pump, sem passar por nenhum defer daqui: stack trace no lugar de uma
	// linha de uso. Então valida o que vai ser usado, não o que foi digitado. A ordem
	// importa: o *fps <= 0 acima tem que vir antes, senão a divisão aqui estoura.
	frameEvery := time.Second / time.Duration(*fps)
	if frameEvery <= 0 {
		fmt.Fprintf(os.Stderr, "fps alto demais (%d): o intervalo entre quadros arredonda pra zero\n", *fps)
		return 2
	}

	// headless sem -speed explícito acelera sozinho. Rodar 20min de prédio parado num
	// pipe de CI não serve pra nada, e um default ruim é pior que um default opinativo.
	// Se o cara passou -speed na mão, respeita — daí o flag.Visit em vez de "if == 1".
	if *headless && !flagPassed("speed") {
		*speed = 30
	}

	// -speed é DILATAÇÃO PURA do mundo: divide a física e o dia do gerador pelo MESMO
	// fator. Como λ e a capacidade escalam juntas, o adimensional ρ = λ/µ não muda — um
	// run a 30x tem exatamente o mesmo comportamento de fila de um run a 1x, só que em
	// 1/30 do tempo. Escalar só o gerador daria um número bonito e sem sentido.
	//
	// E a escala é aplicada UMA vez, aqui: controller e motor têm que compartilhar o
	// MESMO Timing ou todo ETA sai errado (ver domain.Timing).
	timing := domain.Timing{
		FloorTravel: dilate(domain.DefaultTiming.FloorTravel, *speed),
		DoorCycle:   dilate(domain.DefaultTiming.DoorCycle, *speed),
		Speed:       *speed, // o motor precisa lembrar o fator pra carimbar a espera em tempo de prédio
	}

	// NotifyContext só REGISTRA o handler; quem cancela é o cancel de baixo. Detalhe que
	// dá nó na cabeça: em TUI o terminal está em raw mode, então ctrl+c chega como TECLA
	// e nem passa por aqui — quem trata aquele caso é o Update. Esse sinal aqui é pro
	// headless e pro kill vindo de fora.
	sigCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithCancel(sigCtx)
	defer cancel()
	if *duration > 0 {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, *duration)
		defer stop()
	}

	sys := simulation.Build(ctx, timing) // Build deriva um ctx filho; cancelar aqui derruba o core

	gen := simulation.NewTrafficGenerator(simulation.TrafficConfig{
		Passengers: *passengers, Seed: *seed,
		Day: dilate(*day, *speed), Profile: simulation.DefaultProfile(),
		Speed: *speed,
	}, sys.HallCalls)

	frames := make(chan ui.Frame, 1)
	pump := ui.NewPump(sys.UIOut, sys.Waits, gen.Stats(), frames, *speed, frameEvery)

	// as duas goroutines de BORDA (as do core moram no WaitGroup do System).
	var edge sync.WaitGroup
	edge.Add(2)
	go func() { defer edge.Done(); gen.Run(ctx) }() // único sender: fecha HallCalls e stats
	go func() { defer edge.Done(); pump.Run(ctx) }()

	cfg := ui.Config{Seed: *seed, Speed: *speed, Day: *day, ASCII: *ascii}
	var err error
	if *headless {
		printPreamble(os.Stdout, gen, cfg, *passengers)
		err = runHeadless(ctx, frames, gen.Profile(), cfg, *progress)
	} else {
		err = runTUI(ctx, frames, gen.Profile(), cfg)
	}

	// ORDEM DO SHUTDOWN — não é decorativa:
	//  1. cancel: destrava o gerador (que pode estar num timer) e o pump nos ctx.Done
	//     deles, e destrava o waitFrame que ficou parado quando o Run da TUI voltou.
	//  2. edge.Wait: junta as duas de borda antes de mexer no core, pra ninguém ficar
	//     mandando pra um canal cujo dono já foi. O defer close(HallCalls) do gerador
	//     roda aqui dentro; o controller vê ok=false e nila o case.
	//  3. sys.Shutdown: cancela o ctx filho e junta controller + 3 carros.
	// Invertendo 2 e 3 nada quebra (todo mundo é guardado por ctx), mas fica torto: o
	// core morreria enquanto a borda ainda drena. E se alguém tirar o cancel() do passo
	// 1, o edge.Wait pendura pra sempre no meio de um dia — é exatamente o tipo de linha
	// que parece redundante e não é.
	cancel()
	edge.Wait()
	sys.Shutdown()

	if err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		return 1
	}
	return 0
}

func dilate(d time.Duration, speed float64) time.Duration {
	return time.Duration(float64(d) / speed)
}

// flag.Visit só passa nas flags que o usuário REALMENTE escreveu na linha de comando.
func flagPassed(name string) (found bool) {
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return
}

func runTUI(ctx context.Context, frames <-chan ui.Frame, prof simulation.Profile, cfg ui.Config) error {
	p := tea.NewProgram(ui.NewModel(ctx, frames, prof, cfg),
		// tela alternativa: ao sair, o terminal volta EXATAMENTE como estava, sem deixar
		// o poço rolado no scrollback.
		tea.WithAltScreen(),
		// ctx também derruba a TUI, com restore. É o que faz um kill -TERM de fora não
		// largar o terminal em raw mode. Redundante com o ctx dos Cmd de propósito: cada
		// um fecha um buraco (este mata o programa, aquele mata a goroutine do recv) e
		// nenhum depende do outro.
		tea.WithContext(ctx),
	)
	// o restore mora no defer interno do Run — inclusive no caminho de panic, porque o
	// catch-panics do bubbletea está ligado por default.
	_, err := p.Run()
	if errors.Is(err, tea.ErrProgramKilled) || errors.Is(err, context.Canceled) {
		return nil // saída por sinal não é falha
	}
	return err
}

// headless existe pra dar pra verificar isso sem TTY. Ele consome exatamente os mesmos
// Frames que a TUI — mesmo pump, mesmos números — só que imprime linha em vez de
// desenhar poço. Se bate aqui, bate lá; não tem dois caminhos de métrica.
//
// Sem deadline interno próprio: -duration já cobre, e duas redes de segurança
// discordando é pior que uma.
func runHeadless(ctx context.Context, frames <-chan ui.Frame, prof simulation.Profile,
	cfg ui.Config, every time.Duration) error {

	report := time.NewTicker(every)
	defer report.Stop()
	var last ui.Frame

	for {
		select {
		case <-ctx.Done():
			printSummary(os.Stdout, last, prof, cfg)
			return nil // ctrl+c / -duration no headless é saída legítima

		case f := <-frames:
			last = f
			if f.Drained() {
				printSummary(os.Stdout, f, prof, cfg)
				return nil
			}

		case <-report.C:
			fmt.Printf("[%7.1fs] gerados %4d/%d  atendidos %4d  esperando %3d  a_bordo %2d  em_voo %2d  média %v\n",
				last.Elapsed.Seconds(), last.Gen.Generated, last.Gen.Target, last.Served,
				last.WaitingTotal, last.Onboard, last.InFlight(),
				last.MeanWait.Round(10*time.Millisecond))
		}
	}
}

func printPreamble(w io.Writer, gen *simulation.TrafficGenerator, cfg ui.Config, n int) {
	p := gen.Profile()
	// gen.Scale() já vem em tempo de prédio, então λ̄, λ* e o pico saem todos em
	// chamadas por segundo SIMULADO — comparáveis com a capacidade abaixo, que também é
	// em segundo de prédio. Misturar as duas unidades aqui daria um "pico/capacidade"
	// de 34x que é só a dilatação aparecendo, não carga nenhuma.
	lambdaBar := gen.Scale() * p.Norm()
	// capacidade grosseira do prédio: 3 carros × 8 lugares por round-trip (em segundos
	// SIMULADOS). Serve só pra dizer em que regime o pico roda — não é modelo de fila, é
	// ordem de grandeza.
	const roundTrip = 13.2
	seats := float64(domain.NumCars*domain.Capacity) / roundTrip
	var peak float64
	for i := 0; i <= 2000; i++ {
		peak = math.Max(peak, gen.Scale()*p.Density(float64(i)/2000)*p.Norm())
	}
	fmt.Fprintf(w, "elevatorsim — headless\n")
	fmt.Fprintf(w, "seed=%d  passageiros=%d  speed=%.1fx  dia=%s (%.1fs de parede)\n",
		cfg.Seed, n, cfg.Speed, cfg.Day, cfg.Day.Seconds()/cfg.Speed)
	fmt.Fprintf(w, "λ̄=%.2f/s  λ*=%.2f/s  aceitação teórica %.1f%%  pico/capacidade≈%.2fx  (por segundo simulado)\n\n",
		lambdaBar, gen.Scale()*p.Sup(), 100*p.Norm()/p.Sup(), peak/seats)
}

func printSummary(w io.Writer, f ui.Frame, prof simulation.Profile, cfg ui.Config) {
	fmt.Fprintf(w, "\n════════════════ RESUMO ════════════════\n")
	fmt.Fprintf(w, "  passageiros gerados      %d / %d\n", f.Gen.Generated, f.Gen.Target)
	fmt.Fprintf(w, "  embarques atendidos      %d\n", f.Served)
	fmt.Fprintf(w, "  espera média             %-11s <- contadores cumulativos no retrato, imune a descarte\n",
		f.MeanWait.Round(time.Millisecond))
	fmt.Fprintf(w, "  espera p95               <= %-8s (resolução de faixa; o máx abaixo é exato)\n",
		f.P95Wait.Round(time.Millisecond))
	fmt.Fprintf(w, "  espera máxima            %s\n", f.MaxWait.Round(time.Millisecond))
	fmt.Fprintf(w, "  vazão                    %.2f/s\n", f.Throughput)
	fmt.Fprintf(w, "  duração                  %.1fs simulados (%.2f dias do perfil)\n",
		f.Elapsed.Seconds(), f.Gen.SimSeconds/math.Max(f.Gen.DaySeconds, 1))
	if f.Gen.Candidates > 0 {
		fmt.Fprintf(w, "  aceitação do thinning    %.1f%%  (%d candidatos)\n",
			100*float64(f.Gen.Generated)/float64(f.Gen.Candidates), f.Gen.Candidates)
	}

	// os invariantes de conservação só fazem sentido num run que DRENOU. Se o -duration
	// (ou um ctrl+c) cortou no meio, gente a bordo e gente na fila é o esperado, não
	// defeito — cuspir "FALHOU" aí seria alarme falso, e alarme falso numa seção de
	// verificação é pior que não ter a seção.
	fmt.Fprintf(w, "\n── invariantes ──\n")
	if !f.Drained() {
		fmt.Fprintf(w, "  run interrompido antes de drenar (-duration/sinal): conservação não se aplica.\n")
		fmt.Fprintf(w, "  em trânsito no corte      %d esperando + %d a bordo + %d em voo\n",
			f.WaitingTotal, f.Onboard, f.InFlight())
	} else {
		fmt.Fprintf(w, "  gerados == atendidos     %d == %d %s\n", f.Gen.Generated, f.Served, ok(f.Gen.Generated == f.Served))
		fmt.Fprintf(w, "  esperando no fim         %-19d %s\n", f.WaitingTotal, ok(f.WaitingTotal == 0))
		fmt.Fprintf(w, "  a bordo no fim           %-19d %s\n", f.Onboard, ok(f.Onboard == 0))
	}

	// o χ² prova que o thinning é um NHPP de verdade em vez de eu só afirmar. Sem a
	// correção de exposição ele não sai: decoração estatística é pior que nada.
	if chi2, df, emin, small := ui.ChiSquarePhase(prof, f.Gen); df > 0 {
		p := ui.ChiSquareP(chi2, df)
		fmt.Fprintf(w, "\n── aderência do gerador a λ(τ) ──\n")
		if math.IsNaN(p) {
			fmt.Fprintf(w, "  χ² = %.1f   gl = %d   p não impresso (gl<30, fora da faixa do Wilson–Hilferty)\n", chi2, df)
		} else {
			fmt.Fprintf(w, "  χ² = %.1f   gl = %d   p ≈ %.2f\n", chi2, df, p)
			// a validade do teste, dita em vez de subentendida: Cochran aguenta E<5 em até
			// ~20% das caselas se nenhuma cair abaixo de 1. A casela magra é sempre a da
			// fase onde o run parou no meio do bin, não um defeito do sorteador.
			cells := df + 1
			fmt.Fprintf(w, "  validade: E_min = %.1f, %d/%d caselas com E<5 (%.0f%%) — Cochran %s\n",
				emin, small, cells, 100*float64(small)/float64(cells),
				ok(emin >= 1 && float64(small) <= 0.2*float64(cells)))
		}
	}

	// essa seção MEDE o que as outras afirmam. Se o stream não perdeu nada, imprime isso
	// mesmo — evidência fabricada seria pior que evidência ausente.
	if f.Served > 0 {
		fmt.Fprintf(w, "\n── a métrica não sai do stream Waits ──\n")
		drop := 100 * (1 - float64(f.WaitsSeen)/float64(f.Served))
		fmt.Fprintf(w, "  stream Waits recebido    %d / %d amostras (%.1f%% descartadas no drop-newest)\n",
			f.WaitsSeen, f.Served, drop)
		if f.WaitsSeen > 0 && f.MeanWait > 0 {
			e := 100 * (float64(f.WaitsNaive)/float64(f.MeanWait) - 1)
			fmt.Fprintf(w, "  média pelo stream        %-11s -> erra %+.1f%% contra a cumulativa\n",
				f.WaitsNaive.Round(time.Millisecond), e)
		}
		if drop < 0.05 {
			// e aqui é onde eu NÃO me empolgo com o próprio argumento: o buffer (64) não
			// encheu nessa escala, então o stream por acaso entregou tudo e as duas médias
			// batem. Isso não valida tirar métrica dele — só diz que essa corrida não
			// estressou o canal. A garantia é estrutural (o cumulativo viaja no retrato e
			// não tem como ficar curto), não empírica.
			fmt.Fprintf(w, "  nesse run o buffer não encheu, então o stream entregou tudo e as duas médias\n")
			fmt.Fprintf(w, "  batem. Não é isso que torna a cumulativa certa: o stream PODE descartar e a\n")
			fmt.Fprintf(w, "  cumulativa não, e é dessa diferença que a média vive — não da sorte do run.\n")
		}
	}
}

func ok(b bool) string {
	if b {
		return "OK"
	}
	return "FALHOU"
}
