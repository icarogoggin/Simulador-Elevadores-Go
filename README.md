# 🏢 Elevatorsim: Concorrência em Go & Algoritmos de Roteamento

![Elevatorsim Banner](docs/banner.png)

O **Elevatorsim** é um motor de simulação em tempo real, de alto desempenho, construído em Go. Ele demonstra padrões avançados de concorrência (Modelo de Atores), algoritmos de roteamento inteligentes e telemetria em tempo real, tudo isso sem depender de mecanismos tradicionais de bloqueio (`sync.Mutex`).

Embora, visualmente, ele simule um prédio de 10 andares com 3 elevadores independentes correndo de um lado para o outro de forma divertida, a **arquitetura por trás disso** é uma base robusta para resolver problemas complexos de logística e sistemas distribuídos no mundo real.

---

## 🎯 Visão Corporativa e Aplicações Práticas

Muito além de uma simulação visual simpática, as escolhas arquiteturais e os algoritmos implementados no **Elevatorsim** têm aplicações diretas em cenários corporativos de larga escala:

### 1. Logística Inteligente e Gestão de Frotas
Os algoritmos que decidem qual elevador responde a uma chamada (Despacho baseado em ETA) e como eles se movem (Algoritmo LOOK) são estruturalmente idênticos aos usados para rotear AGVs (Veículos Guiados Automatizados) em grandes armazéns como os da Amazon, despachar frotas de aplicativo (Uber/Logística) ou calcular rotas de drones de entrega. O foco é sempre o mesmo: **otimizar para o mínimo de tempo de espera e máximo de capacidade de atendimento**.

### 2. Sistemas de Alta Concorrência (Actor Model)
Este projeto é um estudo de caso prático sobre como construir sistemas altamente concorrentes e *lock-free* (livres de travas). Ao isolar o estado dentro das próprias *goroutines* e fazer a comunicação estritamente via canais, a arquitetura elimina condições de corrida e gargalos comuns. Esse mesmo padrão é essencial na engenharia de:
- Motores de processamento financeiro em tempo real (Trading Engines).
- API Gateways e Microsserviços de altíssimo *throughput*.
- Servidores de jogos multiplayer com alto volume de troca de estado.

### 3. Gêmeos Digitais (Digital Twins) e IoT
O Elevatorsim atua como um verdadeiro "Gêmeo Digital". Ele utiliza a matemática de um Processo de Poisson Não-Homogêneo (NHPP) para simular os horários de pico do mundo real (ex: o temido rush das 8h da manhã nos prédios comerciais). Empresas utilizam motores semelhantes para ingerir dados de sensores IoT, simular os limites físicos de seus ambientes e prever gargalos antes que o caos aconteça.

---

## 🛠️ A Arquitetura Invisível

Toda a simulação é orquestrada por **6 goroutines completamente isoladas**: 1 Controlador principal, 3 Elevadores independentes, 1 Gerador de Tráfego e 1 "Bomba" de Telemetria.

- **Estado Lock-Free:** Mutações de estado acontecem exclusivamente dentro da *goroutine* proprietária. O Controlador nunca congela esperando o elevador responder; ele usa fluxos de telemetria para manter um "espelho" em tempo real do mundo.
- **Telemetria Resiliente ("Drop-Newest"):** Se a interface gráfica ficar lenta ou o consumidor engasgar, o motor de física não para. Ele apenas descarta os *frames* visuais mais antigos, mas protege religiosamente os acumuladores matemáticos (como o total de espera), garantindo precisão analítica sob qualquer estresse de carga.
- **Despacho Inteligente (Smart Dispatching):** O Controlador avalia a velocidade, o peso e a direção de todos os veículos da malha e calcula o Tempo Estimado de Chegada (ETA) em questão de milissegundos, delegando a viagem para a melhor opção.

---

## 🚀 Como ver a mágica rodando

Requerimento mínimo: **Go 1.26+**.

### Comandos de Execução

O projeto possui três visualizadores embutidos. Basta abrir o seu terminal e rodar:

```sh
# 1. MODO GRÁFICO: Interface visual 2D procedural em Pixel Art estilo Game Boy! 🎮
go run ./cmd/elevatorsim -pixel

# 2. MODO TUI (Terminal): Monitoramento e gráficos desenhados direto no console
go run ./cmd/elevatorsim

# 3. MODO HEADLESS: Velocidade insana, sem interface. Feito para testes de CI/CD e coleta bruta de dados
go run ./cmd/elevatorsim -headless
```

### 📸 Suas Telas (Previews)

> *(Dica: Faça um screenshot do seu terminal rodando o modo gráfico e o modo TUI, salve como `docs/pixel_preview.png` e `docs/tui_preview.png` para substituir os placeholders abaixo!)*

**Modo Gráfico 2D:**
![Pixel Art UI Preview](docs/pixel_preview.png)

**Modo de Monitoramento via Terminal:**
![Terminal UI Preview](docs/tui_preview.png)

---

## ⚙️ Teste de Estresse (Flags de Configuração)

Você pode alterar as leis da física da simulação usando *flags*:

- `-passengers 2000`: Define a população total do teste (quantas pessoas vão usar o prédio no dia). A simulação para no último passageiro entregue.
- `-speed 5`: Dilatação temporal. Acelera o limite físico do prédio e o intervalo de chegadas dos passageiros mantendo a proporção exata, permitindo testar picos diários em segundos.
- `-fps 20`: Ajusta o peso de renderização visual (apenas para TUI/Pixel), sem atrapalhar a física subjacente.
- `-seed 42`: Fixa a aleatoriedade. Use a mesma *seed* para reproduzir os mesmos passageiros no mesmo exato momento, excelente para testar algoritmos de despacho e comparar desempenhos.

---

## 🔬 Métricas e Análises em Tempo Real

A simulação não é apenas esteticamente agradável; ela compila estatísticas rigorosas:
- **Tempos de Espera Cumulativos:** O cálculo de média (`soma / atendidos`) nunca sofre defasagem visual.
- **Experiência Real (P95 e Máximas):** Uma fila longa demais pode esconder péssimos casos de espera; o P95 escancara os atrasos inaceitáveis.
- **Capacidade (Throughput):** Avalia a vazão total do seu prédio durante o horário de estrangulamento extremo.

Sinta-se à vontade para mergulhar nos diretórios `simulation/` para ver a mágica dos motores distribuídos, ou `game/` para ver como desenhamos a simulação pixel-a-pixel no Ebitengine!
