package simulation

// Gerador de chegadas por Poisson NÃO-HOMOGÊNEO (NHPP), via thinning de
// Lewis–Shedler (1979). O método é EXATO, não aproximação:
//
//  1. escolho λ* >= sup λ(t);
//  2. sorteio um Poisson HOMOGÊNEO de taxa λ*: dt = -ln(U)/λ*;
//  3. aceito cada candidato em t com probabilidade λ(t)/λ*;
//  4. os aceitos SÃO um NHPP de intensidade λ(t).
//
// A prova cabe em duas linhas: em [t,t+h) o dominante entrega um ponto com prob.
// λ*h + o(h); ele passa no dado com prob. λ(t)/λ*, independente; logo a prob. de
// ponto ACEITO ali é λ(t)h + o(h). A independência entre intervalos disjuntos vem de
// graça do dominante. Isso é a definição do NHPP.
//
// E o -ln(U)/λ é exponencial por transformada inversa: P(dt>x) = P(U < e^-λx) = e^-λx.

import (
	"context"
	"math"
	"math/rand/v2"
	"time"

	"elevatorsim/domain"
)

type kind uint8

const (
	interFloor kind = iota // andar qualquer -> andar qualquer
	fromLobby              // térreo -> cima (chegando pro trabalho)
	toLobby                // cima -> térreo (almoço / indo embora)
)

// uma parcela da intensidade. sigma == 0 => componente CONSTANTE de altura amp.
type component struct {
	kind    kind
	amp, mu float64
	sigma   float64
}

func (c component) at(tau float64) float64 {
	if c.sigma == 0 {
		return c.amp
	}
	// distância CIRCULAR: o dia dá a volta, o sino perto de τ=0 não pode sair cortado.
	d := math.Abs(tau - c.mu)
	if d > 0.5 {
		d = 1 - d
	}
	z := d / c.sigma
	return c.amp * math.Exp(-0.5*z*z)
}

// Profile é o "dia" do prédio. Valor imutável depois de construído — dá pra entregar
// uma cópia pra UI desenhar a curva sem isso virar acoplamento com o motor. É
// matemática pura, não é conversa com ator.
type Profile struct {
	comps []component
	norm  float64 // ∫₀¹ s(τ)dτ, por Simpson
	sup   float64 // Σ amp: limite ANALÍTICO, domina s(τ) em todo τ
}

func DefaultProfile() Profile {
	return NewProfile([]component{
		{kind: interFloor, amp: 0.20},                        // fundo, nunca zera
		{kind: fromLobby, amp: 1.00, mu: 0.18, sigma: 0.045}, // 8h: todo mundo chegando
		{kind: toLobby, amp: 0.30, mu: 0.48, sigma: 0.045},   // almoço, descida
		{kind: fromLobby, amp: 0.28, mu: 0.57, sigma: 0.045}, // almoço, volta
		{kind: toLobby, amp: 0.85, mu: 0.82, sigma: 0.050},   // 18h: saída
	})
}

func NewProfile(comps []component) Profile {
	p := Profile{comps: append([]component(nil), comps...)}
	// sup λ = sup Σᵢcᵢ <= Σᵢ ampᵢ, por construção — cada gaussiana tem teto na própria
	// amplitude. Isso é uma PROVA, e é por isso que não existe varredura de grade aqui:
	// grade pode passar por baixo do pico entre dois pontos, e aí o thinning subamostra
	// o rush e a matemática quebra CALADA. O preço do limite provado é aceitação de
	// ~18%, o que dá uns 5.4k sorteios de float pra 1000 aceitos. É de graça.
	for _, c := range p.comps {
		p.sup += c.amp
	}
	// Simpson e não fórmula fechada de propósito: a normalização continua certa se
	// alguém mexer nas componentes, inclusive no pedacinho de massa que os sinos trocam
	// na volta do dia.
	p.norm = simpson(p.shape, 0, 1, 4096)
	return p
}

func (p Profile) shape(tau float64) float64 {
	var s float64
	for _, c := range p.comps {
		s += c.at(tau)
	}
	return s
}

