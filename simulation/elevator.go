package simulation

import (
	"context"
	"sort"
	"time"

	"elevatorsim/domain"
)

// stopKind é um bitmask por andar com os motivos daquele andar dever uma parada.
// Um bool simples seria furada: um mesmo andar pode ter chamada pra subir, pra
// descer E um desembarque ao mesmo tempo, e o "só embarca quem vai no meu
// sentido" só dá pra provar se o carro distingue os três.
type stopKind uint8

const (
	kPickupUp   stopKind = 1 << 0 // alguém esperando aqui pra SUBIR
	kPickupDown stopKind = 1 << 1 // alguém esperando aqui pra DESCER
	kDropoff    stopKind = 1 << 2 // alguém a bordo quer sair aqui
)

// Elevator é o ator de uma cabine. Todo campo abaixo é escrito por ESSA goroutine
// (Run); o que sai vai como cópia via canal. Sem estado compartilhado, sem mutex.
type Elevator struct {
	id     int
	timing domain.Timing

	// estado próprio — escritor único (Run).
	floor       int
	dir         domain.Direction
	state       domain.CarState
	load        int
	targets     map[int]stopKind          // a fila do LOOK: andar -> motivos
	pendingHall map[int][]domain.HallCall // chamadas esperando, pra métrica e re-arme

	// quanta gente a bordo desce em cada andar. Tem que ser CONTAGEM e não o bit
	// kDropoff: bitmask é idempotente, então dois passageiros indo pro 7 acendem o
	// mesmo bit e o serve() só descontava UM do load. O sobrando ficava a bordo pra
	// sempre, o load só subia, e no fim toda cabine encostava em Capacity e parava de
	// embarcar. O bit continua sendo quem manda no LOOK (o andar deve parada, sim/não);
	// este mapa é só quantos saem lá.
	dropoffs map[int]int

	// métrica acumulada do carro. Mora aqui, não num agregador externo, porque é isso
	// que faz ela viajar dentro do retrato e sobreviver a frame descartado.
	served   int64
	cumWait  time.Duration
	maxWait  time.Duration
	waitHist [domain.WaitBuckets]int32

	dispatch  <-chan domain.DispatchOrder
	cabin     <-chan domain.CabinRequest
	telemetry chan<- domain.CabinTelemetry
	waits     chan<- domain.WaitSample

	// now é injetável pra teste determinístico; default time.Now.
	now func() time.Time
}

func NewElevator(
	id, startFloor int,
	timing domain.Timing,
	dispatch <-chan domain.DispatchOrder,
	cabin <-chan domain.CabinRequest,
	telemetry chan<- domain.CabinTelemetry,
	waits chan<- domain.WaitSample,
) *Elevator {
	return &Elevator{
		id:          id,
		timing:      timing,
		floor:       startFloor,
		dir:         domain.Idle,
		state:       domain.StateIdle,
		targets:     make(map[int]stopKind),
		pendingHall: make(map[int][]domain.HallCall),
		dropoffs:    make(map[int]int),
		dispatch:    dispatch,
		cabin:       cabin,
		telemetry:   telemetry,
		waits:       waits,
		now:         time.Now,
	}
}

// heartbeat: re-publica o retrato de tempos em tempos mesmo sem nada acontecer.
// Parece redundante e não é. Telemetria é vence-o-mais-novo, o que só se
// auto-corrige ENQUANTO vier frame novo — e um carro que acabou de esvaziar não
// emite mais nada nunca. Se o último frame dele cair num buffer cheio, o Served
// daquele carro fica curto PRA SEMPRE e o total nunca fecha.
const heartbeatInterval = 250 * time.Millisecond

// Run é o loop do motor. O movimento é tocado por UM time.Timer criado "parado":
// carro idle gasta quase zero CPU — só o heartbeat, que é 4Hz e não anda nada. O
// timer é resetado pra FloorTravel numa perna de movimento e DoorCycle numa perna de
// porta — dois tempos distintos que um Ticker não daria conta.
//
// ctx.Done é o PRIMEIRO case, shutdown limpo sem leak. Na entrada o carro emite o
// retrato parado inicial pro controller já conseguir preçá-lo.
func (e *Elevator) Run(ctx context.Context) {
	timer := time.NewTimer(time.Hour)
	stopTimer(timer) // começa desarmado; carro idle não pode acordar

	// o heartbeat NÃO escala com -speed de propósito: é mecanismo de vivacidade, não
	// física. Dilatar ele junto com o mundo seria confundir as duas coisas.
	hb := time.NewTicker(heartbeatInterval)
	defer hb.Stop()

	e.emit() // publica o snapshot parado inicial

	for {
		select {
		case <-ctx.Done():
			stopTimer(timer)
			return

		case order := <-e.dispatch:
			e.acceptHallCall(order.Call)
			if e.state == domain.StateIdle {
				e.plan(timer) // carro parado tem que religar a varredura
			}
			e.emit()

		case req := <-e.cabin:
			e.acceptCabinRequest(req)
			if e.state == domain.StateIdle {
				e.plan(timer)
			}
			e.emit()

		case <-timer.C:
			e.onMechanicalTick(timer)
			e.emit()

		case <-hb.C:
			e.emit() // só reconstitui o retrato; não mexe em estado nenhum
		}
	}
}

