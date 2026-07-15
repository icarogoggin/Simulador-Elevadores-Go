package simulation

import (
	"context"
	"math"
	"sort"

	"elevatorsim/domain"
)

// penalidade fixa (2s) somada quando o carro tem que inverter a varredura pra
// atender a chamada. Não é exclusão — um carro que vira ainda ganha se estiver
// MUITO mais perto. Só desempata a favor de quem atende sem virar.
const reversalPenalty = float64(2_000_000_000) // 2s em ns

const (
	// cabine cheia: penalidade grande (5s) mas finita. Carro lotado é último
	// recurso, não impossível.
	fullCabSurcharge = float64(5_000_000_000)
	// empurrãozinho por passageiro a bordo (200ms) pra preferir cabine mais vazia.
	loadUnitSurcharge = float64(200_000_000)
)

// Controller é o ator despachante central. Ele tem UM estado mutável — o mapa de
// snapshots — e é a única goroutine que escreve nele. Preça toda chamada contra
// esses retratos e NUNCA faz ida-e-volta síncrona pro elevador (essa é a receita
// clássica de deadlock). Por isso não existe ciclo de bloqueio entre os atores.
type Controller struct {
	timing domain.Timing

	// réplica privada da última telemetria de cada carro. É a única coisa que o
	// pricing lê. Dono: a goroutine do Run.
	snapshot map[int]domain.CabinTelemetry

	// ids ordenados, iterados em ordem fixa pro desempate ser determinístico.
	carIDs []int

	hallCalls <-chan domain.HallCall
	telemetry <-chan domain.CabinTelemetry
	dispatch  map[int]chan<- domain.DispatchOrder
	uiOut     chan<- domain.CabinTelemetry
}

// NewController liga o controller aos canais. dispatch tem uma ponta de envio por
// carro; carIDs sai daí já ordenado.
//
// startFloors diz onde cada carro começa parado. Semeio o snapshot com um retrato
// parado por carro pra o pricing nunca estar vazio num arranque frio: uma chamada
// que chega antes do primeiro frame de telemetria real é preçada contra a posição
// inicial de verdade, em vez de ser jogada fora. O primeiro frame real sobrescreve
// a semente.
func NewController(
	timing domain.Timing,
	startFloors map[int]int,
	hallCalls <-chan domain.HallCall,
	telemetry <-chan domain.CabinTelemetry,
	dispatch map[int]chan<- domain.DispatchOrder,
	uiOut chan<- domain.CabinTelemetry,
) *Controller {
	ids := make([]int, 0, len(dispatch))
	for id := range dispatch {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	snapshot := make(map[int]domain.CabinTelemetry, len(dispatch))
	for id, floor := range startFloors {
		snapshot[id] = domain.CabinTelemetry{
			CarID:     id,
			Floor:     floor,
			Direction: domain.Idle,
			State:     domain.StateIdle,
		}
	}
	return &Controller{
		timing:    timing,
		snapshot:  snapshot,
		carIDs:    ids,
		hallCalls: hallCalls,
		telemetry: telemetry,
		dispatch:  dispatch,
		uiOut:     uiOut,
	}
}

// Run é o loop de despacho. ctx.Done é o PRIMEIRO case do select pra derrubar o
// ator na hora, sem travar em canal nenhum.
//
// Detalhe de ordem: telemetria (verdade) sobrescreve qualquer estimativa otimista,
// e quando hallCalls fecha eu zero o handle pra esse case parar de vez em vez de
// rodar em vazio.
func (c *Controller) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return

		case t := <-c.telemetry:
			// verdade de campo entra e depois eu repasso pra UI. Um valor de canal só
			// tem UM receptor, então a UI é alimentada por esse tee, não assinando o
			// canal direto.
			c.snapshot[t.CarID] = t
			c.forwardToUI(t)

		case call, ok := <-c.hallCalls:
			if !ok {
				// sender único fechou a entrada. Parqueia esse case pra não ficar
				// girando em cima de canal fechado. Telemetria continua.
				c.hallCalls = nil
				continue
			}
			c.assign(ctx, call)
		}
	}
}

// repasse pra UI. drop-newest: UI lenta (ou ausente) não pode segurar o controller.
func (c *Controller) forwardToUI(t domain.CabinTelemetry) {
	select {
	case c.uiOut <- t:
	default:
	}
}

// assign preça a chamada contra cada carro e manda pro mais barato, desempate no
// menor id. Depois faz o "fold otimista": já reflete o embarque no snapshot
// daquele carro pra um pico de Poisson no mesmo tick espalhar em vez de empilhar
// tudo num carro só.
func (c *Controller) assign(ctx context.Context, call domain.HallCall) {
	best := -1
	bestCost := math.Inf(1)
	for _, id := range c.carIDs {
		snap, ok := c.snapshot[id]
		if !ok {
			continue // carro ainda não reportou (janela de 1 tick no arranque)
		}
		cost := c.eta(snap, call)
		if cost < bestCost {
			bestCost, best = cost, id
		}
	}
	if best < 0 {
		return // não deve acontecer: o snapshot já vem semeado com todos os carros
	}

	// aqui a entrega TEM que acontecer (chamada perdida = passageiro abandonado),
	// então o envio é guardado por ctx em vez de best-effort.
	order := domain.DispatchOrder{Call: call}
	select {
	case c.dispatch[best] <- order:
	case <-ctx.Done():
		return
	}

	c.snapshot[best] = fold(c.snapshot[best], call)
}

