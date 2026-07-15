# 🏢 Elevatorsim: Go Concurrency & Routing Algorithms in Action

**Elevatorsim** is a high-performance, real-time simulation engine built in Go. It demonstrates advanced concurrency patterns (Actor Model), intelligent routing algorithms, and real-time telemetry without relying on traditional locking mechanisms (`sync.Mutex`).

While it visually simulates a 10-story building with 3 independent elevators, the underlying architecture is a robust foundation for solving complex, real-world distributed systems and logistics problems.

---

## 🎯 Enterprise Vision & Practical Applications

Beyond a visual simulation, the architectural choices and algorithms implemented in **Elevatorsim** have direct applications in large-scale enterprise scenarios:

### 1. Smart Logistics & Fleet Management
Os algoritmos que decidem qual elevador responde a uma chamada (ETA-based Dispatching) e como eles se movem (Algoritmo LOOK) são idênticos aos necessários para roteamento de AGVs (Veículos Guiados Automatizados) em armazéns da Amazon, despacho de frotas (Uber/Logística) ou rotas de drones de entrega. Ele otimiza para tempo mínimo de espera e rendimento máximo.

### 2. High-Concurrency Systems (Actor Model)
Este projeto é um estudo de caso prático na construção de sistemas altamente concorrentes e sem locks (lock-free). Ao isolar o estado dentro das goroutines e comunicar estritamente via canais, essa arquitetura elimina condições de corrida e gargalos de concorrência. Esse mesmo padrão é altamente eficaz para construir:
- Motores de processamento financeiro em tempo real (Trading Engines).
- API Gateways e Microsserviços de alto throughput.
- Gerenciamento de estado de servidores de jogos multiplayer.

### 3. Digital Twins & IoT
Elevatorsim atua como um "Gêmeo Digital" (Digital Twin). Ele processa um Processo de Poisson Não-Homogêneo (NHPP) para simular horários de pico do mundo real (ex: a corrida das 8h da manhã). Empresas podem usar motores semelhantes para ingerir dados de sensores IoT, simular ambientes físicos e prever gargalos de tráfego antes que aconteçam.

---

## 🛠️ The Invisible Architecture (A Arquitetura Invisível)

A simulação é alimentada por 6 goroutines isoladas: 1 Controlador principal, 3 Elevadores independentes, 1 Gerador de Tráfego e 1 "Pump" de Telemetria.

- **Lock-Free State:** Mutações de estado só acontecem dentro da própria goroutine do ator. O Controlador nunca bloqueia esperando um elevador; ele mantém uma réplica em tempo real do estado do mundo via fluxos de telemetria contínuos.
- **Resilient Telemetry ("Drop-Newest"):** Se a UI gráfica ou o consumidor de dados ficar lento, o motor de física não trava. Ele descarta frames visuais obsoletos enquanto preserva contadores matemáticos cumulativos (como o tempo total de espera de todos), garantindo precisão absoluta mesmo sob carga máxima.
- **Smart Dispatching:** O Controlador calcula o tempo estimado de chegada (ETA) em milissegundos, avaliando velocidade atual, direção e carga de passageiros, e delega o atendimento para o veículo mais eficiente na malha.

---

## 🚀 Getting Started

Requerimento mínimo: **Go 1.26+**.

### Rodando a Simulação

```sh
# A estrela do show: Terminal UI (TUI) rodando em tempo real
go run ./cmd/elevatorsim

# MODO GRÁFICO: Um ambiente 2D retro em Pixel Art
go run ./cmd/elevatorsim -pixel

# Modo Headless: Velocidade máxima, sem UI. Perfeito para testes em CI/CD
go run ./cmd/elevatorsim -headless
```

### 📸 Previews (Telas do Sistema)

#### 1. Interface Gráfica 2D (Pixel Art)
> *(Coloque o seu print aqui: Substitua esta imagem salvando o seu screenshot como `docs/pixel_preview.png`)*

![Pixel Art UI Preview](docs/pixel_preview.png)

#### 2. Monitoramento via Terminal (TUI)
> *(Coloque o seu print aqui: Substitua esta imagem salvando o seu screenshot como `docs/tui_preview.png`)*

![Terminal UI Preview](docs/tui_preview.png)

---

## ⚙️ Configuration Flags

Você pode ajustar as regras do motor para simular diferentes cenários de estresse:

- `-pixel`: Substitui o terminal TUI por uma renderização gráfica 2D procedural usando o Ebitengine.
- `-passengers 2000`: Define a população total do teste. A simulação para exatamente na N-ésima chegada.
- `-speed 5`: Dilatação temporal. Escala a física do prédio e as chegadas proporcionalmente, acelerando os testes sem quebrar a proporção de carga real.
- `-fps 20`: Ajusta a taxa de atualização da interface sem afetar a física subjacente.
- `-seed 42`: Geração de tráfego determinístico. Use a mesma *seed* para reprisar exatamente o mesmo padrão de embarques e debugar anomalias.

---

## 🔬 Metrics & Analysis

A simulação gera métricas em tempo real matematicamente rigorosas:
- **Tempos de Espera Cumulativos:** `soma(esperas) / soma(atendidos)` com proteção contra frame-drop.
- **P95 & Max Waits:** Histogramas ao vivo rastreando a experiência da cauda longa.
- **Throughput:** Mede a capacidade real de escoamento do sistema sob o pico extremo das 8h da manhã.

Explore o diretório `simulation/` para ver as engrenagens do motor distribuído, ou `game/` para entender como o **Ebitengine** traduz telemetria em tempo real em gráficos.