// registra a chamada externa como obrigação de embarque no mesmo sentido. O
// DestFloor NÃO entra aqui de propósito — vira kDropoff só quando a pessoa embarca
// de fato (ver boardHall), aí o desembarque fica self-contained.
func (e *Elevator) acceptHallCall(c domain.HallCall) {
	if c.Direction == domain.Up {
		e.targets[c.OriginFloor] |= kPickupUp
	} else {
		e.targets[c.OriginFloor] |= kPickupDown
	}
	e.pendingHall[c.OriginFloor] = append(e.pendingHall[c.OriginFloor], c)
}

// botão interno = desembarque (o caminho manual opcional). Apertar o andar atual
// não faz nada.
func (e *Elevator) acceptCabinRequest(r domain.CabinRequest) {
	if r.DestFloor != e.floor {
		e.targets[r.DestFloor] |= kDropoff
	}
}

// onMechanicalTick avança a física quando o timer de uma perna dispara.
func (e *Elevator) onMechanicalTick(timer *time.Timer) {
	switch e.state {
	case domain.StateMoving:
		// venceu um andar; serve aqui se valer, senão decide a próxima perna.
		e.floor += int(e.dir)
		if e.shouldServe(e.floor, e.dir) {
			e.serve(e.floor, e.dir)
			e.state = domain.StateDoorsOpen
			timer.Reset(e.timing.DoorCycle)
			return
		}
		e.plan(timer)
	case domain.StateDoorsOpen:
		// porta acabou de fechar; decide a próxima perna.
		e.plan(timer)
	}
}

// plan é a decisão do LOOK — o coração do motor.
//
//  1. sem alvos: para NO LUGAR (dir=Idle, desarma o timer). LOOK não parka no extremo.
//  2. idle: vai pro alvo mais perto (empate -> Up).
//  3. senão, se não sobra nada à frente mas ainda há alvos, INVERTE — é a virada
//     do LOOK, no andar requisitado mais alto/baixo, nunca no 1/10.
//  4. se dá pra servir o andar atual no sentido (talvez novo), abre a porta; senão
//     começa uma perna de movimento.
func (e *Elevator) plan(timer *time.Timer) {
	if len(e.targets) == 0 {
		e.dir = domain.Idle
		e.state = domain.StateIdle
		stopTimer(timer)
		return
	}

	if e.dir == domain.Idle {
		e.dir = e.chooseInitialDir()
	} else if !e.hasAhead(e.dir) {
		e.dir = e.dir.Opposite()
	}

	if e.shouldServe(e.floor, e.dir) {
		e.serve(e.floor, e.dir)
		e.state = domain.StateDoorsOpen
		timer.Reset(e.timing.DoorCycle)
		return
	}

	e.state = domain.StateMoving
	timer.Reset(e.timing.FloorTravel)
}

func (e *Elevator) hasAhead(d domain.Direction) bool { return e.aheadBeyond(e.floor, d) }

// tem algum alvo estritamente além do andar f no sentido d? Como varre os alvos
// REAIS, a virada é no andar requisitado mais alto/baixo — isso é LOOK, não SCAN.
func (e *Elevator) aheadBeyond(f int, d domain.Direction) bool {
	for t := range e.targets {
		if int(d)*(t-f) > 0 {
			return true
		}
	}
	return false
}

// escolhe o sentido do alvo mais perto por |t-floor|, empate -> Up. Só pra sair do Idle.
func (e *Elevator) chooseInitialDir() domain.Direction {
	best := domain.Up
	bestDist := -1
	for t := range e.targets {
		dist := abs(t - e.floor)
		if bestDist == -1 || dist < bestDist {
			bestDist = dist
			if t >= e.floor {
				best = domain.Up
			} else {
				best = domain.Down
			}
		}
	}
	return best
}

