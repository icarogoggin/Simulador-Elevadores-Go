# Elevatorsim — Documentação Técnica

Simulador de elevadores em Go: 3 cabines independentes, dispatcher por ETA, algoritmo LOOK,
tráfego de passageiros gerado por processo de Poisson não-homogêneo (NHPP). Três frontends
(TUI, headless, pixel-art Game Boy) consomem o mesmo stream de dados — nunca divergem em números.

Módulo: `elevatorsim` · Go 1.26.3+ · sem config externo (tudo via flags CLI).

## Índice

1. [Visão geral](#visão-geral)
2. [Como rodar](#como-rodar)
3. [Flags CLI](#flags-cli)
4. [Arquitetura — Actor Model](#arquitetura--actor-model)
5. [Estrutura de diretórios](#estrutura-de-diretórios)
6. [Módulos](#módulos)
7. [Fluxo de dados](#fluxo-de-dados)
8. [Algoritmos-chave](#algoritmos-chave)
9. [Testes](#testes)
10. [Histórico de bugs corrigidos](#histórico-de-bugs-corrigidos)
11. [Roadmap / débito conhecido](#roadmap--débito-conhecido)

---

## Visão geral

Prédio de 10 andares (`MinFloor=1`..`MaxFloor=10`), 3 elevadores (`NumCars=3`), capacidade 8
passageiros/cabine (`Capacity=8`). Um dispatcher central atribui chamadas de andar (hall calls)
ao elevador de menor custo-ETA; cada elevador roda o próprio loop LOOK de forma independente.
Um gerador de tráfego produz chegadas de passageiros seguindo λ(τ) realista (pico manhã/almoço/saída).

Todas as métricas (tempo médio de espera, p95, max, histograma) são acumulativas por cabine —
sobrevivem a frames de UI descartados (política drop-newest).

## Como rodar

```sh
go run ./cmd/elevatorsim              # TUI (bubbletea), padrão
go run ./cmd/elevatorsim -pixel       # modo gráfico Game Boy (Ebitengine)
go run ./cmd/elevatorsim -ascii       # TUI com glifos ASCII (terminais sem unicode)
go run ./cmd/elevatorsim -headless    # sem UI, imprime progresso + resumo (CI-friendly)
go test ./...                         # roda ui/stats_test.go (único arquivo de teste)
```

Requisitos: Go 1.26+ (usa builtin `max` e semântica nova de loop-variable). TUI exige
terminal com ≥28 linhas (checado dinamicamente, não hardcoded). Sair: `q` / `esc` / `ctrl+c`.

`-race` documentado como não testável no Windows atual (falta toolchain cgo/gcc) —
ausência de race condition é por construção do actor model, não confirmada pelo detector.

## Flags CLI

| Flag | Default | Significado |
|---|---|---|
| `-headless` | `false` | Sem TUI; imprime progresso + resumo final. Se `-speed` não for passado explicitamente, força `-speed 30`. |
| `-passengers` | `1000` | Nº exato de passageiros a gerar; simulação para na N-ésima chegada. |
| `-seed` | `1` | Seed do RNG do gerador de chegadas (reprodutível; timing exato de goroutines ainda pode variar). |
| `-speed` | `1` | Fator de dilatação de tempo — escala física (`FloorTravel`/`DoorCycle`) e `-day` juntos, preservando a taxa de ocupação ρ. |
| `-day` | `20m` | Duração do "dia virtual", antes de aplicar `-speed`. |
| `-duration` | `0` (sem limite) | Teto de wall-clock para a execução. |
| `-progress` | `2s` | Intervalo de relatório no modo headless (deve ser > 0). |
| `-fps` | `20` | Taxa de frames da UI. |
| `-ascii` | `false` | Glifos ASCII em vez de unicode. |
| `-pixel` | `false` | Modo gráfico Ebitengine simulando tela Game Boy original (160×144, paleta 4 cores). |

`-pixel` roteia para `runGame()` em `main.go`: janela Ebiten 640×480, título "Elevator Sim -
Pixel Art", consome o mesmo canal `ui.Frame` que TUI/headless via `ui.Pump` compartilhado —
os três modos sempre reportam os mesmos números.

## Arquitetura — Actor Model

**Sem mutex.** Cada goroutine é dona exclusiva do próprio estado; comunicação só via channels.
6 goroutines no total:

- 1 `Controller` (dispatcher)
- `NumCars` (3) `Elevator`
- 1 `TrafficGenerator`
- 1 `Pump` (agregador de UI)

Canal `HallCalls` é o único fechado no sistema (disciplina "exactly one channel closes").
Canais de telemetria/UI usam política **drop-newest**: um renderer lento nunca trava o prédio.
Backpressure em `HallCalls` (buffer 256) é o único ponto que pode legitimamente bloquear.

```
TrafficGenerator ──HallCalls(256,lossless)──▶ Controller ──Dispatch(16)──▶ Elevator×3
                                                   ▲                            │
                                                   └────────Cabin(16)◀──────────┘
                                                   
Elevator×3 ──Telemetry(32,drop-newest)──▶ Controller ──tee──▶ Pump ──▶ UIOut(64)
TrafficGenerator ──stats──▶ Pump
Elevator×3 ──Waits(64)──▶ Pump

Pump ──Frame──▶ { TUI (bubbletea) | headless print | game.Game (Ebitengine) }
```

## Estrutura de diretórios

```
cmd/elevatorsim/main.go   entrypoint: flags, wiring, dispatch de modo, shutdown ordenado
domain/contracts.go       tipos/constantes puros que atravessam channels (sem estado mutável)
simulation/
  assemble.go             Build()/Shutdown() — monta o System (channels + goroutines)
  controller.go           Controller — dispatcher ETA-based
  elevator.go             Elevator — actor por cabine, algoritmo LOOK
  traffic.go              TrafficGenerator — NHPP via thinning de Lewis–Shedler
game/game.go               modo -pixel: renderer Ebitengine estilo Game Boy
ui/
  frame.go                Frame/CarView — snapshot imutável consumido pelas 3 UIs
  model.go                tea.Model (bubbletea) — Init/Update
  view.go                 tea.View — renderização do terminal
  pump.go                 Pump — agrega canais brutos em Frames throttled
  stats.go                teste χ² de aderência do gerador de tráfego
  stats_test.go           testes de calibração do próprio teste χ²
```

## Módulos

### `domain` — contratos
Tipos imutáveis trocados entre goroutines: `Direction`, `HallCall`, `DispatchOrder`,
`CabinRequest` (existe no protocolo mas nenhum sender usa ainda — ver roadmap),
`CarState`, `CabinTelemetry` (snapshot por cabine com contadores cumulativos:
`Served`, `CumWait`, `MaxWait`, `WaitHist[16]`), `WaitSample`, `TrafficStat`, `Timing`.

### `simulation.Controller`
Único dispatcher; mantém `map[int]CabinTelemetry` como única fonte de verdade sobre o
estado de cada cabine. Loop de seleção: telemetria atualiza snapshot + repassa (tee) pra
UI; hall calls são precificadas via `eta()` e mandadas pro carro mais barato (`assign()`).

`eta()` — custo em 3 regimes:
1. carro ocioso
2. carro em rota, mesma direção (2b: caso especial de carro já parado no andar da chamada)
3. reversão (turn-around do LOOK) — penalidade fixa `reversalPenalty=2s`

Sobretaxas: `fullCabSurcharge=5s` (cabine cheia), `loadUnitSurcharge=200ms`/passageiro.

`fold()` — dobra otimisticamente a chamada recém-atribuída no snapshot local, pra um burst
de chegadas Poisson no mesmo tick se espalhar entre carros em vez de empilhar num só.

### `simulation.Elevator`
Um actor por cabine, único escritor do próprio estado (`floor`, `dir`, `state`, `load`,
`targets map[int]stopKind`). Loop dirigido por um único `time.Timer` reutilizado
(FloorTravel vs DoorCycle) + heartbeat de 250ms que reemite telemetria mesmo parado —
garante que `Served` nunca fique permanentemente subcontado por um frame perdido.

`plan()` — núcleo do LOOK: sem alvos → idle; escolhe direção mais próxima a partir de
idle; só reverte quando não sobra nada à frente; reverte no andar **requisitado** mais
distante, não no 1/10 do prédio.

Guarda contra livelock: cabine cheia com única razão "pickup" não para.
`dropoffs map[int]int` conta por passageiro (não só bitmask) — trata corretamente múltiplos
passageiros com mesmo destino.

### `simulation.TrafficGenerator`
NHPP exato via thinning de Lewis–Shedler. λ(τ) = soma de bumps Gaussianos sobre um dia
circular (`DefaultProfile()`: tráfego base inter-andares + pico manhã τ≈0.18 + saída pro
almoço/volta + pico saída τ≈0.82). `pick()` escolhe arquétipo de viagem
(inter-andar/da-lobby/pra-lobby) via teorema da marcação — o rush de lobby "emerge"
matematicamente, sem `if` de horário. Para na N-ésima aceitação (stopping time).

### `ui.Pump`
Único agregador; consome `UIOut`/`Waits`/`stats` e republica `Frame`s throttled em `-fps`.
Compartilhado literalmente pelos 3 modos ("uma verdade, duas/três telas"). `publish()`
reconstrói o `Frame` do zero a cada tick a partir dos snapshots mais recentes por cabine —
um frame perdido não deixa dano permanente. Wait médio = Σcumwait/ΣServed entre cabines.

### `game.Game` (modo `-pixel`)
Resolução interna fixa 160×144 (clássica Game Boy), Ebiten escala pra janela sem borrar
pixel. Paleta 4 cores verde/cinza GB. Sprites gerados **proceduralmente em código**
(sem carregar PNG): retângulo 3×5 pro passageiro, retângulo 12×12 com janela+porta pra
cabine. `Update()` drena o canal de frames mantendo só o mais recente, interpola posição Y
de cada cabine a 0.5px/frame. Consome o mesmo `ui.Frame` que TUI/headless.

## Fluxo de dados

1. `main.go` monta `domain.Timing` dilatado por `-speed`.
2. `simulation.Build()` sobe `Controller` + `NumCars` `Elevator`s.
3. `simulation.NewTrafficGenerator()` sobe o gerador.
4. `ui.NewPump()` agrega tudo em `Frame`s a `-fps`.
5. Dispatch pra exatamente um de `runHeadless` / `runGame` / `runTUI`.
6. Shutdown ordenado: `cancel()` → espera gerador+pump → `sys.Shutdown()` (controller+elevadores).

## Algoritmos-chave

- **LOOK** (`elevator.go: plan()`): elevador continua na direção atual até não haver mais
  chamada pendente nela, só então reverte — evita percorrer o prédio inteiro
  desnecessariamente (diferente do FCFS ingênuo).
- **Dispatch por ETA** (`controller.go: eta()`): custo estimado considerando distância,
  paradas no caminho, penalidade de reversão e lotação da cabine.
- **NHPP / thinning** (`traffic.go`): gera chegadas com taxa variável no tempo respeitando
  a distribuição real λ(τ) sem hardcoding de horário.
- **χ² de aderência** (`ui/stats.go`): valida que o gerador de tráfego realmente reflete
  λ(τ) teórico — corrige exposição parcial de dia e exclui a última chegada (Nth) da
  contagem multinomial. p-valor via aproximação de Wilson–Hilferty (válida pra df≥30).

## Testes

Único arquivo de teste: `ui/stats_test.go` — valida a calibração do próprio teste χ²
(`TestChiSquareNaoInfla`, `TestPValorEhUniforme`, `TestPValorRecusaForaDaFaixa`).

```sh
go test ./...
```

`-race` ainda não roda no Windows local (falta CGO_ENABLED=1 + compilador C, ex. MinGW/TDM-GCC).

## Histórico de bugs corrigidos

(ver `TODO.md` para o log original em PT)

- Alvos fantasma em `elevator.go`: bits de pickup limpos não eram persistidos no mapa antes
  do boarding.
- Hall call chegando antes do primeiro frame de telemetria sumia — corrigido semeando o
  snapshot em `NewController`.
- ETA errado pra chamada no andar atual do carro — corrigido com regime 2b (gated em
  `State != StateMoving`).
- Carro engolia passageiro com mesmo destino de outro (bug bitmask vs. contagem) —
  corrigido com `dropoffs map[int]int`.
- Livelock com cabine cheia — corrigido não parando por razão pickup-only quando cheio.
- Teste χ² mal calibrado (exposição somava "fração do dia" em vez de "quantas vezes o bin
  foi varrido") — corrigido em `ui/stats.go`, pego por `ui/stats_test.go`.

## Roadmap / débito conhecido

- `-race` bloqueado no Windows por falta de toolchain cgo.
- Testes de regressão pros dois bugs encontrados sob carga.
- Validade de Cochran em contagem baixa de passageiros.
- Teste end-to-end headless pra condição `Drained()`.
- `reversalPenalty` calibrado empiricamente (hoje fixo em 2s).
- Botão de cabine manual não conectado — canais já existem (`CabinRequest`, mapa `Cabin`),
  sem sender ainda.
- Benchmark de `eta()`.

---

## Notas sobre assets

`assets/` está no `.gitignore` mas não existe no working tree — chegou a existir com 3 PNGs
(building/cabin/passenger) no commit que introduziu `-pixel`, removidos no commit seguinte
porque `game/game.go` desenha sprites proceduralmente, nunca carregou os arquivos.
`banner.png` continua no repo (~1MB) mas não é mais referenciado no corpo do README atual.