func (p Profile) Sup() float64                  { return p.sup }
func (p Profile) Norm() float64                 { return p.norm }
func (p Profile) Density(tau float64) float64   { return p.shape(tau) / p.norm } // f(τ)
func (p Profile) MassIn(lo, hi float64) float64 { return simpson(p.Density, lo, hi, 64) }

// pick sorteia o arquétipo DADO que chegou alguém em τ: categórica com pesos λᵢ(τ).
// É o teorema da marcação — λ(t) = Σ λᵢ(t) com cada parcela um Poisson independente,
// então dado que chegou alguém em t a chance de ser da parcela i é λᵢ(t)/λ(t).
//
// É por isso que não existe `if hora_do_pico { origem = térreo }` em lugar nenhum: o
// pico no térreo EMERGE, contínuo, porque a parcela fromLobby domina a mistura quando
// o sino dela sobe. Um if daria descontinuidade dura na borda da janela e um número
// mágico a mais pra afinar. Essa decomposição é o coração do arquivo.
func (p Profile) pick(tau float64, r *rand.Rand) kind {
	u := r.Float64() * p.shape(tau)
	for _, c := range p.comps {
		if u -= c.at(tau); u <= 0 {
			return c.kind
		}
	}
	return p.comps[len(p.comps)-1].kind // guarda contra arredondamento
}

// Simpson clássico, n forçado a par.
func simpson(f func(float64) float64, a, b float64, n int) float64 {
	if n%2 != 0 {
		n++
	}
	h := (b - a) / float64(n)
	s := f(a) + f(b)
	for i := 1; i < n; i++ {
		x := a + float64(i)*h
		if i%2 == 0 {
			s += 2 * f(x)
		} else {
			s += 4 * f(x)
		}
	}
	return s * h / 3
}

// quanta gente "vive" em cada andar. Térreo tem peso porque tráfego de lobby existe
// fora do rush também; o 7 é o inquilino grande, o 10 é cobertura.
var floorWeight = [domain.MaxFloor + 1]float64{0, 1.5, 1.0, 1.2, 1.2, 1.0, 0.9, 2.0, 1.1, 0.9, 0.6}

// sorteia andar por peso. Renormalizar excluindo em vez de rejeitar num laço: o laço
// poderia girar pra sempre num vetor degenerado, isso aqui tem custo O(10) e sempre
// termina.
func pickFloor(r *rand.Rand, skip int, noLobby bool) int {
	skipf := func(f int) bool { return f == skip || (noLobby && f == domain.MinFloor) }
	var total float64
	for f := domain.MinFloor; f <= domain.MaxFloor; f++ {
		if !skipf(f) {
			total += floorWeight[f]
		}
	}
	u := r.Float64() * total
	for f := domain.MinFloor; f <= domain.MaxFloor; f++ {
		if skipf(f) {
			continue
		}
		if u -= floorWeight[f]; u <= 0 {
			return f
		}
	}
	return domain.MaxFloor
}

// origem != destino SEMPRE — e é exatamente por isso que Direction nunca sai Idle.
func (g *TrafficGenerator) trip(k kind) (origin, dest int) {
	switch k {
	case fromLobby:
		return domain.MinFloor, pickFloor(g.rng, 0, true) // noLobby: nunca colide com o térreo
	case toLobby:
		return pickFloor(g.rng, 0, true), domain.MinFloor
	default:
		o := pickFloor(g.rng, 0, false)
		return o, pickFloor(g.rng, o, false) // exclui a origem: nunca sai igual
	}
}

// o==d nunca acontece (trip garante), então o zero não é caso real.
func sign(x int) domain.Direction {
	if x > 0 {
		return domain.Up
	}
	return domain.Down
}

const statsBuffer = 8 // feed do gerador, descartável — vence o mais novo