// shouldServe é a garantia de "mesmo sentido" pro andar f indo em d.
//
//   - desembarque sempre serve (deixa sair);
//   - cabine no talo NÃO para só pra embarcar — ver abaixo, é livelock;
//   - embarque no mesmo sentido serve;
//   - embarque no sentido OPOSTO sozinho só serve na virada (nada mais à frente),
//     pra ninguém ser carregado pro lado errado mas também não ficar largado no
//     fim da varredura.
func (e *Elevator) shouldServe(f int, d domain.Direction) bool {
	k := e.targets[f]
	if k == 0 {
		return false
	}
	if k&kDropoff != 0 {
		return true
	}

	// lotado e o único motivo de parar aqui é embarque: NÃO para. Sem isso o carro
	// trava de vez — ele abre a porta, o boardHall não consegue embarcar ninguém e
	// re-arma o mesmo bit de pickup, o plan() relê shouldServe, dá true de novo, e a
	// cabine fica abrindo porta no mesmo andar pra sempre. Como ela nunca sai do lugar,
	// nunca desembarca, nunca vaga um lugar: livelock com o prédio inteiro na fila.
	// Só aparece quando o rush enche um carro (ρ>1 no pico), por isso passa batido em
	// simulação pequena.
	//
	// A chamada NÃO se perde: o bit de pickup continua em targets[f] e o carro volta
	// aqui na varredura seguinte, depois de desembarcar alguém. E load>0 garante que
	// existe kDropoff em algum andar (todo mundo a bordo tem destino), então sempre
	// sobra pra onde ir — não tem como isso virar "parado e lotado pra sempre".
	if e.load >= domain.Capacity {
		return false
	}
	if d == domain.Up && k&kPickupUp != 0 {
		return true
	}
	if d == domain.Down && k&kPickupDown != 0 {
		return true
	}
	// só sobrou pickup no sentido oposto: serve se for a virada.
	return !e.aheadBeyond(f, d)
}

// serve executa a parada no andar f indo em d, espelhando a decisão do shouldServe.
// Desembarca, embarca quem pode (mesmo sentido, + os dois sentidos na virada) e
// limpa os bits servidos — apagando o andar quando não sobra nada.
func (e *Elevator) serve(f int, d domain.Direction) {
	k := e.targets[f]
	turnaround := !e.aheadBeyond(f, d)

	// desembarque primeiro: quem sai, sai antes de entrar gente nova. Desce TODO mundo
	// que pediu esse andar, não uma pessoa só — o bit diz que tem desembarque aqui, o
	// contador diz quantos. O boardHall não interfere: ele só registra desembarque pra
	// andar != f, então dropoffs[f] não volta a crescer no meio desta parada.
	if k&kDropoff != 0 {
		k &^= kDropoff
		if n := e.dropoffs[f]; n > 0 {
			e.load -= n
			if e.load < 0 {
				e.load = 0
			}
		}
		delete(e.dropoffs, f)
	}

	boardUp := k&kPickupUp != 0 && (d == domain.Up || turnaround)
	boardDown := k&kPickupDown != 0 && (d == domain.Down || turnaround)

	// limpa os bits que vou servir e PERSISTE isso no mapa ANTES de embarcar. O
	// boardHall pode re-armar um pickup (spillover de lotação) dando OR de volta em
	// e.targets[f]; persistir antes é o que deixa distinguir um re-arme de verdade
	// do bit que acabei de servir. Sem esse write, a releitura lá embaixo pegava o
	// pickup original nunca-limpo e o andar virava alvo fantasma pra sempre.
	if boardUp {
		k &^= kPickupUp
	}
	if boardDown {
		k &^= kPickupDown
	}
	if k == 0 {
		delete(e.targets, f)
	} else {
		e.targets[f] = k
	}

	if boardUp {
		e.boardHall(f, domain.Up)
	}
	if boardDown {
		e.boardHall(f, domain.Down)
	}

	// boardHall pode ter re-armado um pickup (spillover); dobra esse re-arme de
	// verdade de volta — e.targets[f] agora só tem bits setados depois da limpeza.
	k |= e.targets[f] & (kPickupUp | kPickupDown)
	if k == 0 {
		delete(e.targets, f)
	} else {
		e.targets[f] = k
	}
}

