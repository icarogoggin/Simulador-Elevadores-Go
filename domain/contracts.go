// Package domain concentra os tipos que trafegam entre as goroutines da
// simulação. Nada de lógica pesada aqui, só os "contratos" e uns helpers.
//
// Regra que se repete no projeto todo: o que atravessa um canal é sempre uma
// cópia fechada em si mesma. Ninguém guarda ponteiro pra estado vivo de outro
// ator, então dá pra rodar sem mutex nenhum e o -race fica quieto.
package domain

import "time"

// Geometria do prédio + as duas constantes que valem como fonte única. Capacity
// é lida tanto pelo embarque do elevador quanto pelo custo do controller, então
// "lotado" significa a mesma coisa nos dois lados.
const (
	MinFloor = 1
	MaxFloor = 10
	NumCars  = 3
	Capacity = 8 // passageiros por cabine
)

// resolução do histograma de espera, em segundos. Fina onde a massa cai (a média
// fica na casa dos 4-5s), cauda comprimida. A última faixa é ABERTA: se estourar,
// quem lê usa o MaxWait, que é exato.
var WaitBucketEdges = [...]float64{0.5, 1, 1.5, 2, 3, 4, 5.5, 7, 9, 12, 16, 21, 28, 38, 50}

// len() de ARRAY é constante de compilação (não vale pra slice), então dá pra usar
// como tamanho de array logo abaixo.
const WaitBuckets = len(WaitBucketEdges) + 1 // = 16, ocupa 64B

// bins da fase do dia, pro histograma de aderência do gerador.
const ProfileBins = 48

func WaitBucket(d time.Duration) int {
	s := d.Seconds()
	for i, e := range WaitBucketEdges {
		if s < e {
			return i
		}
	}
	return WaitBuckets - 1
}

// teto da faixa b. Devolve 0 no balde de estouro — quem chamou tem que cair no
// MaxWait em vez de inventar um número.
func WaitBucketUpper(b int) time.Duration {
	if b < 0 || b >= len(WaitBucketEdges) {
		return 0
	}
	return time.Duration(WaitBucketEdges[b] * float64(time.Second))
}

// Direction é o sinal do movimento. Guardar como int8 em {-1,0,+1} deixa as
// contas sem branch:
//
//	floor += int(d)          // anda um andar no sentido atual
//	int(d)*(t-floor) > 0     // o alvo t tá à frente nesse sentido?
//
// Esse mesmo teste de "tá à frente" serve pro LOOK no motor e pra decisão
// on-the-way/reversão no cálculo de ETA.
type Direction int8

const (
	Down Direction = -1
	Idle Direction = 0
	Up   Direction = +1
)

func (d Direction) String() string {
	switch d {
	case Up:
		return "UP"
	case Down:
		return "DOWN"
	default:
		return "IDLE"
	}
}

// inverter o sentido é só negar (Direction é o sinal). É literalmente a virada do LOOK.
func (d Direction) Opposite() Direction { return -d }

// HallCall é a chamada EXTERNA feita no andar. Carrega a viagem inteira: o
// controller despacha por Origin+Direction e o elevador que ganhar injeta o
// DestFloor como desembarque na hora que a pessoa entra — sem canal de volta pra
// descobrir o destino.
//
// Invariante: Direction == sinal(DestFloor-OriginFloor), nunca Idle numa chamada
// real. CreatedAt é o nascimento, usado pra métrica de espera.
type HallCall struct {
	OriginFloor int
	DestFloor   int
	Direction   Direction
	CreatedAt   time.Time
	PassengerID int64
}

// DispatchOrder = uma HallCall entregue a uma cabine. Mando a chamada toda (não
// só o andar) pro carro conseguir respeitar o sentido e cravar a espera certa.
type DispatchOrder struct{ Call HallCall }

// CabinRequest é o botão apertado DENTRO da cabine (destino). Não é preçado nem
// reatribuído. No core o desembarque sai do HallCall.Dest, então esse caminho
// aqui é opcional — fica pra quando tiver UI com botão manual.
type CabinRequest struct {
	DestFloor   int
	PassengerID int64
}

type CarState uint8

const (
	StateIdle CarState = iota
	StateMoving
	StateDoorsOpen
)

func (s CarState) String() string {
	switch s {
	case StateMoving:
		return "MOVING"
	case StateDoorsOpen:
		return "DOORS_OPEN"
	default:
		return "IDLE"
	}
}