type TrafficConfig struct {
	Passengers int
	Seed       uint64
	Day        time.Duration // um dia virtual, já com o -speed aplicado
	Profile    Profile

	// o mesmo fator que dilatou o Day acima. O gerador PRECISA pensar em segundos de
	// parede — é ele quem arma os timers de verdade —, mas tudo que ele PUBLICA sai em
	// tempo de prédio, que é a unidade que o resto do programa fala. Sem isso o λ da
	// tela mudaria de significado conforme a flag: a mesma simulação imprimiria
	// λ̄=0.83/s a 1x e λ̄=25/s a 30x, e a segunda é pura ilusão de ótica da dilatação.
	// Zero = 1.
	Speed float64
}

// TrafficGenerator é o ÚNICO sender de HallCalls e do próprio stats — e por ser
// único, é o único que pode fechar os dois. Todo o resto do projeto nunca fecha nada.
type TrafficGenerator struct {
	profile Profile
	target  int64
	day     time.Duration
	rng     *rand.Rand // *rand.Rand NÃO é goroutine-safe. E não precisa ser: um dono só, essa goroutine.

	out   chan<- domain.HallCall
	stats chan domain.TrafficStat

	speed float64 // fator de dilatação; só serve pra publicar em tempo de prédio

	// estado próprio, escritor único (Run)
	emitted, candidates       int64
	lambdaMax, tau, lambda, t float64
	scale                     float64
	hist                      [domain.ProfileBins]int32
}

func NewTrafficGenerator(cfg TrafficConfig, out chan<- domain.HallCall) *TrafficGenerator {
	// PCG quer duas sementes. (seed, seed) deixa estados vizinhos parecidos demais;
	// espalho com a constante do SplitMix64 pra seed=0,1,2 darem fluxos completamente
	// descorrelacionados.
	src := rand.NewPCG(cfg.Seed, cfg.Seed^0x9E3779B97F4A7C15)
	speed := cfg.Speed
	if speed <= 0 {
		speed = 1
	}
	g := &TrafficGenerator{
		profile: cfg.Profile, target: int64(cfg.Passengers), day: cfg.Day,
		rng: rand.New(src), out: out, speed: speed,
		stats: make(chan domain.TrafficStat, statsBuffer),
	}
	g.scale = float64(g.target) / (g.profile.Norm() * cfg.Day.Seconds()) // faz ∫₀^dia λ = target
	g.lambdaMax = g.scale * g.profile.Sup()
	return g
}

func (g *TrafficGenerator) Stats() <-chan domain.TrafficStat { return g.stats }

// Target é quantos passageiros esse gerador vai emitir, nem um a mais.
func (g *TrafficGenerator) Target() int64 { return g.target }

// cópia do perfil pra UI. Valor imutável + matemática pura: desenhar a curva teórica
// não é chamar método de ator.
func (g *TrafficGenerator) Profile() Profile { return g.profile }

// Scale leva a forma s(τ) pra chamadas por segundo SIMULADO: λ(τ) = Scale·s(τ). Por
// dentro o gerador conta em segundos de parede (o g.scale cru), porque é assim que ele
// arma timer; pra fora ele fala a unidade do prédio, igual ao resto do programa.
func (g *TrafficGenerator) Scale() float64 { return g.scale / g.speed }