// boardHall embarca quem espera no andar f indo em dir. Pra cada um injeta o
// desembarque (kDropoff no DestFloor) e emite um WaitSample. Cabine no talo deixa
// a pessoa esperando E re-arma o pickup pra uma varredura futura — a chamada nunca
// some.
func (e *Elevator) boardHall(f int, dir domain.Direction) {
	queue := e.pendingHall[f]
	kept := queue[:0] // compacta no lugar: mantém quem ainda espera
	for _, c := range queue {
		if c.Direction != dir {
			kept = append(kept, c) // quem vai pro outro lado espera a varredura dele
			continue
		}
		if e.load >= domain.Capacity {
			// sem vaga: continua esperando e re-arma o bit pra depois.
			kept = append(kept, c)
			if dir == domain.Up {
				e.targets[f] |= kPickupUp
			} else {
				e.targets[f] |= kPickupDown
			}
			continue
		}
		e.load++
		// injeta o desembarque: é assim que o destino chega no carro sem canal de volta.
		// O bit acende o alvo; o contador é quem sabe que são DOIS indo pro 7.
		if c.DestFloor != f {
			e.targets[c.DestFloor] |= kDropoff
			e.dropoffs[c.DestFloor]++
		}
		// a espera nasce JÁ em tempo de simulação, aqui, no único lugar que vê o embarque.
		// Com -speed 30 o mundo inteiro roda 30x mais rápido, então 150ms de parede são
		// 4.5s de prédio. Converter aqui e não lá na UI é o que deixa o waitHist ser
		// binado nas faixas certas: média e máximo dá pra dilatar depois (é só
		// multiplicação), balde de histograma não dá — se binar em parede, com speed alto
		// TODA amostra cai no primeiro balde e o p95 vira lixo silencioso.
		w := time.Duration(float64(e.now().Sub(c.CreatedAt)) * e.timing.Dilation())

		// contador PRIMEIRO, stream DEPOIS. O incremento é incondicional; só o send é
		// best-effort. Se o WaitSample morrer no drop-newest a média nem percebe — é
		// literalmente por isso que a média não sai do stream.
		e.served++
		e.cumWait += w
		if w > e.maxWait {
			e.maxWait = w
		}
		e.waitHist[domain.WaitBucket(w)]++

		e.sendWait(domain.WaitSample{
			PassengerID: c.PassengerID,
			CarID:       e.id,
			Wait:        w,
			PickupFloor: f,
		})
	}
	if len(kept) == 0 {
		delete(e.pendingHall, f)
	} else {
		// copia pra fora pra não segurar a cauda do array antigo.
		e.pendingHall[f] = append([]domain.HallCall(nil), kept...)
	}
}

// emit publica um retrato self-contained. drop-newest (não bloqueia) pra um
// controller/UI lento não travar o motor — o próximo frame redescreve o carro.
func (e *Elevator) emit() {
	ahead, deferred := e.snapshotQueues()
	snap := domain.CabinTelemetry{
		CarID:         e.id,
		Floor:         e.floor,
		Direction:     e.dir,
		State:         e.state,
		QueueAhead:    ahead,
		QueueDeferred: deferred,
		Load:          e.load,
		Waiting:       e.waitingByFloor(),
		Served:        e.served,
		CumWait:       e.cumWait,
		MaxWait:       e.maxWait,
		WaitHist:      e.waitHist,
		StampedAt:     e.now(),
	}
	select {
	case e.telemetry <- snap:
	default:
	}
}

// achata o pendingHall num array. Recontar em vez de manter contador incremental é
// de propósito: como o frame pode ser descartado, um delta perderia contagem PRA
// SEMPRE; um retrato recontado só atrasa um tick. O laço é sobre o pendingHall
// (<=10 entradas), não sobre os 10 andares — carro ocioso nem entra no laço.
func (e *Elevator) waitingByFloor() (w [domain.MaxFloor + 1]int32) {
	for f, q := range e.pendingHall {
		if f >= domain.MinFloor && f <= domain.MaxFloor {
			w[f] = int32(len(q))
		}
	}
	return
}

// separa os alvos nos conjuntos perto (ahead) e longe (deferred), cada um alocado
// na hora e ordenado em ordem de viagem — sobe ascendente, desce descendente.
// Nunca faz alias do mapa vivo, então o receptor pode segurar os slices numa boa.
func (e *Elevator) snapshotQueues() (ahead, deferred []int) {
	ahead = make([]int, 0, len(e.targets))
	deferred = make([]int, 0, len(e.targets))
	d := e.dir
	if d == domain.Idle {
		// sem sentido ainda: joga tudo em "ahead" ascendente só pra ter um retrato estável.
		for f := range e.targets {
			ahead = append(ahead, f)
		}
		sort.Ints(ahead)
		return ahead, deferred
	}
	for f := range e.targets {
		if int(d)*(f-e.floor) > 0 {
			ahead = append(ahead, f) // à frente na varredura atual
		} else {
			deferred = append(deferred, f) // atrás/atual: só depois da virada
		}
	}
	if d == domain.Up {
		sort.Ints(ahead)
		sort.Sort(sort.Reverse(sort.IntSlice(deferred)))
	} else {
		sort.Sort(sort.Reverse(sort.IntSlice(ahead)))
		sort.Ints(deferred)
	}
	return ahead, deferred
}

// drop-newest de novo: métrica não pode travar o motor.
func (e *Elevator) sendWait(s domain.WaitSample) {
	select {
	case e.waits <- s:
	default:
	}
}

// stopTimer desarma e drena um tick já disparado, pra um Reset depois não ver um
// disparo fantasma. Idiom clássico quando se reusa UM Timer entre pernas.
func stopTimer(t *time.Timer) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}
