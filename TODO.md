# TODO

## Bugs achados na revisão do núcleo

- [x] **`serve()` deixava alvo fantasma** (`simulation/elevator.go`)
      Os bits de pickup servidos eram limpos só na variável local e nunca persistidos no mapa
      antes do `boardHall`, então a releitura logo abaixo ressuscitava o bit original. Efeito:
      `len(targets)` nunca chegava a zero, o carro nunca parava, ficava oscilando em volta do
      andar abrindo a porta a cada ciclo e ainda sujava o `QueueAhead`/`QueueDeferred` que o
      controlador usa pra preçar. Pegava todo hall call que embarcasse alguém — não era canto.
      Correção: persistir os bits limpos no mapa antes de embarcar, aí a releitura só pega
      re-arme de verdade (o de lotação).

- [x] **Chamada sumia no arranque frio** (`simulation/controller.go`)
      Se uma chamada chegasse antes do primeiro frame de telemetria ser processado, o snapshot
      ainda estava vazio, `best` ficava -1 e o `assign` voltava sem despachar e sem
      re-enfileirar. Passageiro simplesmente desaparecia — furava o invariante de que chamada
      não se perde. Correção: semear o snapshot no `NewController` com o andar inicial de cada
      carro (o `Build` já calculava essas posições, agora passa pra frente).

- [x] **ETA errado pra chamada no andar onde o carro está** (`simulation/controller.go`)
      Origem igual ao andar atual com o mesmo sentido caía no branch de reversão e era preçada
      como ~4.4s em vez de ~0, então o carro ideal (parado ali) perdia pra um mais longe.
      Correção: regime 2b, mas checando `State` — carro que já está `StateMoving` saiu do andar
      e vai precisar virar mesmo, esse continua no branch de reversão. Sem esse cuidado a
      correção erra pro outro lado (preça como instantâneo um carro que está indo embora).

## Bugs achados ligando o gerador (o motor só quebrou quando teve carga de verdade)

- [x] **Carro engolia passageiro que dividia destino** (`simulation/elevator.go`)
      `kDropoff` é um BIT no bitmask do andar. Dois passageiros indo pro 7 acendiam o mesmo
      bit e o `serve()` fazia `load--` UMA vez só. O que sobrava ficava a bordo pra sempre,
      o `load` só subia e no fim toda cabine encostava em `Capacity` e parava de embarcar.
      Bitmask não conta, e aqui precisava contar. Correção: mapa `dropoffs[andar]int` do lado
      do bit — o bit continua mandando no LOOK (o andar deve parada, sim/não), o contador diz
      quantos saem lá. Invariante que cai de graça: `load == soma(dropoffs)`.

- [x] **Livelock com a cabine lotada** (`simulation/elevator.go`)
      `shouldServe` não olhava lotação. Carro cheio parava pra embarcar quem não cabia, o
      `boardHall` re-armava o bit de pickup, o `plan()` relia `shouldServe`, dava true de
      novo — e a cabine abria porta no mesmo andar pra sempre. Como nunca saía do lugar,
      nunca desembarcava, nunca vagava um lugar: o prédio inteiro parava na fila. Só aparecia
      com o rush enchendo um carro (ρ>1 no pico), por isso passou batido com pouca gente.
      Correção: lotado + único motivo é embarque -> não para. A chamada não se perde (o bit
      fica em `targets`) e `load>0` garante que existe desembarque em algum andar, então
      sempre sobra pra onde ir.

- [x] **χ² acusava viés que era dele mesmo** (`ui/stats.go`)
      A exposição por bin somava FRAÇÃO DE DIA onde devia contar QUANTAS VEZES o run passou
      pelo bin: `full + (min(partial,hi)-lo)`. Num run de 1.05 dias os primeiros bins são
      varridos DUAS vezes e o esperado tem que dobrar, mas a conta dava 1.02x. Efeito: o teste
      rejeitava 22% das próprias corridas legítimas a α=5% e `-seed 3` imprimia p≈0.00 — o
      resumo acusava o sorteador de não seguir λ(τ) quando o errado era o juiz.
      Sacana de achar: com `full=0` o erro vira fator constante e some na renormalização, e
      num horizonte de exatamente 1.0 dia `partial=0` e ele nem aparece. Só sangrava quem
      estourava o dia — que é metade das corridas.
      Correção: integrar a massa do pedaço direto (`full*MassIn(lo,hi) + MassIn(lo,partial)`).
      Depois: E[χ²]/gl = 1.000 em N=300/1000/3000 e rejeição de volta pros ~5% nominais.
      Coberto por `ui/stats_test.go`, que foi quem pegou.

## Próximos passos

- [x] **Gerador de tráfego com Poisson** — NHPP por thinning de Lewis–Shedler, λ* de limite
      analítico (Σamp), pico no térreo emergindo por marcação. Único sender de `HallCalls`,
      fecha o canal e o `stats` no fim.
- [x] **UI de terminal** (bubbletea) — poço, sentido, lotação, fila por andar, métricas,
      sparkline teórica vs. observada. Consumo por `select` + `ctx.Done()` via `tea.Cmd`.
- [x] **`cmd/`** com o `main` fiando gerador + core + UI, + modo `-headless`.
- [x] **Agregação de métrica** — média/p95/máx saem de contadores cumulativos dentro da
      telemetria, não do stream `Waits`.
- [ ] **Testes com `-race`** — `go test -race ./...`. Prioridade: a virada do LOOK (inverte no
      andar pedido, não vai no extremo à toa) e os regimes do `eta`. O `now` do elevador já é
      injetável pra deixar isso determinístico.
      Obs: no Windows o `-race` exige cgo (`CGO_ENABLED=1` + gcc, tipo MinGW/TDM-GCC). **Ainda
      não rodou**: tentei e o toolchain reclamou `C compiler "gcc" not found`. A ausência de
      corrida continua sendo por construção (escritor único + cópia no canal), não confirmada
      pelo detector.
- [ ] **Teste de regressão pros dois bugs acima** — os dois só apareceram com 1000 passageiros
      e carga no pico. Merecem teste direto: dois passageiros com o mesmo destino têm que
      zerar o `load`; carro lotado parado num andar com fila tem que SAIR do andar.
- [ ] **Cochran com poucos passageiros** — com `-passengers 1000` (o default) o χ² passa na
      regra folgado (1 casela de 46 com E<5). Com 300 são ~40% das caselas, e o resumo diz
      "Cochran FALHOU" — corretamente, é pouco dado pra 48 bins. Fundir caselas adjacentes até
      E>=5 resolveria pros dois casos. Hoje o programa avisa em vez de fingir, que é o
      mínimo aceitável, mas não é o ideal.
- [ ] **Teste do fim-a-fim do headless** — `ui/stats_test.go` cobre o χ², mas a condição de
      dreno (`Drained`) e a ordem de shutdown só são exercitadas rodando o binário na mão.

## Talvez / ideias soltas

- [ ] Calibrar o `reversalPenalty` (2s) com dado do gerador em vez de chute.
- [ ] Ligar o botão manual da cabine na UI — `CabinRequest` e os canais `cabin[i]` já existem,
      só não tem ninguém mandando nada neles.
- [ ] Benchmark do `eta` — hoje é O(paradas) por carro por chamada. Deve ser irrelevante nessa
      escala, mas seria bom medir antes de afirmar.
