# 🏢 Elevatorsim: O caos do horário de pico, domado em Go

Se você já esperou um elevador no térreo de um prédio comercial às 8 da manhã, sabe que o tráfego pode virar um pesadelo rápido. Este projeto nasceu para responder a uma pergunta simples: **o despacho inteligente realmente diminui o tempo de espera das pessoas?**

Aqui nós simulamos um prédio de 10 andares e 3 elevadores independentes operando concorrentemente. Tudo rodando no seu terminal, em tempo real, com métricas rolando soltas e os carrinhos subindo e descendo loucamente.

Mas o grande charme do projeto não é só ver os elevadores se movendo na tela. É *como* isso foi construído debaixo do capô. O simulador é uma prova de conceito de três coisas muito legais:

1. **Actor Model sem Mutex:** Cada elevador e o controlador principal vivem em suas próprias goroutines. Eles não dividem memória, não tem trava (`sync.Mutex`), não tem gargalo. Eles conversam estritamente trocando mensagens através de canais.
2. **Algoritmo LOOK:** Se você acha que elevador vai até o último andar à toa, se enganou. Eles invertem o sentido exatamente no andar mais extremo onde foram chamados.
3. **Despacho por ETA:** Quando você aperta o botão no corredor, o cérebro do prédio calcula em milissegundos qual cabine vai chegar até você mais rápido, considerando a distância, as paradas no caminho e até se a cabine está lotada. E o melhor leva.

---

## 🚀 Como ver a mágica acontecendo

O projeto requer o Go 1.26+ (estou usando o `max` nativo e a nova semântica de variáveis de loop). Com ele instalado, é só rodar:

```sh
go run ./cmd/elevatorsim                 # A estrela do show: TUI em tempo real
go run ./cmd/elevatorsim -pixel          # MODO GRÁFICO: Interface visual procedural em Pixel Art estilo Game Boy!
go run ./cmd/elevatorsim -ascii          # Se o seu terminal for antigo e não desenhar caixas direito
go run ./cmd/elevatorsim -headless       # Sem firula visual: apenas o progresso e os números finais (bom pra CI)
```

> **Dica:** O seu terminal precisa ter pelo menos 28 linhas de altura para a interface renderizar os 10 andares sem quebrar! Para sair, basta apertar `q`, `esc` ou `ctrl+c`.

### O que você pode brincar (Flags)

Você pode plugar algumas flags no comando para mudar as regras do jogo:

- `-pixel`: Substitui a interface do terminal por uma UI procedural gráfica simulando a tela de um Game Boy original! 🎮
- `-passengers 2000`: Quantas pessoas colocar no prédio. O laço para exatamente na N-ésima chegada.
- `-speed 5`: Acelera o tempo! Escala a física e a chegada das pessoas juntas, então a carga do prédio continua realista. (No modo headless, a velocidade pula pra 30x automaticamente).
- `-fps 20`: Taxa de quadros por segundo da interface.
- `-seed 42`: Fixa o comportamento caótico. Quer testar o mesmo rush do dia anterior? Use a mesma seed. Apenas lembre que o escalonador do Go pode variar os milissegundos exatos de processamento.

### O que a tela vai te mostrar

Um corte transversal do prédio. 
- **Os andares (1 ao 10)** com uma coluna dedicada a cada elevador.
- **Os passageiros:** Células mostrando `▲` (subindo), `▼` (descendo), `◧` (portas abertas) ou `■` (parado). E, se piscar vermelho, significa que lotou.
- **Gráficos ao vivo:** Na parte inferior, duas "sparklines" mostram o tráfego teórico do dia contra o tráfego real. Dá pra ver claramente o pico das 8h se formando e esvaziando. 
- **Métricas:** Os tempos de espera sendo devorados pelo algoritmo em tempo real (média, p95, máximas).

---

## 🧠 A Arquitetura Invisível

São 6 goroutines orquestrando tudo: 1 para o controlador, 3 para os elevadores, 1 pro gerador de tráfego e 1 "pump" para empurrar tudo pra tela.

Como não usamos lock (`sync.Mutex`), quem garante que o prédio não pega fogo? Simples: **quem escreve no estado de um ator é sempre ele mesmo.** As informações saem como cópias imutáveis por meio dos canais. O controlador, por exemplo, nunca bloqueia esperando um elevador responder. Ele guarda uma "réplica mental" (telemetria) do estado mais recente de cada cabine e toma decisões baseado nisso.

Se a interface visual engasgar e demorar para ler os canais, o prédio não para. Os canais da UI (`UIOut`) descartam o frame mais antigo ("drop-newest"). Se perder um frame, o próximo já vem com a foto atualizada de todo mundo.

O único momento em que a corda estica de verdade é quando o canal `HallCalls` lota. Aí o gerador de tráfego toma um "backpressure" e espera. Em um prédio de verdade, ninguém desaparece misteriosamente porque o corredor encheu, certo?

---

## 🔬 Alguns detalhes que dão orgulho

- **As Métricas não mentem:** Calcular a média de espera lendo eventos de um canal que pode pular pacotes ("drop-newest") seria um desastre silencioso. Para consertar isso, os elevadores transmitem contadores *cumulativos* dentro da própria telemetria. Assim, a média sai matematicamente perfeita de `soma(esperas) / soma(atendidos)`, mesmo que a interface pisque.
- **Tráfego Estocástico Realista:** Eu não programei "dê um pico de pessoas às 8h". O sistema roda uma simulação de Poisson Não-Homogêneo. Eu alimento arquétipos (ida pro trabalho, saída pro almoço) e o limite matemático faz a mágica. O caos das manhãs *emerge* naturalmente no algoritmo.
- **Reconhecimento de Virada:** Os elevadores entendem que se precisarem inverter o sentido de viagem para atender alguém, essa pessoa pode esperar, porque existe uma penalidade fixa de tempo na matemática do ETA do controlador. Mas se o elevador estiver muito perto, ele vira mesmo assim.

Se quiser ver as entranhas disso tudo ou debugar comigo, abra o código em `simulation/` (o motor de tudo) ou `cmd/` (como a UI amarra tudo). 

Pegue seu café, ajuste a altura do terminal e divirta-se! ☕