// CabinTelemetry é o retrato que o controller espelha e a UI desenha. É "vence o
// mais novo": jogar fora um frame velho não dói porque o próximo redescreve o
// carro inteiro.
//
// QueueAhead/QueueDeferred são os alvos do LOOK (perto/longe) já em ordem de
// viagem. O controller preça SÓ com esses dois slices e nunca toca na memória
// viva do carro. Os slices são alocados na hora do emit, então dá pra guardar
// sem medo de corrida.
type CabinTelemetry struct {
	CarID         int
	Floor         int
	Direction     Direction
	State         CarState
	QueueAhead    []int
	QueueDeferred []int
	Load          int

	// Waiting[f] = quanta gente espera no andar f já despachada pra ESTE carro.
	// Array, não mapa nem slice, e a escolha é o ponto todo: value type, então a
	// cópia do canal JÁ É o isolamento — não faz alias do pendingHall vivo e não
	// aloca nada por frame. Com mapa, mandar e.pendingHall pelo canal entregaria o
	// mapa vivo e o -race acendia na hora. Índice = número do andar; o 0 fica
	// sobrando de propósito, perder 4 bytes vale mais que espalhar f-1 por tudo e
	// errar um dia. Somando as 3 cabines dá o total do prédio — cada chamada vive
	// em exatamente um pendingHall, então não tem contagem dupla.
	Waiting [MaxFloor + 1]int32

	// daqui pra baixo é tudo CUMULATIVO desde o arranque: monótono, não-decrescente.
	// Fica aqui dentro de propósito — o canal de métrica descarta, e média feita em
	// cima de amostra que pode sumir é média errada. Cumulativo dentro de um retrato
	// vence-o-mais-novo se conserta sozinho no frame seguinte; um "+1" incremental no
	// mesmo canal ficaria torto pra sempre. É por isso que a média sai DAQUI e não do
	// stream Waits.
	Served  int64
	CumWait time.Duration

	// max é idempotente e associativo, então max sobre retratos independentes é tão
	// exato quanto soma sobre cumulativos. Mesma família. Serve de rede pro p95
	// quando a espera estoura o último balde.
	MaxWait time.Duration

	// e o histograma pelo mesmo motivo: média sai de CumWait/Served, mas percentil
	// precisa da distribuição. Esse array é a distribuição mais barata que sobrevive
	// a descarte. Soma balde-a-balde sobre as cabines = o prédio inteiro, porque
	// histograma é aditivo.
	WaitHist [WaitBuckets]int32

	StampedAt time.Time
}

func (t CabinTelemetry) PendingStops() int { return len(t.QueueAhead) + len(t.QueueDeferred) }

// WaitSample fecha o loop da métrica: emitido no instante do embarque, então
// Wait = embarque - CreatedAt é a espera real que o dispatcher tenta reduzir. Já vem
// em tempo de SIMULAÇÃO (o motor aplica o Timing.Speed), igual aos cumulativos da
// telemetria — assim toda duração de espera do projeto fala a mesma língua.
//
// É feed de evento ao vivo, não fonte de métrica: o canal descarta, então média
// tirada daqui é média errada. A média sai dos cumulativos da CabinTelemetry.
type WaitSample struct {
	PassengerID int64
	CarID       int
	Wait        time.Duration
	PickupFloor int
}

// TrafficStat é o retrato do gerador. Mesma lei da telemetria: tudo aqui é NÍVEL
// ou cumulativo, nada é delta, então drop-newest não corrompe.
//
// Não tem campo "Done": o fim é sinalizado FECHANDO o canal. Flag de borda seria a
// única coisa aqui dentro que não se auto-corrige — se o frame com ela caísse, quem
// espera o fim esperava pra sempre. close() não é mensagem, é mudança de estado, e
// não tem como ser descartado.
type TrafficStat struct {
	Generated  int64
	Target     int64
	Candidates int64   // sorteios do thinning (aceitos+rejeitados) -> taxa de aceitação ao vivo
	Lambda     float64 // λ(t) no último candidato, chamadas/s de tempo SIMULADO
	LambdaMax  float64 // o teto do thinning
	Phase      float64 // τ ∈ [0,1)
	SimSeconds float64 // tempo simulado decorrido — o χ² precisa pra corrigir exposição
	DaySeconds float64
	Hist       [ProfileBins]int32 // chegadas ACEITAS por bin de fase
	StampedAt  time.Time
}

// Timing junta os tempos "físicos" do prédio. Controller e motor compartilham o
// MESMO Timing — se divergir, todo ETA sai errado.
type Timing struct {
	FloorTravel time.Duration // tempo pra vencer um andar em movimento
	DoorCycle   time.Duration // abrir + esperar + fechar numa parada

	// de quanto o mundo foi dilatado pra caber no relógio de parede. FloorTravel e
	// DoorCycle acima JÁ vêm divididos por isso; Speed é só a memória do fator, pra
	// quem precisa devolver uma duração medida no relógio de parede pra tempo de
	// prédio. Quem lê é o motor, e só pra carimbar a espera (ver boardHall).
	//
	// Existe porque o histograma de espera é binado por faixas em segundos SIMULADOS,
	// e binagem não-linear não dá pra desfazer depois: converter média e máximo lá na
	// frente funciona (é multiplicação), converter balde não. Então a espera nasce já
	// em tempo de simulação, na fonte, e ninguém rio abaixo precisa saber de speed.
	// Zero = 1 (sem dilatação), pra Timing montado na mão continuar válido.
	Speed float64
}

// fator de dilatação, com o zero tratado como "sem dilatação".
func (t Timing) Dilation() float64 {
	if t.Speed <= 0 {
		return 1
	}
	return t.Speed
}

var DefaultTiming = Timing{
	FloorTravel: 400 * time.Millisecond,
	DoorCycle:   1200 * time.Millisecond,
	Speed:       1,
}
