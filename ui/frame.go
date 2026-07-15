// Package ui desenha o prédio e agrega a métrica. Só CONSOME canal: nunca chama
// método do Controller nem do Elevator. A única coisa que atravessa essa fronteira
// sem ser canal é o simulation.Profile, que é valor imutável e matemática pura —
// desenhar a curva teórica não é conversa com ator.
package ui

import (
	"math"
	"time"

	"elevatorsim/domain"
)

const recentN = 4

// Frame é o prédio inteiro num instante. Zero ponteiro, zero slice, zero mapa: o
// valor que atravessa o canal é fechado em si mesmo e a cópia é a garantia. Podia
// carregar a CabinTelemetry direto, mas ela tem slices dentro (QueueAhead/Deferred) e
// aí eu teria que ficar argumentando "esses slices são alocados no emit e ninguém
// muta depois". São, mas não quero que a corretude da borda dependa disso — a UI só
// precisa da CONTAGEM de paradas, então converto pra uma view chapada e o assunto
// morre.
//
// Todas as durações aqui já estão em tempo de SIMULAÇÃO (o pump converte). Ver simDur.
type Frame struct {
	Cars [domain.NumCars]CarView
	Seen [domain.NumCars]bool

	Waiting      [domain.MaxFloor + 1]int32
	WaitingTotal int
	Onboard      int
	Served       int64

	MeanWait, P95Wait, MaxWait time.Duration
	Throughput                 float64
	Elapsed                    time.Duration // simulado

	Gen     domain.TrafficStat
	GenDone bool // veio do CLOSE do stats, não de flag em canal que descarta

	Recent  [recentN]domain.WaitSample // ring: feed vivo. NÃO é métrica.
	RecentN int

	// evidência de que a métrica não pode sair do stream, medida em vez de afirmada:
	// o que o stream Waits conseguiu entregar, e a média que sairia dele. A diferença
	// pro MeanWait é o tamanho do erro que a gente evitou.
	WaitsSeen  int64
	WaitsNaive time.Duration

	hist [domain.WaitBuckets]int32
}

type CarView struct {
	Floor int
	Dir   domain.Direction
	State domain.CarState
	Load  int
	Stops int
}

// percentil a partir dos baldes cumulativos. Devolve o TETO do balde onde a acumulada
// cruza p — resolução de faixa, e a UI escreve "<= 12s" em vez de fingir precisão.
// Grosso, sim, mas é um erro LIMITADO e conhecido (a largura do balde); o p95 tirado
// de um stream que descarta erra um tanto ILIMITADO que depende de quem tava lento na
// hora. Trocar erro desconhecido por erro limitado é a decisão inteira.
//
// Interpolar dentro do balde seria inventar precisão que o dado não tem (assumiria
// uniformidade dentro da faixa, o que é falso pra distribuição de espera).
func percentile(h [domain.WaitBuckets]int32, p float64, maxWait time.Duration) time.Duration {
	var total int32
	for _, c := range h {
		total += c
	}
	if total == 0 {
		return 0
	}
	target := int32(math.Ceil(p * float64(total)))
	var acc int32
	for b, c := range h {
		if acc += c; acc >= target {
			if b == domain.WaitBuckets-1 {
				return maxWait // faixa aberta: o máximo exato é melhor que chutar o piso
			}
			return domain.WaitBucketUpper(b)
		}
	}
	return maxWait
}

// quem já apertou o botão mas cuja chamada ainda não foi despachada (tá em voo no
// hallCalls ou no dispatch). Sai por conservação. Pode dar negativo por um instante
// porque o stat do gerador e os frames dos carros são de tempos diferentes — clampo
// em 0 e sigo.
func (f Frame) InFlight() int {
	n := f.Gen.Generated - int64(f.WaitingTotal) - f.Served
	if n < 0 {
		return 0
	}
	return int(n)
}

// gerador acabou, todo mundo embarcou e todo mundo desceu. É o critério de fim do
// headless. Só é confiável por causa de duas decisões lá atrás: o Served vem de
// contador CUMULATIVO no retrato (não de amostra descartável) e o HEARTBEAT garante
// que o último frame de cada carro chega mesmo se um cair. Sem essas duas, essa
// condição penduraria de vez em quando, sem padrão.
func (f Frame) Drained() bool {
	if !f.GenDone || f.Gen.Target == 0 || f.Gen.Generated != f.Gen.Target {
		return false
	}
	for i := range f.Cars {
		if !f.Seen[i] || f.Cars[i].Load > 0 {
			return false
		}
	}
	return f.WaitingTotal == 0 && f.Served == f.Gen.Generated
}