// eta devolve o custo em ns: tempo até a porta do carro vencedor COMEÇAR a abrir
// no andar da chamada (o door cycle da própria chamada não entra na conta).
//
// Três regimes cobrem os quatro fatores exigidos (posição, sentido, paradas na
// fila, on-the-way vs reversão). Os pontos de virada saem das paradas já
// comprometidas, NUNCA dos extremos 1/10 — o preço segue o caminho físico real
// do LOOK.
func (c *Controller) eta(snap domain.CabinTelemetry, call domain.HallCall) float64 {
	ft := float64(c.timing.FloorTravel)
	dc := float64(c.timing.DoorCycle)

	p := snap.Floor
	d := snap.Direction
	cf := call.OriginFloor

	stops := mergeStops(snap.QueueAhead, snap.QueueDeferred)

	var eta float64
	switch {
	case d == domain.Idle:
		// (1) parado e sem fila: corre reto até a chamada.
		eta = float64(abs(p-cf)) * ft

	case d == call.Direction && int(d)*(cf-p) > 0:
		// (2) on-the-way: mesmo sentido E chamada estritamente à frente. Paga um
		// door cycle por parada comprometida entre P e C.
		eta = float64(abs(p-cf))*ft + dc*float64(countBetween(stops, p, cf))

	case cf == p && d == call.Direction && snap.State != domain.StateMoving:
		// (2b) parado EM cima do andar da chamada, mesmo sentido, e não já saindo: o
		// teste estrito do (2) dá 0 aqui, mas o carro tá sentado em C e vai (re)servir
		// na hora. Preçar como reversão de duas pernas passaria por cima do carro
		// ideal parado no lugar exato. Carro já MOVING já saiu de C, esse cai na
		// reversão de baixo mesmo.
		eta = 0

	default:
		// (3) reversão: o carro fecha a varredura atual até o alvo mais distante à
		// frente (E) e só então vira pra alcançar C. E vira P quando não há nada à
		// frente (leg1 == 0).
		e := extremeAhead(stops, p, d)
		leg1 := float64(abs(p-e))*ft + dc*float64(countBetween(stops, p, e))
		leg2 := float64(abs(e-cf))*ft + dc*float64(countBetween(stops, e, cf))
		eta = leg1 + leg2 + reversalPenalty
	}

	// sobretaxa de lotação, em todos os regimes.
	if snap.Load >= domain.Capacity {
		eta += fullCabSurcharge
	} else {
		eta += float64(snap.Load) * loadUnitSurcharge
	}
	return eta
}

// fold reflete um embarque recém-atribuído no snapshot pra um pico espalhar entre
// carros. Anexa o andar de origem a uma CÓPIA do slice — nunca mexe num slice que
// uma telemetria anterior ainda referencia.
func fold(s domain.CabinTelemetry, call domain.HallCall) domain.CabinTelemetry {
	f := call.OriginFloor
	if s.Direction != domain.Idle && int(s.Direction)*(f-s.Floor) > 0 {
		s.QueueAhead = append(append([]int(nil), s.QueueAhead...), f)
	} else {
		s.QueueDeferred = append(append([]int(nil), s.QueueDeferred...), f)
		if s.Direction == domain.Idle {
			// carro parado: aponta ele pro andar da chamada pra os próximos folds
			// desse tick preçarem consistente.
			if f >= s.Floor {
				s.Direction = domain.Up
			} else {
				s.Direction = domain.Down
			}
		}
	}
	return s
}

// junta os alvos perto+longe num slice só pros helpers geométricos. Aloca array
// novo, não faz alias de nenhuma entrada.
func mergeStops(ahead, deferred []int) []int {
	out := make([]int, 0, len(ahead)+len(deferred))
	out = append(out, ahead...)
	out = append(out, deferred...)
	return out
}

// conta paradas ESTRITAMENTE entre a e b (ordem não importa — paga door cycle
// cruzando pra qualquer lado).
func countBetween(stops []int, a, b int) int {
	lo, hi := a, b
	if lo > hi {
		lo, hi = hi, lo
	}
	n := 0
	for _, s := range stops {
		if s > lo && s < hi {
			n++
		}
	}
	return n
}

// alvo mais distante estritamente à frente de p no sentido d — o ponto de virada
// do LOOK. Devolve p quando não há nada à frente.
func extremeAhead(stops []int, p int, d domain.Direction) int {
	e := p
	for _, s := range stops {
		if int(d)*(s-p) > 0 && int(d)*(s-e) > 0 {
			e = s
		}
	}
	return e
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