// Run é o processo de chegadas. Fecha out e stats no fim — pode, porque é o dono
// exclusivo dos dois, e o defer vale inclusive pra saída por ctx.
//
// O relógio anda por time.Timer, nunca por Sleep: sleep não tem como ser
// interrompido por ctx, e "goroutine que só sai quando o sono acabar" é leak com data
// marcada. Aqui todo select que espera tem o Done junto.
//
// O laço para na N-ésima chegada. Como o perfil é PERIÓDICO (τ = (t mod day)/day),
// isso é um TEMPO DE PARADA do processo, não um corte com descarte: o caminho
// amostrado até ali é um NHPP legítimo, sem viés na forma, e a contagem sai
// exatamente igual a -passengers. O preço é o horizonte virar aleatório (~1.0 dia
// ± 3%, porque calibro E[N(dia)] = N), então um run pode encostar no "dia 2".
// Terminação garantida: a componente interFloor é constante 0.20 > 0, então λ nunca
// zera.
func (g *TrafficGenerator) Run(ctx context.Context) {
	defer close(g.out)   // sender único
	defer close(g.stats) // idem. close não pode ser descartado — é assim que o fim é sinalizado.

	start := time.Now()
	timer := time.NewTimer(time.Hour)
	stopTimer(timer) // reusa o idiom que já existe no elevator.go
	defer stopTimer(timer)

	dayS := g.day.Seconds()
	g.report(ctx, false)

	for g.emitted < g.target {
		// --- passo do thinning ---
		for {
			// 1-U põe o uniforme em (0,1], então o log é sempre finito. Com Float64()
			// cru, U==0 daria +Inf e a simulação sumia no infinito.
			g.t += -math.Log(1-g.rng.Float64()) / g.lambdaMax
			g.tau = math.Mod(g.t/dayS, 1) // perfil PERIÓDICO: o dia dá a volta
			g.lambda = g.scale * g.profile.shape(g.tau)
			g.candidates++
			if g.rng.Float64()*g.lambdaMax < g.lambda {
				break // aceito
			}
			// rejeitado: NÃO durmo nele. O tempo do candidato já andou e o processo
			// aceito é exatamente o mesmo — só não acordo o mundo 5x à toa.
		}

		// --- espera até o instante aceito ---
		// prazo ABSOLUTO (start+t), não soma de sleeps: erro de timer não acumula ao
		// longo de milhares de candidatos e o pico não chega atrasado.
		if d := time.Until(start.Add(time.Duration(g.t * float64(time.Second)))); d > 0 {
			timer.Reset(d)
			select {
			case <-timer.C:
			case <-ctx.Done():
				stopTimer(timer)
				return
			}
		} else {
			select { // já atrasado (speed alto): só confere o ctx e segue
			case <-ctx.Done():
				return
			default:
			}
		}

		o, d := g.trip(g.profile.pick(g.tau, g.rng))
		g.emitted++
		call := domain.HallCall{
			OriginFloor: o, DestFloor: d,
			Direction:   sign(d - o),
			CreatedAt:   time.Now(),
			PassengerID: g.emitted,
		}

		// chamada de andar é LOSSLESS: se o buffer (256) encheu eu BLOQUEIO
		// (backpressure) em vez de descartar. Descartar aqui seria um passageiro apagado
		// da existência, e o invariante do projeto inteiro é que chamada não se perde.
		// Guardado por ctx pra o bloqueio nunca virar eterno.
		select {
		case g.out <- call:
		case <-ctx.Done():
			return
		}

		g.hist[int(g.tau*domain.ProfileBins)%domain.ProfileBins]++
		g.report(ctx, false)
	}

	// o ÚNICO envio guardado do stats, e ele é o total FINAL. Os best-effort de cima
	// podem cair à vontade que o próximo conserta — esse aqui não tem próximo, e o
	// headless usa ele pra fechar a conta. Sempre existe um leitor (o pump roda nos
	// dois modos) e tem o Done junto, então isso não pendura.
	g.report(ctx, true)
}

func (g *TrafficGenerator) report(ctx context.Context, guarded bool) {
	// tudo que sai daqui vai em tempo de PRÉDIO: λ dividido pela dilatação, relógio
	// multiplicado por ela. Os dois na mesma direção, então a razão SimSeconds/DaySeconds
	// (que é o que o χ² e o contador de dia usam) não muda — só os nomes passam a dizer a
	// verdade.
	s := domain.TrafficStat{
		Generated: g.emitted, Target: g.target, Candidates: g.candidates,
		Lambda: g.lambda / g.speed, LambdaMax: g.lambdaMax / g.speed, Phase: g.tau,
		SimSeconds: g.t * g.speed, DaySeconds: g.day.Seconds() * g.speed,
		Hist: g.hist, StampedAt: time.Now(),
	}
	if !guarded {
		// drop-newest: retrato cumulativo, quem perder um frame se acerta no próximo.
		select {
		case g.stats <- s:
		default:
		}
		return
	}
	select {
	case g.stats <- s:
	case <-ctx.Done():
	}
}
