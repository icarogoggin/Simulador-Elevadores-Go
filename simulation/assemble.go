package simulation

import (
	"context"
	"sync"

	"elevatorsim/domain"
)

// tamanhos de buffer dos canais. Nomeados pra a justificativa morar junto do
// número e ninguém sair tunando às cegas.
const (
	// hallCalls é LOSSLESS: headroom pros picos de Poisson. Se encher, o gerador
	// faz backpressure (bloqueia) em vez de descartar uma chamada.
	hallCallsBuffer = 256
	// desacopla o controller de um carro momentaneamente ocupado na porta. Por
	// carro (não compartilhado) pra um não segurar o outro.
	dispatchBuffer = 16
	// braço opcional do botão manual da cabine, por carro.
	cabinBuffer = 16
	// absorve rajadas de transição dos carros no único receptor (controller);
	// tamanho não é crítico (drop-newest, vence o mais novo).
	telemetryBuffer = 32
	// stream de métrica, descartável.
	waitsBuffer = 64
	// feed de render, descartável (vence o mais novo).
	uiOutBuffer = 64
)

// System é o core montado: a ponta de entrada das chamadas, os dois streams que a
// UI consome, e um Shutdown que cancela todas as goroutines e junta elas sem leak.
type System struct {
	// entrada das chamadas. O gerador de tráfego é o ÚNICO sender e o único que
	// pode fechar esse canal. Nada no core fecha.
	HallCalls chan<- domain.HallCall

	// feed de telemetria pra UI (só leitura).
	UIOut <-chan domain.CabinTelemetry
	// stream de espera pra métrica (só leitura).
	Waits <-chan domain.WaitSample

	// pontas dos botões internos por carro (caminho manual opcional).
	Cabin map[int]chan<- domain.CabinRequest

	cancel context.CancelFunc
	wg     *sync.WaitGroup
}

// Build cria todos os canais nos tamanhos definidos, sobe um Controller e NumCars
// Elevators sob um WaitGroup, e devolve um System cujo Shutdown cancela o context
// e junta tudo.
//
// Disciplina de fechamento: exatamente UM canal (HallCalls) é fechado, pelo seu
// único sender externo. Todo o resto — inclusive os multi-sender telemetry e waits
// — NUNCA é fechado; as goroutines saem no ctx.Done e o GC recolhe depois do join.
// Isso mata na raiz o pânico de send em canal fechado.
func Build(ctx context.Context, timing domain.Timing) *System {
	ctx, cancel := context.WithCancel(ctx)

	hallCalls := make(chan domain.HallCall, hallCallsBuffer)
	telemetry := make(chan domain.CabinTelemetry, telemetryBuffer)
	waits := make(chan domain.WaitSample, waitsBuffer)
	uiOut := make(chan domain.CabinTelemetry, uiOutBuffer)

	// canais por carro. Duas visões de cada: o canal bidirecional que crio, e a
	// ponta direcional de envio entregue ao controller / guardada pro caminho manual.
	dispatchSend := make(map[int]chan<- domain.DispatchOrder, domain.NumCars)
	cabinSend := make(map[int]chan<- domain.CabinRequest, domain.NumCars)
	dispatchChans := make(map[int]chan domain.DispatchOrder, domain.NumCars)
	cabinChans := make(map[int]chan domain.CabinRequest, domain.NumCars)

	for id := 0; id < domain.NumCars; id++ {
		d := make(chan domain.DispatchOrder, dispatchBuffer)
		c := make(chan domain.CabinRequest, cabinBuffer)
		dispatchChans[id] = d
		cabinChans[id] = c
		dispatchSend[id] = d
		cabinSend[id] = c
	}

	// espalha as posições iniciais pelo poço pra o pricing do arranque não ser
	// degenerado (todo carro no mesmo andar). Calculado uma vez e usado pra semear o
	// snapshot do controller E pra construir cada carro.
	startFloors := make(map[int]int, domain.NumCars)
	for id := 0; id < domain.NumCars; id++ {
		startFloors[id] = domain.MinFloor + id*(domain.MaxFloor-domain.MinFloor)/max(1, domain.NumCars-1)
	}

	var wg sync.WaitGroup

	controller := NewController(timing, startFloors, hallCalls, telemetry, dispatchSend, uiOut)
	wg.Add(1)
	go func() {
		defer wg.Done()
		controller.Run(ctx)
	}()

	for id := 0; id < domain.NumCars; id++ {
		car := NewElevator(id, startFloors[id], timing, dispatchChans[id], cabinChans[id], telemetry, waits)
		wg.Add(1)
		go func() {
			defer wg.Done()
			car.Run(ctx)
		}()
	}

	return &System{
		HallCalls: hallCalls,
		UIOut:     uiOut,
		Waits:     waits,
		Cabin:     cabinSend,
		cancel:    cancel,
		wg:        &wg,
	}
}

// Shutdown cancela o context (destravando o ctx.Done de cada ator) e espera todas
// as goroutines saírem — teardown sem leak.
func (s *System) Shutdown() {
	s.cancel()
	s.wg.Wait()
}
