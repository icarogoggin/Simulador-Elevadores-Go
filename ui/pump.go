package ui

import (
	"context"
	"time"

	"elevatorsim/domain"
)

// converte duração medida no relógio de parede pra tempo de SIMULAÇÃO. Com -speed 30
// o dia roda 30x mais rápido, inclusive as portas — então 150ms de parede são 4.5s de
// prédio. Sem isso os números na tela mudariam de significado conforme a flag, o que
// seria uma bela armadilha.
func simDur(d time.Duration, speed float64) time.Duration {
	return time.Duration(float64(d) * speed)
}

// Pump é o ator agregador, e existe por causa de impedância: o motor emite no ritmo
// do motor (centenas de transições/s) e o olho lê a 20fps. Se a UI consumisse o UIOut
// evento-a-evento, cada telemetria viraria um Update+View — caro, inútil (o frame N-1
// nunca chegou à tela) e, pior, deixaria o uiOut encher, o que faz o controller
// descartar EXATAMENTE nos picos, que é quando a informação vale.
//
// É o único consumidor de UIOut/Waits/stats. Escritor único, só canal, ctx.Done no
// primeiro case — ator igual aos outros.
//
// Dividendo decisivo: é o MESMO código no TUI e no headless. Uma verdade pros
// números, dois desenhos — os dois modos não têm como discordar sobre a média.
type Pump struct {
	uiOut <-chan domain.CabinTelemetry
	waits <-chan domain.WaitSample
	stats <-chan domain.TrafficStat
	out   chan<- Frame
	speed float64
	fps   time.Duration

	// estado próprio — escritor único (Run)
	last     [domain.NumCars]domain.CabinTelemetry
	seen     [domain.NumCars]bool
	recent   [recentN]domain.WaitSample
	nRecent  int
	gen      domain.TrafficStat
	genDone  bool
	naiveN   int64
	naiveSum time.Duration
	start    time.Time
}

func NewPump(
	uiOut <-chan domain.CabinTelemetry,
	waits <-chan domain.WaitSample,
	stats <-chan domain.TrafficStat,
	out chan<- Frame,
	speed float64,
	fps time.Duration,
) *Pump {
	return &Pump{uiOut: uiOut, waits: waits, stats: stats, out: out, speed: speed, fps: fps}
}

func (p *Pump) Run(ctx context.Context) {
	tick := time.NewTicker(p.fps)
	defer tick.Stop()
	p.start = time.Now()

	for {
		select {
		case <-ctx.Done():
			return

		case t := <-p.uiOut: // coalesce: o mais novo vence, sem fila
			if t.CarID >= 0 && t.CarID < domain.NumCars {
				p.last[t.CarID], p.seen[t.CarID] = t, true
			}

		case s := <-p.waits:
			// SÓ o feed ao vivo e o contador da evidência. Nenhum agregado sai daqui:
			// esse canal descarta, e média de amostra que pode sumir é média errada.
			p.recent[p.nRecent%recentN] = s
			p.nRecent++
			p.naiveN++
			p.naiveSum += s.Wait

		case st, ok := <-p.stats:
			if !ok {
				// sender único fechou. Parqueia o case pra não girar em vazio — mesmo
				// idiom que o controller faz com o hallCalls.
				p.stats = nil
				p.genDone = true
				continue
			}
			p.gen = st

		case <-tick.C:
			p.publish()
		}
	}
}

// publish remonta o Frame DO ZERO a cada tick, a partir dos últimos retratos. Nada
// aqui é incremental, e isso é a decisão inteira: recalcular do último retrato é o que
// faz um frame perdido lá atrás não deixar sequela nenhuma.
//
// Nota: MeanWait é soma(CumWait)/soma(Served) sobre retratos de instantes diferentes.
// É a média EXATA do multiconjunto de embarques que os frames em mãos refletem. Pode
// atrasar milissegundos; nunca é contaminada.
func (p *Pump) publish() {
	f := Frame{Gen: p.gen, GenDone: p.genDone, WaitsSeen: p.naiveN}
	f.Elapsed = simDur(time.Since(p.start), p.speed)

	var cum time.Duration
	for i := range p.last {
		t := p.last[i]
		f.Seen[i] = p.seen[i]
		f.Cars[i] = CarView{Floor: t.Floor, Dir: t.Direction, State: t.State,
			Load: t.Load, Stops: t.PendingStops()}
		f.Onboard += t.Load
		f.Served += t.Served
		cum += t.CumWait
		if t.MaxWait > f.MaxWait {
			f.MaxWait = t.MaxWait // max é associativo: exato mesmo sobre retratos de instantes diferentes
		}
		for fl, n := range t.Waiting {
			f.Waiting[fl] += n
			f.WaitingTotal += int(n)
		}
		for b, c := range t.WaitHist {
			f.hist[b] += c // histograma é aditivo
		}
	}
	// nada de simDur nas esperas: elas JÁ chegam em tempo de simulação, carimbadas lá
	// no boardHall. Dilatar de novo aqui multiplicaria o speed duas vezes. O Elapsed
	// acima é a exceção e o motivo é simples — ele é medido no relógio DESTE ator, que
	// é parede, então é o único que ainda precisa converter.
	if f.Served > 0 {
		f.MeanWait = cum / time.Duration(f.Served)
	}
	f.P95Wait = percentile(f.hist, 0.95, f.MaxWait)
	if p.naiveN > 0 {
		f.WaitsNaive = p.naiveSum / time.Duration(p.naiveN)
	}
	if s := f.Elapsed.Seconds(); s > 0 {
		f.Throughput = float64(f.Served) / s // em /s SIMULADO: comparável com o λ̄
	}
	f.Recent, f.RecentN = p.recent, p.nRecent

	// drop-newest e último elo da cadeia. Se a UI ainda não pegou o frame anterior, esse
	// morre aqui e não perde nada — Frame é vence-o-mais-novo e o próximo vem em 50ms.
	// O que não pode NUNCA é o pump ficar preso esperando a UI: seria o único jeito de a
	// tela empurrar backpressure de volta pro motor.
	//
	// E o pump NÃO para ao drenar: segue republicando, então o frame com Drained()==true
	// é reoferecido a cada tick até alguém ler. O headless não tem como perder o fim.
	select {
	case p.out <- f:
	default:
	}
}
